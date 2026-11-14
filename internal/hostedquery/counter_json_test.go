package hostedquery

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCounterQueryJSONPreservesOldAndNewInt64Identities(t *testing.T) {
	for _, want := range []int64{0, 1, -1, 1234567890123456789, -1234567890123456789, math.MinInt64, math.MaxInt64} {
		digits := strconv.FormatInt(want, 10)
		for _, value := range []string{digits, strconv.Quote(digits)} {
			t.Run(value, func(t *testing.T) {
				var got Counter
				if err := json.Unmarshal([]byte(`{"Database":1,"Role":2,"Query":`+value+`,"Boot":"2026-11-14T12:00:00Z","Calls":42,"ExecutionMS":84}`), &got); err != nil {
					t.Fatal(err)
				}
				if got.Query != want || got.Database != 1 || got.Role != 2 || got.Calls != 42 || got.ExecutionMS != 84 || got.Boot != time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC) {
					t.Fatalf("lost counter identity or fields: %+v", got)
				}
				encoded, err := json.Marshal(got)
				if err != nil || !strings.Contains(string(encoded), `"Query":"`+digits+`"`) {
					t.Fatalf("output must retain exact string identity: %s, %v", encoded, err)
				}
			})
		}
	}
}

func TestCounterQueryJSONRejectsInvalidIdentities(t *testing.T) {
	for _, value := range []string{`9223372036854775808`, `-9223372036854775809`, `"9223372036854775808"`, `"-9223372036854775809"`, `1.5`, `1e3`, `"1.5"`, `"1e3"`, `""`, `" 1"`, `"+1"`, `null`, `true`, `{}`, `[]`} {
		t.Run(value, func(t *testing.T) {
			if err := json.Unmarshal([]byte(`{"Query":`+value+`}`), &Counter{}); err == nil {
				t.Fatalf("accepted invalid query identity %s", value)
			}
		})
	}
}
