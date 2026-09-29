package ident_test

import (
	"errors"
	"testing"

	"github.com/KaioVinicios/pda/internal/domain/ident"
)

func TestParse(t *testing.T) {
	// Covers: DOM-03, IDEM-03
	const canonical = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"canonical", canonical, canonical},
		{"uppercase is normalized", "0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1", canonical},
		{"empty", "", ""},
		{"nil UUID", "00000000-0000-0000-0000-000000000000", ""},
		{"braces", "{" + canonical + "}", ""},
		{"urn", "urn:uuid:" + canonical, ""},
		{"no hyphens", "0192f28f5dc07d58bdb2814ad6a0f4a1", ""},
		{"too short", canonical[:35], ""},
		{"non hex", "0192f28g-5dc0-7d58-bdb2-814ad6a0f4a1", ""},
		{"misplaced hyphen", "0192f28f5-dc0-7d58-bdb2-814ad6a0f4a1", ""},
		{"surrounding space", " " + canonical[1:], ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ident.Parse(tc.in)
			if tc.want == "" {
				if !errors.Is(err, ident.ErrInvalid) {
					t.Fatalf("Parse(%q) error = %v, want ErrInvalid", tc.in, err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("Parse(%q) = %q, %v; want %q, nil", tc.in, got, err, tc.want)
			}
		})
	}
}

func TestValid(t *testing.T) {
	// Covers: DOM-03
	tests := map[string]bool{
		"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1": true,
		"0192F28F-5DC0-7D58-BDB2-814AD6A0F4A1": false, // valid UUID, but not canonical
		"00000000-0000-0000-0000-000000000000": false,
		"":                                     false,
	}
	for in, want := range tests {
		if got := ident.Valid(in); got != want {
			t.Errorf("Valid(%q) = %v, want %v", in, got, want)
		}
	}
}
