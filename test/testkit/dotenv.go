// Package testkit holds shared helpers for the integration and e2e tests.
package testkit

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ParseDotEnv parses KEY=VALUE lines, ignoring blanks and # comments and
// stripping one level of surrounding quotes.
func ParseDotEnv(r io.Reader) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("dotenv: line %d has no '='", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[strings.TrimSpace(k)] = v
	}
	return out, sc.Err()
}

// DotEnv loads .env.example, overridden by .env when present.
func DotEnv(tb testing.TB) map[string]string {
	tb.Helper()
	root := RepoRoot(tb)
	merged := map[string]string{}
	for i, name := range []string{".env.example", ".env"} {
		f, err := os.Open(filepath.Join(root, name))
		if errors.Is(err, os.ErrNotExist) && i > 0 {
			continue
		}
		if err != nil {
			tb.Fatalf("open %s: %v", name, err)
		}
		vals, err := ParseDotEnv(f)
		_ = f.Close()
		if err != nil {
			tb.Fatalf("parse %s: %v", name, err)
		}
		for k, v := range vals {
			merged[k] = v
		}
	}
	return merged
}
