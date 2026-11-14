package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"pooltune/internal/domain"
)

// These are local accounting fixtures, not database or production tail-latency
// benchmarks. Rate/buffer ceilings are explicit and not deployment defaults.
func BenchmarkAdmissionUncontended(b *testing.B) {
	cfg := config()
	cfg.RatePerSecond = 1e12
	s := newScheduler(b, cfg, snapshot(b, 32, .2))
	c := classification(domain.ClassNormal, "tenant")
	c.QueueDeadline = time.Now().Add(time.Hour)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		p, err := s.Acquire(context.Background(), c)
		if err != nil {
			b.Fatal(err)
		}
		s.Release(p, Finished)
	}
}
func BenchmarkAdmissionContended(b *testing.B) {
	cfg := config()
	cfg.RatePerSecond = 1e12
	s := newScheduler(b, cfg, snapshot(b, 8, .2))
	var ids atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		c := classification(domain.ClassNormal, fmt.Sprintf("tenant-%d", ids.Add(1)))
		c.QueueDeadline = time.Now().Add(time.Hour)
		for pb.Next() {
			p, err := s.Acquire(context.Background(), c)
			if err != nil {
				b.Error(err)
				return
			}
			s.Release(p, Finished)
		}
	})
}
