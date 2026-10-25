//go:build managedlive

package burst

import "testing"

func TestAdaptiveOnlyLiveSelection(t *testing.T) {
	modes, e := cloudModes("burst")
	if e != nil || len(modes) != 1 || modes[0] != "burst" {
		t.Fatal("adaptive selection expanded the test", modes, e)
	}
	modes, e = cloudModes("")
	if e != nil || len(modes) != 3 {
		t.Fatal("existing comparison changed")
	}
	if _, e = cloudModes("anything"); e == nil {
		t.Fatal("unknown live mode accepted")
	}
}
