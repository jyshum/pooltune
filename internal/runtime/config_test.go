package runtime

import (
	"strings"
	"testing"
)

func TestRuntimeConfigRejectsAmbiguousOrUnsafeDeployment(t *testing.T) {
	for _, body := range []string{`{}`, `{"schema":"local-pg-1","password":"no"}`, `{} {}`, strings.Repeat(" ", 2<<20)} {
		if _, err := Load(strings.NewReader(body)); err == nil {
			t.Fatal("invalid deployment accepted")
		}
	}
	if _, err := ListenLocal("0.0.0.0:1234"); err == nil {
		t.Fatal("public local profile")
	}
}
