package observability_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/KaioVinicios/pda/internal/observability"
)

// Covers: FX-02
func TestNewJSONLogger_RejectsUnknownLevel(t *testing.T) {
	if _, err := observability.NewJSONLogger(&bytes.Buffer{}, "verbose"); err == nil {
		t.Fatal("NewJSONLogger(verbose) error = nil, want error")
	}
}

// Covers: OBS-01
func TestNewJSONLogger_WritesJSONAtConfiguredLevel(t *testing.T) {
	var buf bytes.Buffer
	log, err := observability.NewJSONLogger(&buf, "info")
	if err != nil {
		t.Fatalf("NewJSONLogger: %v", err)
	}
	log.Debug("hidden")
	log.Info("shown", "walletId", "w-1")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1 (debug must be suppressed): %q", len(lines), buf.String())
	}
	var entry map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &entry); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	if entry["msg"] != "shown" || entry["walletId"] != "w-1" || entry["level"] != "INFO" {
		t.Fatalf("unexpected entry: %v", entry)
	}
}
