package burst

import (
	"strings"
	"testing"
)

func TestBurstProfileRejectsUnsafeAndUnknownConfiguration(t *testing.T) {
	for _, raw := range []string{`{}`, `{"schema":"burst-gateway-1","unexpected":true}`, `{"schema":"burst-gateway-1"} {}`} {
		if _, e := LoadProfile(strings.NewReader(raw)); e == nil {
			t.Fatal("unsafe profile accepted")
		}
	}
}
