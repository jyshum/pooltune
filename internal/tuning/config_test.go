package tuning

import (
	"strings"
	"testing"
)

func TestProfileRequiresPrivateLocalExplicitAuthority(t *testing.T) {
	valid := `{"schema":"local-tuner-1","target":"db","directory":"/private/state","secret_root":"/private/secrets","admin_dsn_file":"/private/secrets/admin","application_dsn_file":"/private/secrets/app","monitor_dsn_file":"/private/secrets/monitor","listen":"127.0.0.1:8181","assets":"/app/ui/dist","bounds":{"minimum":2,"maximum":8,"cpu_grow":0.6,"cpu_backoff":0.8,"memory_grow":0.7,"memory_backoff":0.9},"exclusive_local_pool":true}`
	if _, e := LoadConfig(strings.NewReader(valid)); e != nil {
		t.Fatal(e)
	}
	for _, bad := range []string{strings.Replace(valid, "127.0.0.1:8181", "0.0.0.0:8181", 1), strings.Replace(valid, `"exclusive_local_pool":true`, `"exclusive_local_pool":false`, 1), strings.Replace(valid, `"minimum":2`, `"minimum":0`, 1), strings.Replace(valid, `"assets":"/app/ui/dist"`, `"assets":"/private"`, 1), valid + `{}`, strings.Replace(valid, `"schema":`, `"unknown":1,"schema":`, 1)} {
		if _, e := LoadConfig(strings.NewReader(bad)); e == nil {
			t.Fatal("unsafe profile accepted")
		}
	}
}
