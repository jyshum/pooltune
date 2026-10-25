package telemetry

import (
	"context"
	"github.com/jackc/pgx/v5"
	"os"
	"pooltune/internal/admin"
	"regexp"
	"sync"
	"time"
)

type Backend struct {
	admin.Backend
	db        *Database
	stream    string
	mu        sync.Mutex
	status    string
	lastWrite time.Time
}
type View struct {
	Enabled   bool      `json:"enabled"`
	Status    string    `json:"status"`
	LastWrite time.Time `json:"last_write"`
	Items     []Bucket  `json:"items"`
}

// WrapEnv adds an independent optional worker. It never returns initialization errors
// to the gateway, and storage work never runs on a control/tuning call path.
func WrapEnv(ctx context.Context, source admin.Backend, suffix string) (*Backend, func()) {
	b := &Backend{Backend: source, status: "disabled"}
	ctx, cancel := context.WithCancel(ctx)
	path := os.Getenv("POOLTUNE_HISTORY_URL_FILE")
	stream := os.Getenv("POOLTUNE_HISTORY_STREAM")
	if path == "" {
		return b, cancel
	}
	b.status = "configuration unavailable"
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`).MatchString(stream) {
		return b, cancel
	}
	db, e := FromFile(path)
	if e != nil {
		return b, cancel
	}
	b.db = db
	b.stream = stream + "-" + suffix
	b.status = "connecting"
	done := make(chan struct{})
	go func() { defer close(done); b.run(ctx) }()
	return b, func() { cancel(); <-done }
}
func (b *Backend) setStatus(s string) { b.mu.Lock(); b.status = s; b.mu.Unlock() }
func (b *Backend) Read(ctx context.Context, resource string) (any, error) {
	if resource != "telemetry" {
		return b.Backend.Read(ctx, resource)
	}
	b.mu.Lock()
	v := View{Enabled: b.db != nil, Status: b.status, LastWrite: b.lastWrite, Items: []Bucket{}}
	b.mu.Unlock()
	if b.db == nil {
		return v, nil
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	items, e := b.db.query(bounded, b.stream)
	if e != nil {
		v.Status = "storage unavailable"
		return v, nil
	}
	v.Items = items
	return v, nil
}
func (b *Backend) run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var c *pgx.Conn
	var prior *Sample
	var retry time.Time
	defer func() {
		if c != nil {
			closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			c.Close(closeCtx)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if time.Now().Before(retry) {
				continue
			}
			bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
			if c == nil {
				var e error
				c, e = b.db.connect(bounded)
				if e == nil {
					e = b.db.initialize(bounded, c)
				}
				if e != nil {
					if c != nil {
						c.Close(bounded)
						c = nil
					}
					cancel()
					b.setStatus("storage unavailable")
					retry = time.Now().Add(10 * time.Second)
					prior = nil
					continue
				}
			}
			value, e := b.Backend.Read(bounded, "state")
			s, ok := Decode(value, time.Now())
			if e != nil || !ok {
				cancel()
				prior = nil
				b.setStatus("waiting for fresh readings")
				continue
			}
			if prior != nil && prior.At.Equal(s.At) && prior.Epoch == s.Epoch && prior.Total == s.Total {
				cancel()
				continue
			}
			sample := Measure(prior, s)
			e = b.db.write(bounded, c, b.stream, sample)
			cancel()
			if e != nil {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
				c.Close(closeCtx)
				closeCancel()
				c = nil
				prior = nil
				b.setStatus("storage unavailable")
				retry = time.Now().Add(10 * time.Second)
				continue
			}
			prior = &s
			b.mu.Lock()
			b.status = "recording"
			b.lastWrite = s.At
			b.mu.Unlock()
		}
	}
}
