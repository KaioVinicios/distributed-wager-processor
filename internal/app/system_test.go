package app_test

import (
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/domain/ident"
)

func TestSystemClock(t *testing.T) {
	before := time.Now()
	got := app.SystemClock{}.Now()
	if got.Before(before) || got.After(time.Now()) {
		t.Fatalf("SystemClock.Now() = %v, want the wall clock", got)
	}
}

// Covers: D-08
func TestUUIDv7(t *testing.T) {
	a, b := app.UUIDv7{}.New(), app.UUIDv7{}.New()
	for _, id := range []string{a, b} {
		if !ident.Valid(id) || id[14] != '7' {
			t.Fatalf("New() = %q, want a canonical UUIDv7", id)
		}
	}
	if a == b || a > b {
		t.Fatalf("New() = %q then %q, want distinct, time-ordered ids", a, b)
	}
}
