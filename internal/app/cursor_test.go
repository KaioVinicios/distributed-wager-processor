package app

// Internal test: the cursor is opaque to clients, so its encoding is private.

import (
	"encoding/base64"
	"errors"
	"testing"

	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/domain/wagering"
)

func wantInvalidField(t *testing.T, err error, field string) {
	t.Helper()
	var ve *wagering.ValidationError
	if apperrors.Classify(err) != apperrors.KindInput || !errors.As(err, &ve) ||
		ve.Code != wagering.InputInvalidField || ve.Field != field {
		t.Fatalf("error = %v, want INVALID_FIELD on %s", err, field)
	}
}

// Covers: HTTP-03 (U14)
func TestLedgerCursor(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		for _, v := range []int64{1, 2, 50, 1 << 40} {
			c := encodeCursor(v)
			got, err := decodeCursor(c)
			if err != nil || got != v {
				t.Fatalf("decodeCursor(encodeCursor(%d) = %q) = %d, %v", v, c, got, err)
			}
		}
	})
	t.Run("opaque base64url of the version", func(t *testing.T) {
		if got := encodeCursor(2); got != "eyJ2IjoyfQ" {
			t.Fatalf("encodeCursor(2) = %q, want eyJ2IjoyfQ", got)
		}
	})
	t.Run("rejects malformed cursors", func(t *testing.T) {
		enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
		for name, c := range map[string]string{
			"not base64":        "!!!",
			"padded base64":     base64.URLEncoding.EncodeToString([]byte(`{"v":2}`)),
			"not json":          enc(`v=2`),
			"unknown field":     enc(`{"v":2,"x":1}`),
			"missing version":   enc(`{}`),
			"zero version":      enc(`{"v":0}`),
			"negative version":  enc(`{"v":-1}`),
			"fractional":        enc(`{"v":2.5}`),
			"string version":    enc(`{"v":"2"}`),
			"trailing data":     enc(`{"v":2}{}`),
			"null":              enc(`null`),
			"overflowing value": enc(`{"v":99999999999999999999}`),
		} {
			t.Run(name, func(t *testing.T) {
				_, err := decodeCursor(c)
				wantInvalidField(t, err, "cursor")
			})
		}
	})
	t.Run("limit from 1 to 200", func(t *testing.T) {
		for _, ok := range []int{1, DefaultLedgerLimit, MaxLedgerLimit} {
			if err := checkLimit(ok); err != nil {
				t.Fatalf("checkLimit(%d) = %v", ok, err)
			}
		}
		for _, bad := range []int{0, -1, MaxLedgerLimit + 1} {
			wantInvalidField(t, checkLimit(bad), "limit")
		}
	})
}
