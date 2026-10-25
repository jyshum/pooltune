// Package hostmetrics samples the local database host. Never use these readings
// as CPU/memory evidence for a remote or managed database.
package hostmetrics

import (
	"context"
	"errors"
	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/mem"
	"math"
	"pooltune/internal/domain"
	"sync"
	"time"
)

var ErrUnavailable = errors.New("local host resource sample unavailable")

type Sampler struct {
	mu          sync.Mutex
	at          time.Time
	total, idle float64
}

func (s *Sampler) Sample(ctx context.Context) (domain.Resources, error) {
	counters, err := cpu.TimesWithContext(ctx, false)
	if err != nil || len(counters) != 1 {
		return domain.Resources{}, ErrUnavailable
	}
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return domain.Resources{}, ErrUnavailable
	}
	return s.update(time.Now(), counters[0], memory)
}
func (s *Sampler) update(at time.Time, c cpu.TimesStat, m *mem.VirtualMemoryStat) (domain.Resources, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Guest time is already included in user/nice on Linux. Do not count twice.
	total := c.User + c.Nice + c.System + c.Idle + c.Iowait + c.Irq + c.Softirq + c.Steal
	idle := c.Idle + c.Iowait
	prevAt, prevTotal, prevIdle := s.at, s.total, s.idle
	s.at = at
	s.total = total
	s.idle = idle
	delta := total - prevTotal
	idleDelta := idle - prevIdle
	if prevAt.IsZero() || at.Sub(prevAt) < 100*time.Millisecond || at.Sub(prevAt) > 10*time.Second || !finite(total) || !finite(idle) || !finite(delta) || !finite(idleDelta) || total < 0 || idle < 0 || delta <= 0 || idleDelta < 0 || idleDelta > delta || m == nil || m.Total == 0 || m.Available > m.Total {
		return domain.Resources{}, ErrUnavailable
	}
	return domain.Resources{At: at, CPU: (delta - idleDelta) / delta, Memory: 1 - float64(m.Available)/float64(m.Total), Source: "local-host"}, nil
}
func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
