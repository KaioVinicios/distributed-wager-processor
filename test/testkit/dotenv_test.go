package testkit_test

import (
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/test/testkit"
)

func TestParseDotEnv(t *testing.T) {
	in := "# comment\n\nA=1\nB=\"quoted value\"\nC='single'\nURL=postgres://u:p@h:5432/db?sslmode=disable\n  D = spaced \n"
	got, err := testkit.ParseDotEnv(strings.NewReader(in))
	if err != nil {
		t.Fatalf("ParseDotEnv() error = %v", err)
	}
	want := map[string]string{"A": "1", "B": "quoted value", "C": "single", "URL": "postgres://u:p@h:5432/db?sslmode=disable", "D": "spaced"}
	if len(got) != len(want) {
		t.Fatalf("ParseDotEnv() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestParseDotEnv_RejectsLineWithoutEquals(t *testing.T) {
	if _, err := testkit.ParseDotEnv(strings.NewReader("JUSTAKEY\n")); err == nil {
		t.Fatal("ParseDotEnv() error = nil, want error")
	}
}
