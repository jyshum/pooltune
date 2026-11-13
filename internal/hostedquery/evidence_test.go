package hostedquery

import (
	"math"
	"testing"
	"time"
)

func TestMeasureCounterDelta(t *testing.T) {
	epoch := time.Unix(100, 0)
	before := Counter{Database: 1, Role: 2, Query: -3, Boot: epoch, Reset: epoch, Entry: epoch, Calls: 10, ExecutionMS: 25, Blocks: 100}
	after := before
	after.Calls, after.ExecutionMS, after.Blocks = 14, 33, 120

	observed, err := Measure(before, after, 4)
	if err != nil || observed.Calls != 4 || observed.MeanMS != 2 || observed.BlocksPerCall != 5 {
		t.Fatalf("observation = %+v, error = %v", observed, err)
	}

	cases := map[string]func(*Counter){
		"other query":          func(c *Counter) { c.Query++ },
		"other role":           func(c *Counter) { c.Role++ },
		"other database":       func(c *Counter) { c.Database++ },
		"restart":              func(c *Counter) { c.Boot = c.Boot.Add(time.Second) },
		"reset":                func(c *Counter) { c.Reset = c.Reset.Add(time.Second) },
		"eviction and rebirth": func(c *Counter) { c.Entry = c.Entry.Add(time.Second) },
		"missing entry":        func(c *Counter) { c.Entry = time.Time{} },
		"no calls":             func(c *Counter) { c.Calls = before.Calls },
		"unexpected calls":     func(c *Counter) { c.Calls++ },
		"counter regression":   func(c *Counter) { c.Blocks = before.Blocks - 1 },
		"invalid time":         func(c *Counter) { c.ExecutionMS = math.NaN() },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			next := after
			change(&next)
			if _, err := Measure(before, next, 4); err == nil {
				t.Fatal("accepted invalid counter interval")
			}
		})
	}
}

func TestDetectObjective(t *testing.T) {
	for _, test := range []struct {
		name  string
		means []float64
		want  Decision
	}{
		{"three breaches", []float64{1.1, 1, 2, 0.5, 3}, Investigate},
		{"two breaches and ties", []float64{3, 2, 1, 1, 1}, NoChange},
		{"latest five", []float64{4, 3, 2, 1, 1, 1}, NoChange},
		{"too few", []float64{3, 3, 3, 3}, InsufficientEvidence},
	} {
		t.Run(test.name, func(t *testing.T) {
			var observed []Observation
			for _, mean := range test.means {
				observed = append(observed, Observation{Calls: 4, MeanMS: mean, context: observationContext{query: 7}})
			}
			got, err := DetectObjective(observed, 1)
			if err != nil || got != test.want {
				t.Fatalf("decision = %q, error = %v, want %q", got, err, test.want)
			}
		})
	}
	if _, err := DetectObjective(make([]Observation, 5), 1); err == nil {
		t.Fatal("accepted observations with no measured calls")
	}
}

func TestDetectObjectiveRejectsMixedContexts(t *testing.T) {
	epoch := time.Unix(100, 0)
	before := Counter{Database: 1, Role: 2, Query: -3, Boot: epoch, Reset: epoch, Entry: epoch}
	after := before
	after.Calls, after.ExecutionMS = 4, 8
	one, err := Measure(before, after, 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Counter){
		func(c *Counter) { c.Query-- },
		func(c *Counter) { c.Role++ },
		func(c *Counter) { c.Reset = c.Reset.Add(time.Second) },
	} {
		start, end := before, after
		change(&start)
		change(&end)
		other, err := Measure(start, end, 4)
		if err != nil {
			t.Fatal(err)
		}
		decision, err := DetectObjective([]Observation{one, one, one, other, other}, 1)
		if decision != InsufficientEvidence || err == nil {
			t.Fatalf("mixed context returned %q, %v", decision, err)
		}
	}
}
