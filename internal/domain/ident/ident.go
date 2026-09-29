// Package ident validates the textual UUIDs used as identifiers by the domain.
// The domain depends only on the standard library, so IDs travel as canonical
// lowercase strings (D-08); the app generates them (UUIDv7).
package ident

import "errors"

// ErrInvalid reports a value that is not a canonical, non-nil UUID.
var ErrInvalid = errors.New("ident: invalid UUID")

const nilUUID = "00000000-0000-0000-0000-000000000000"

// Parse accepts the 8-4-4-4-12 hexadecimal form in any letter case and returns
// it in lowercase. The nil UUID and the braced, URN and hyphenless forms are
// rejected.
func Parse(s string) (string, error) {
	if len(s) != len(nilUUID) {
		return "", ErrInvalid
	}
	b := []byte(s)
	for i, c := range b {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return "", ErrInvalid
			}
		case '0' <= c && c <= '9', 'a' <= c && c <= 'f':
		case 'A' <= c && c <= 'F':
			b[i] = c + ('a' - 'A')
		default:
			return "", ErrInvalid
		}
	}
	if out := string(b); out != nilUUID {
		return out, nil
	}
	return "", ErrInvalid
}

// Valid reports whether s is already a canonical lowercase, non-nil UUID.
func Valid(s string) bool {
	p, err := Parse(s)
	return err == nil && p == s
}
