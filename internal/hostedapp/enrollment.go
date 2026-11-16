package hostedapp

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"pooltune/internal/hostedquery"
)

// Enrollment identifies an owner-selected target. It does not provide measured
// repair evidence, a function concurrency limit, or permission to replay SQL.
type repairEnrollmentTarget struct {
	Kind, Schema, Name, Signature, Fingerprint, Generation string
	Schedule, CommandDigest                                string
	DatabaseOID, ObjectOID, OwnerOID                       uint32
	JobID                                                  int64
}

type repairEnrollment struct {
	Target         repairEnrollmentTarget
	Window         *hostedquery.ProjectCronWindowPlan `json:",omitempty"`
	GrantedAtEpoch int64
	EnrolledAt     time.Time
}

var errEnrollmentChoice = errors.New("this selection expired or changed; refresh the enrollment page and choose again")

func enrollmentBinding(row projectRecord) []byte {
	return append(projectBinding(row), []byte("/repair-enrollment/v1")...)
}

func (v vault) enrollmentChoice(row projectRecord, target repairEnrollmentTarget, now time.Time) (string, error) {
	choice := struct {
		Target  repairEnrollmentTarget
		Epoch   int64
		Expires time.Time
	}{target, row.Epoch, now.Add(15 * time.Minute)}
	plain, err := json.Marshal(choice)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(v.Seal(nil, nil, plain, enrollmentBinding(row))), nil
}

func (v vault) openEnrollmentChoice(row projectRecord, token string, now time.Time) (repairEnrollmentTarget, error) {
	var choice struct {
		Target  repairEnrollmentTarget
		Epoch   int64
		Expires time.Time
	}
	if len(token) == 0 || len(token) > 16384 {
		return choice.Target, errEnrollmentChoice
	}
	sealed, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return choice.Target, errEnrollmentChoice
	}
	plain, err := v.Open(nil, nil, sealed, enrollmentBinding(row))
	if err != nil || json.Unmarshal(plain, &choice) != nil || choice.Epoch != row.Epoch || row.Epoch <= 0 || !choice.Expires.After(now) || choice.Expires.After(now.Add(16*time.Minute)) {
		return repairEnrollmentTarget{}, errEnrollmentChoice
	}
	return choice.Target, nil
}

var enrollmentCronPattern = regexp.MustCompile(`^(?:\*/([0-9]+)|([0-9]+)-59/([0-9]+)|([0-9]+)) \* \* \* \*$`)

func enrollmentCronCycle(schedule string) (int, int, error) {
	m := enrollmentCronPattern.FindStringSubmatch(schedule)
	if m == nil {
		return 0, 0, errors.New("only fixed UTC cycles from 2 to 60 minutes can be enrolled")
	}
	every, current := 60, 0
	var err error
	switch {
	case m[1] != "":
		every, err = strconv.Atoi(m[1])
		if every == 60 {
			err = errEnrollmentChoice
		}
	case m[2] != "":
		current, err = strconv.Atoi(m[2])
		if err == nil {
			every, err = strconv.Atoi(m[3])
		}
		if current == 0 || every == 60 {
			err = errEnrollmentChoice
		}
	default:
		current, err = strconv.Atoi(m[4])
	}
	if err != nil || every < 2 || every > 60 || 60%every != 0 || current < 0 || current >= every {
		return 0, 0, errors.New("this schedule is outside the supported fixed UTC cycles")
	}
	canonical := fmt.Sprintf("%d * * * *", current)
	if every < 60 {
		if current == 0 {
			canonical = fmt.Sprintf("*/%d * * * *", every)
		} else {
			canonical = fmt.Sprintf("%d-59/%d * * * *", current, every)
		}
	}
	if canonical != schedule {
		return 0, 0, errors.New("this schedule is not in the supported canonical form")
	}
	return every, current, nil
}

func enrollmentCronWindow(schedule string, form url.Values) (*hostedquery.ProjectCronWindowPlan, error) {
	every, current, err := enrollmentCronCycle(schedule)
	if err != nil {
		return nil, err
	}
	w := &hostedquery.ProjectCronWindowPlan{EveryMinutes: every, CurrentMinute: current}
	for key, dest := range map[string]*int{"proposed_minute": &w.ProposedMinute, "deadline_seconds": &w.DeadlineSeconds, "max_runtime_seconds": &w.MaxRuntimeSeconds} {
		value, err := strconv.Atoi(form.Get(key))
		if err != nil {
			return nil, errors.New("enter whole numbers for the allowed schedule and deadline")
		}
		*dest = value
	}
	if w.ProposedMinute < 0 || w.ProposedMinute >= every || w.ProposedMinute == current || w.MaxRuntimeSeconds < 1 || w.MaxRuntimeSeconds > 20 || w.DeadlineSeconds > every*60 || w.DeadlineSeconds < max(current, w.ProposedMinute)*60+w.MaxRuntimeSeconds {
		return nil, errors.New("choose a different minute inside the same cycle, with a deadline that allows both schedules and a runtime from 1 to 20 seconds")
	}
	return w, nil
}
