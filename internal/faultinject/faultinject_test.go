package faultinject

// Internal test: parse and trigger are the logic faultinject_on.go wires to
// os.Getenv, os.Stderr and os.Exit. The wiring itself is proven by the e2e
// crash tests, which require FAULT_HIT and the exit code 137 from a real
// process (test-plan §4).

import (
	"bytes"
	"maps"
	"testing"
)

// Covers: D-19, test-plan §4 (U27)
func TestParse(t *testing.T) {
	got := parse(" consumer.before_commit ,outbox.after_publish_before_ack,,")
	want := map[string]bool{"consumer.before_commit": true, "outbox.after_publish_before_ack": true}
	if !maps.Equal(got, want) {
		t.Fatalf("parse() = %v, want %v", got, want)
	}
	if got := parse(""); len(got) != 0 {
		t.Fatalf(`parse("") = %v, want no point`, got)
	}
}

// Covers: D-19, test-plan §4 (U27)
func TestTrigger(t *testing.T) {
	points := parse("http.after_commit_before_response")
	var out bytes.Buffer
	code := -1
	exit := func(c int) { code = c }

	trigger(points, "outbox.after_claim_before_publish", &out, exit)
	if code != -1 || out.Len() != 0 {
		t.Fatalf("a point not enabled: exit %d, output %q; want neither", code, out.String())
	}
	trigger(points, "http.after_commit_before_response", &out, exit)
	if code != ExitCode || out.String() != "FAULT_HIT http.after_commit_before_response\n" {
		t.Fatalf("an enabled point: exit %d, output %q; want %d and the FAULT_HIT line", code, out.String(), ExitCode)
	}
}
