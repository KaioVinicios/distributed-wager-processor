package app

import (
	"time"

	"github.com/google/uuid"
)

// SystemClock reads the wall clock.
type SystemClock struct{}

// Now returns the current time.
func (SystemClock) Now() time.Time { return time.Now() }

// UUIDv7 generates time-ordered UUIDs.
type UUIDv7 struct{}

// New returns a canonical lowercase UUIDv7. uuid.NewV7 fails only when the
// system random source fails, which leaves nothing sensible to do.
func (UUIDv7) New() string { return uuid.Must(uuid.NewV7()).String() }
