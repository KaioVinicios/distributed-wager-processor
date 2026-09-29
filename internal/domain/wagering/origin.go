package wagering

import "fmt"

// Origin distinguishes the internal opening from provider operations (TX-05).
type Origin string

const (
	OriginInternal Origin = "INTERNAL"
	OriginExternal Origin = "EXTERNAL"
)

// ParseOrigin accepts the exact name of an origin.
func ParseOrigin(s string) (Origin, error) {
	o := Origin(s)
	if !o.Valid() {
		return "", fmt.Errorf("%w: origin", ErrInvalidEnum)
	}
	return o, nil
}

// Valid reports whether o is known. The zero value is not.
func (o Origin) Valid() bool { return o == OriginInternal || o == OriginExternal }

// ReceivedVia is the channel of an external operation; audit only, outside the
// payload hash (IDEM-03).
type ReceivedVia string

const (
	ReceivedViaHTTP ReceivedVia = "HTTP"
	ReceivedViaSQS  ReceivedVia = "SQS"
)

// ParseReceivedVia accepts the exact name of a channel.
func ParseReceivedVia(s string) (ReceivedVia, error) {
	v := ReceivedVia(s)
	if !v.Valid() {
		return "", fmt.Errorf("%w: received via", ErrInvalidEnum)
	}
	return v, nil
}

// Valid reports whether v is known. The zero value is not.
func (v ReceivedVia) Valid() bool { return v == ReceivedViaHTTP || v == ReceivedViaSQS }
