package app

import "github.com/KaioVinicios/pda/internal/domain/events"

// sealEvents wraps the events the domain returned in envelopes with fresh
// UUIDv7 event ids, ready for the outbox (D-13).
func sealEvents(ids IDGenerator, evs []events.Event, correlationID, causationID string) ([]events.Envelope, error) {
	out := make([]events.Envelope, 0, len(evs))
	for _, e := range evs {
		env, err := events.Seal(ids.New(), correlationID, causationID, e)
		if err != nil {
			return nil, domainError(err)
		}
		out = append(out, env)
	}
	return out, nil
}
