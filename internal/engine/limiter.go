package engine

import (
	"context"
	"io"
	"sync"
	"time"
)

// Limiter is a shared token bucket that caps upload speed for all uploads
// together. Rate returns bytes per second; 0 means unlimited.
type Limiter struct {
	Rate   func() int64
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

// Wait blocks until n bytes may be sent.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	for {
		rate := l.Rate()
		if rate <= 0 {
			return ctx.Err()
		}
		l.mu.Lock()
		now := time.Now()
		if l.last.IsZero() {
			l.last = now
		}
		l.tokens += now.Sub(l.last).Seconds() * float64(rate)
		l.last = now
		burst := float64(rate) // at most one second of burst
		if burst < 32*1024 {
			burst = 32 * 1024
		}
		if l.tokens > burst {
			l.tokens = burst
		}
		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			l.mu.Unlock()
			return nil
		}
		need := (float64(n) - l.tokens) / float64(rate)
		l.mu.Unlock()
		d := time.Duration(need * float64(time.Second))
		if d > 500*time.Millisecond {
			d = 500 * time.Millisecond // re-check: the rate may change (day/night)
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

type limitedReader struct {
	ctx context.Context
	r   io.Reader
	l   *Limiter
	on  func(int)
}

func (lr *limitedReader) Read(p []byte) (int, error) {
	if len(p) > 16*1024 {
		p = p[:16*1024]
	}
	if err := lr.l.Wait(lr.ctx, len(p)); err != nil {
		return 0, err
	}
	n, err := lr.r.Read(p)
	if n > 0 && lr.on != nil {
		lr.on(n)
	}
	return n, err
}

// Wrap returns a reader that respects the limiter and reports progress.
func (l *Limiter) Wrap(ctx context.Context, r io.Reader, progress func(int)) io.Reader {
	return &limitedReader{ctx: ctx, r: r, l: l, on: progress}
}

// RateForTime picks the day or night limit (KB/s) for the given clock time.
func RateForTime(now time.Time, dayKBps, nightKBps int, nightStart, nightEnd string) int64 {
	if isNight(now, nightStart, nightEnd) {
		return int64(nightKBps) * 1024
	}
	return int64(dayKBps) * 1024
}

func isNight(now time.Time, start, end string) bool {
	s, err1 := time.Parse("15:04", start)
	e, err2 := time.Parse("15:04", end)
	if err1 != nil || err2 != nil {
		return false
	}
	m := now.Hour()*60 + now.Minute()
	sm := s.Hour()*60 + s.Minute()
	em := e.Hour()*60 + e.Minute()
	if sm == em {
		return false
	}
	if sm < em {
		return m >= sm && m < em
	}
	return m >= sm || m < em
}
