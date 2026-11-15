package hostedquery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/jackc/pgx/v5"
	"pooltune/internal/supabaseproject"
)

type ownedCronFixtureSettings struct {
	Installation string
	Query        int64
	Request      OwnedCronRequest
}

// ParseOwnedCronFixture gives the hosted worker and the recorder the same exact
// private binding. The caller remains responsible for authorizing its use.
func ParseOwnedCronFixture(binding, password string, ca []byte) (*OwnedProjectCron, error) {
	invalid := errors.New("the owned Rookery Cron fixture configuration is invalid")
	var settings ownedCronFixtureSettings
	if err := readOwnedCronJSON(strings.NewReader(binding), &settings, 16<<10); err != nil {
		return nil, invalid
	}
	project, err := supabaseproject.New(OwnedIndexRookeryRef, settings.Installation)
	if err != nil || settings.Request.Target.Project != project.ID() {
		return nil, invalid
	}
	writer, err := supabaseproject.DirectConfig(project.Ref(), project.OwnedRepairProbeRole("cron"), password, ca)
	if err != nil || !validOwnedCronFixtureRoute(writer, settings.Request) {
		return nil, invalid
	}
	return &OwnedProjectCron{Config: writer, Request: settings.Request, QueryID: settings.Query}, nil
}

// DecodeOwnedCronPeriod accepts the observer's one-period JSON format. It
// preserves bigint query IDs and refuses truncated or appended input.
func DecodeOwnedCronPeriod(input io.Reader) (CronPeriod, error) {
	var period CronPeriod
	err := readOwnedCronJSON(input, &period, 256<<10)
	return period, err
}

func readOwnedCronJSON(input io.Reader, value any, limit int64) error {
	raw, err := io.ReadAll(io.LimitReader(input, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return errors.New("expected one complete bounded Cron JSON object")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '{' || !json.Valid(raw) {
		return errors.New("expected one complete bounded Cron JSON object")
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if err := decode.Decode(value); err != nil {
		return errors.New("Cron JSON did not match the approved format")
	}
	return nil
}

// RecordObservedPeriod binds a normally discovered query from the observer's
// counters. RecordOwnedCronPeriod independently checks the exact PostgreSQL
// query identity, provider history and the current job before saving it.
func (f *OwnedProjectCron) RecordObservedPeriod(ctx context.Context, state *pgx.Conn, period CronPeriod) error {
	request, err := f.requestForObservedPeriod(period)
	if err != nil {
		return err
	}
	return RecordOwnedCronPeriod(ctx, f.Config, state, request, period)
}

func (f *OwnedProjectCron) requestForObservedPeriod(period CronPeriod) (OwnedCronRequest, error) {
	if f == nil {
		return OwnedCronRequest{}, errors.New("Cron recorder requires one owned fixture")
	}
	request := f.Request
	request.Observed = period.Busy.Target.Before
	if !validOwnedCronRoute(f.Config, request) || (f.QueryID != 0 && f.QueryID != request.Observed.Query) {
		return OwnedCronRequest{}, errors.New("Cron period did not match the owned fixture or query")
	}
	return request, nil
}
