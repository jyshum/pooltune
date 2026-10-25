package hostmetrics

import (
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"testing"
	"time"
)

func TestCounterDeltaAndUnavailableSamples(t *testing.T) {
	s := &Sampler{}
	now := time.Now()
	memory := &mem.VirtualMemoryStat{Total: 1000, Available: 250}
	if _, err := s.update(now, cpu.TimesStat{User: 10, Idle: 90}, memory); err == nil {
		t.Fatal("first sample fabricated pressure")
	}
	sample, err := s.update(now.Add(time.Second), cpu.TimesStat{User: 60, Idle: 140}, memory)
	if err != nil || sample.CPU != .5 || sample.Memory != .75 || sample.Source != "local-host" {
		t.Fatalf("wrong pressure %+v %v", sample, err)
	}
	if _, err = s.update(now.Add(2*time.Second), cpu.TimesStat{User: 1, Idle: 1}, memory); err == nil {
		t.Fatal("counter reset became healthy")
	}
	if _, err = s.update(now.Add(3*time.Second), cpu.TimesStat{User: 2, Idle: 2}, &mem.VirtualMemoryStat{}); err == nil {
		t.Fatal("missing memory became zero pressure")
	}
}
