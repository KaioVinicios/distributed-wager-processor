package testkit

import "testing"

// Covers: D-23 (spec of 01/10, decision 9: the token every test message carries)
func TestTokenFor(t *testing.T) {
	tokenOf := func(clientID string) string { return "token-of-" + clientID }
	const bodyB = `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-10-01T12:00:00Z","data":{"providerId":"provider-b"}}`
	cases := []struct {
		name, body string
		o          SendOpts
		want       string
	}{
		{"the provider named in the body", bodyB, SendOpts{}, "token-of-provider-b"},
		{"provider-a when the body names none", `{"messageId":"m","data":{"walletId":"w"}}`, SendOpts{}, "token-of-provider-a"},
		{"provider-a for a malformed body", `{"messageId":`, SendOpts{}, "token-of-provider-a"},
		{"an explicit token", bodyB, SendOpts{Token: "forged"}, "forged"},
		{"no token", bodyB, SendOpts{NoToken: true}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tokenFor(tc.body, tc.o, tokenOf); got != tc.want {
				t.Fatalf("tokenFor = %q, want %q", got, tc.want)
			}
		})
	}
}
