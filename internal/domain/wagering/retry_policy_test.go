package wagering_test

import (
	"errors"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

// noJitter makes the jitter offset zero: randN(2j+1) - j == 0.
func noJitter(n int64) int64 { return n / 2 }

func TestReferenceRetryPolicy(t *testing.T) {
	// Covers: TST-U04, OPS-12, OPS-13
	p, err := wagering.NewReferenceRetryPolicy(time.Second, time.Minute, 8, 10*time.Minute, noJitter)
	if err != nil {
		t.Fatalf("NewReferenceRetryPolicy: %v", err)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	var total time.Duration
	for n, w := range want {
		if got := p.Delay(n); got != w*time.Second {
			t.Errorf("Delay(%d) = %v, want %v", n, got, w*time.Second)
		}
		if n < 8 {
			total += p.Delay(n)
		}
	}
	if total != 183*time.Second {
		t.Errorf("8 attempts wait %v, want 183s (~3 min, D-11)", total)
	}
	if p.TTL() != 10*time.Minute {
		t.Errorf("TTL = %v", p.TTL())
	}

	low, _ := wagering.NewReferenceRetryPolicy(time.Second, time.Minute, 8, time.Hour, func(int64) int64 { return 0 })
	high, _ := wagering.NewReferenceRetryPolicy(time.Second, time.Minute, 8, time.Hour, func(n int64) int64 { return n - 1 })
	if low.Delay(3) != 6400*time.Millisecond || high.Delay(3) != 9600*time.Millisecond {
		t.Errorf("jitter bounds of 8s: %v and %v, want 6.4s and 9.6s", low.Delay(3), high.Delay(3))
	}
	random, _ := wagering.NewReferenceRetryPolicy(time.Second, time.Minute, 8, time.Hour, nil)
	for range 200 {
		if d := random.Delay(6); d < 48*time.Second || d > 72*time.Second {
			t.Fatalf("Delay(6) = %v outside 60s ± 20%%", d)
		}
	}
	huge, _ := wagering.NewReferenceRetryPolicy(time.Nanosecond, 24*time.Hour, 1, time.Hour, noJitter)
	if d := huge.Delay(1000); d != 24*time.Hour {
		t.Errorf("Delay(1000) = %v, want the 24h cap without overflow", d)
	}

	created := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	expires := created.Add(10 * time.Minute)
	exhausted := []struct {
		name     string
		attempts int
		now      time.Time
		want     bool
	}{
		{"7 attempts, before TTL", 7, created.Add(time.Minute), false},
		{"8 attempts", 8, created.Add(time.Minute), true},
		{"TTL reached", 1, expires, true},
		{"TTL passed on the first attempt", 1, expires.Add(time.Hour), true},
	}
	for _, e := range exhausted {
		if got := p.Exhausted(e.attempts, expires, e.now); got != e.want {
			t.Errorf("%s: Exhausted = %v, want %v", e.name, got, e.want)
		}
	}

	invalid := map[string][4]int64{
		"zero base":      {0, int64(time.Minute), 8, int64(time.Minute)},
		"max below base": {int64(time.Minute), int64(time.Second), 8, int64(time.Minute)},
		"max above 24h":  {int64(time.Second), int64(25 * time.Hour), 8, int64(time.Minute)},
		"zero attempts":  {int64(time.Second), int64(time.Minute), 0, int64(time.Minute)},
		"zero ttl":       {int64(time.Second), int64(time.Minute), 8, 0},
	}
	for name, v := range invalid {
		_, err := wagering.NewReferenceRetryPolicy(time.Duration(v[0]), time.Duration(v[1]), int(v[2]), time.Duration(v[3]), nil)
		if !errors.Is(err, wagering.ErrInvalidPolicy) {
			t.Errorf("%s: error = %v, want ErrInvalidPolicy", name, err)
		}
	}
}
