package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
)

// Ledger page sizes (D-16, HTTP-03).
const (
	DefaultLedgerLimit = 50
	MaxLedgerLimit     = 200
)

// encodeCursor returns the opaque cursor that resumes the ledger after
// version: base64url, without padding, of {"v":version}.
func encodeCursor(version int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(`{"v":` + strconv.FormatInt(version, 10) + `}`))
}

// decodeCursor returns the version a cursor resumes after. Anything but an
// encodeCursor result is 400 INVALID_FIELD on "cursor": no silent fallback to
// the first page.
func decodeCursor(s string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, invalidField("cursor")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var c struct {
		V *int64 `json:"v"`
	}
	if err := dec.Decode(&c); err != nil || c.V == nil || *c.V < 1 {
		return 0, invalidField("cursor")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return 0, invalidField("cursor")
	}
	return *c.V, nil
}

// checkLimit accepts a page size from 1 to MaxLedgerLimit; there is no silent
// clamp (spec decision 9).
func checkLimit(limit int) error {
	if limit < 1 || limit > MaxLedgerLimit {
		return invalidField("limit")
	}
	return nil
}
