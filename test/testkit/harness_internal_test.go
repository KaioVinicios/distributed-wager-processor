package testkit

// Internal test: pick, target and the Client's fields are unexported, and a
// Harness with fake targets needs no infrastructure.

import (
	"net/http"
	"testing"
	"time"
)

// Covers: TST-C04 (spec M8, decisions 3 and 15) (U29)
func TestHarnessPick(t *testing.T) {
	dead, armed, up := newTarget("dead:1", "dead:2"), newTarget("armed:1", "armed:2"), newTarget("up:1", "up:2")
	dead.live.Store(false)
	armed.armed.Store(true)
	h := &Harness{targets: []*target{dead, armed, up}}
	for range 4 {
		if got, ok := h.pick(); !ok || got != up {
			t.Fatalf("pick() = %+v, %v; want the only live, disarmed instance", got, ok)
		}
	}

	even := &Harness{targets: []*target{newTarget("a:1", "a:2"), newTarget("b:1", "b:2"), newTarget("c:1", "c:2")}}
	picks := map[*target]int{}
	for range 6 {
		got, _ := even.pick()
		picks[got]++
	}
	for _, tg := range even.targets {
		if picks[tg] != 2 {
			t.Fatalf("6 picks over 3 live instances gave %d to %s, want 2", picks[tg], tg.baseURL)
		}
	}

	up.live.Store(false)
	if got, ok := h.pick(); ok {
		t.Fatalf("pick() = %+v with no live, disarmed instance; want false", got)
	}
}

// Covers: spec M8, decision 8 (U29)
func TestClientTryReportsTransportError(t *testing.T) {
	contract, err := LoadContract()
	if err != nil {
		t.Fatal(err)
	}
	closed := FreeAddr(t) // reserved and released: nothing listens there
	h := &Harness{http: &http.Client{Timeout: 2 * time.Second}, contract: contract, targets: []*target{newTarget(closed, closed)}}
	resp, err := (&Client{h: h}).Try(t, Request{Method: http.MethodGet, Path: "/health/live"})
	if err == nil || resp != nil {
		t.Fatalf("Try() = %+v, %v; want a transport error and no response", resp, err)
	}
}
