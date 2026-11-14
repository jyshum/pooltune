package runtime

import (
	"strings"
	"testing"
)

func TestRESTConfigRejectsAmbiguousProfiles(t *testing.T) {
	for _, raw := range []string{`{}`, `{"Schema":"local-rest-1","Password":"secret"}`, `{} {}`, strings.Repeat(" ", 2<<20)} {
		if _, e := LoadREST(strings.NewReader(raw)); e == nil {
			t.Fatal("invalid REST profile accepted")
		}
	}
}
