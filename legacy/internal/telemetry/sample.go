// Package telemetry persists optional dashboard observations, never control decisions.
package telemetry

import (
	"encoding/json"
	"strconv"
	"time"
)

type Sample struct {
	At       time.Time `json:"at"`
	Provider string    `json:"provider"`
	Total    string    `json:"total"`
	Epoch    string    `json:"epoch"`
	Demo     bool      `json:"demo"`
	Active   int       `json:"active"`
	Queued   int       `json:"queued"`
	Limit    int       `json:"limit"`
	Warm     int       `json:"warm"`
	Rate     *float64  `json:"rate"`
	Delta    string    `json:"-"`
	Seconds  float64   `json:"-"`
}

type counter struct {
	At    time.Time `json:"at"`
	Total string    `json:"total"`
	Epoch string    `json:"epoch"`
	Demo  bool      `json:"demo"`
}

func Decode(value any, now time.Time) (Sample, bool) {
	// This typed allowlist deliberately excludes target names, SQL, reasons and credentials.
	var v struct {
		Mode         string   `json:"mode"`
		Provider     string   `json:"provider"`
		Transactions *counter `json:"transactions"`
		Effective    int      `json:"effective"`
		Active       int      `json:"active"`
		Queued       int      `json:"queued"`
		Warm         int      `json:"warm"`
		Observation  *struct {
			Transactions *counter `json:"transactions"`
			Target       int      `json:"target"`
			Working      int      `json:"working"`
			Idle         int      `json:"idle"`
			Waiting      int      `json:"waiting"`
		} `json:"observation"`
	}
	raw, e := json.Marshal(value)
	if e != nil || json.Unmarshal(raw, &v) != nil {
		return Sample{}, false
	}
	s := Sample{Provider: v.Provider, Active: v.Active, Queued: v.Queued, Limit: v.Effective, Warm: v.Warm}
	c := v.Transactions
	if v.Mode == "pool-tuning" && v.Observation != nil {
		o := v.Observation
		c = o.Transactions
		s = Sample{Provider: "local", Active: o.Working - o.Idle, Queued: o.Waiting, Limit: o.Target, Warm: o.Working}
	} else if v.Mode != "managed-burst" {
		return Sample{}, false
	}
	if s.Provider != "azure" && s.Provider != "google" && s.Provider != "supabase" && s.Provider != "local" {
		return Sample{}, false
	}
	if c == nil || c.Epoch == "" || len(c.Epoch) > 128 || len(c.Total) > 20 || s.Active < 0 || s.Queued < 0 || s.Limit < 0 || s.Warm < 0 {
		return Sample{}, false
	}
	if _, e := strconv.ParseUint(c.Total, 10, 64); e != nil {
		return Sample{}, false
	}
	age := now.Sub(c.At)
	if age < -time.Second || age > 3*time.Second {
		return Sample{}, false
	}
	s.At = c.At
	s.Total = c.Total
	s.Epoch = c.Epoch
	s.Demo = c.Demo
	return s, true
}
func Measure(prior *Sample, s Sample) Sample {
	s.Rate = nil
	s.Delta = "0"
	s.Seconds = 0
	if prior == nil || prior.Epoch != s.Epoch || prior.Provider != s.Provider || prior.Demo != s.Demo {
		return s
	}
	dt := s.At.Sub(prior.At).Seconds()
	a, e := strconv.ParseUint(prior.Total, 10, 64)
	b, f := strconv.ParseUint(s.Total, 10, 64)
	if e != nil || f != nil || dt <= 0 || dt > 3 || b < a {
		return s
	}
	delta := b - a
	rate := float64(delta) / dt
	s.Rate = &rate
	s.Delta = strconv.FormatUint(delta, 10)
	s.Seconds = dt
	return s
}
