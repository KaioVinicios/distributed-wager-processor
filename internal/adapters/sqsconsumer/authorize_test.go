package sqsconsumer

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/adapters/awsclient"
	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/auth"
)

// stubAuth answers AuthenticateAt with p and err and records the calls.
type stubAuth struct {
	p     auth.Principal
	err   error
	calls int
	at    time.Time
}

func (s *stubAuth) AuthenticateAt(_ context.Context, _ string, at time.Time) (auth.Principal, error) {
	s.calls++
	s.at = at
	return s.p, s.err
}

// recordingProcessor counts the messages that reach the use case.
type recordingProcessor struct{ calls atomic.Int32 }

func (r *recordingProcessor) Execute(context.Context, app.WagerMessage) (app.ConsumeResult, error) {
	r.calls.Add(1)
	return app.ConsumeResult{Duplicate: true}, nil
}

// Covers: AUTH-04, AUTH-07, AUTH-09, SQS-07, D-23 (U35)
func TestAuthorizeMessage(t *testing.T) {
	sent := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	received := sent.Add(10 * time.Minute)
	providerA := auth.Principal{ProviderID: "provider-a", Roles: []auth.Role{auth.RoleProvider}}
	providerB := auth.Principal{ProviderID: "provider-b", Roles: []auth.Role{auth.RoleProvider}}
	internal := auth.Principal{ClientID: "wallet-service", Roles: []auth.Role{auth.RoleWalletInternal}}
	unnamed := auth.Principal{Roles: []auth.Role{auth.RoleProvider}} // the provider role without provider_id

	withoutProvider := strings.Replace(validBody, `"providerId":"provider-a",`, "", 1)
	emptyProvider := strings.Replace(validBody, `"providerId":"provider-a"`, `"providerId":""`, 1)
	invalidAmount := strings.Replace(validBody, `"amount":"25.00"`, `"amount":"1"`, 1)
	token, empty := "a-token", ""

	type want struct {
		kind           actionKind
		code, category string
		processed      bool   // the use case was called
		reason         string // the auth_failures_total reason, empty for none
	}
	refused := func(code string) want {
		return want{kind: actDLQ, code: code, category: "CORRECTABLE", reason: strings.ToLower(code)}
	}
	concluded := want{kind: actDelete, processed: true}
	cases := []struct {
		name  string
		body  string
		token *string // nil = no accessToken attribute
		auth  *stubAuth
		want  want
	}{
		{"no accessToken", validBody, nil, &stubAuth{p: providerA}, refused("UNAUTHENTICATED")},
		{"an empty accessToken", validBody, &empty, &stubAuth{p: providerA}, refused("UNAUTHENTICATED")},
		{"a token the IdP refuses", validBody, &token, &stubAuth{err: fmt.Errorf("%w: forged", auth.ErrUnauthenticated)}, refused("UNAUTHENTICATED")},
		{"the IdP keys unavailable", validBody, &token, &stubAuth{err: fmt.Errorf("%w: 503", auth.ErrKeysUnavailable)}, want{kind: actRetry}},
		{"the internal service", validBody, &token, &stubAuth{p: internal}, refused("FORBIDDEN")},
		{"a provider without provider_id", validBody, &token, &stubAuth{p: unnamed}, refused("FORBIDDEN")},
		{"another provider", validBody, &token, &stubAuth{p: providerB}, refused("PROVIDER_MISMATCH")},
		{"an empty providerId", emptyProvider, &token, &stubAuth{p: providerA}, refused("PROVIDER_MISMATCH")},
		{"another provider and an invalid amount", invalidAmount, &token, &stubAuth{p: providerB}, refused("PROVIDER_MISMATCH")},
		{"no providerId: the use case decides", withoutProvider, &token, &stubAuth{p: providerA}, concluded},
		{"the provider of the token", validBody, &token, &stubAuth{p: providerA}, concluded},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := types.Message{
				MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(tc.body),
				Attributes: map[string]string{"MessageGroupId": "g", "SentTimestamp": strconv.FormatInt(sent.UnixMilli(), 10)},
			}
			if tc.token != nil {
				msg.MessageAttributes = map[string]types.MessageAttributeValue{
					AccessTokenAttribute: {DataType: aws.String("String"), StringValue: aws.String(*tc.token)},
				}
			}
			m, proc := &countingMetrics{}, &recordingProcessor{}
			c := NewConsumer(&fakeQueue{}, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, proc, tc.auth,
				upPinger{}, m, slog.New(slog.DiscardHandler), unitOptions(0))
			a := c.handle(t.Context(), msg, received)
			if a.kind != tc.want.kind || a.code != tc.want.code || a.category != tc.want.category {
				t.Fatalf("action = %+v, want %+v", a, tc.want)
			}
			if got := proc.calls.Load() > 0; got != tc.want.processed {
				t.Fatalf("use case called = %v, want %v", got, tc.want.processed)
			}
			var wantReasons []string
			if tc.want.reason != "" {
				wantReasons = []string{tc.want.reason}
			}
			if got := m.reasons(); !slices.Equal(got, wantReasons) {
				t.Fatalf("auth failures = %v, want %v", got, wantReasons)
			}
			if tc.token == nil || *tc.token == "" {
				if tc.auth.calls != 0 {
					t.Fatalf("the IdP was asked %d times about a message without a token", tc.auth.calls)
				}
			}
		})
	}

	instants := map[string]struct {
		sentTimestamp string // "" = no SentTimestamp
		want          time.Time
	}{
		"the SentTimestamp of the broker":               {strconv.FormatInt(sent.UnixMilli(), 10), sent},
		"no SentTimestamp: the receive time":            {"", received},
		"an unreadable SentTimestamp: the receive time": {"yesterday", received},
	}
	for name, tc := range instants {
		t.Run("the instant of the token is "+name, func(t *testing.T) {
			msg := types.Message{
				MessageId: aws.String("sqs-1"), ReceiptHandle: aws.String("rh-1"), Body: aws.String(validBody),
				Attributes: map[string]string{"MessageGroupId": "g"},
				MessageAttributes: map[string]types.MessageAttributeValue{
					AccessTokenAttribute: {DataType: aws.String("String"), StringValue: aws.String(token)},
				},
			}
			if tc.sentTimestamp != "" {
				msg.Attributes["SentTimestamp"] = tc.sentTimestamp
			}
			a := &stubAuth{p: providerA}
			c := NewConsumer(&fakeQueue{}, &awsclient.Queues{WagerURL: "wager", DLQURL: "dlq"}, &recordingProcessor{}, a,
				upPinger{}, &countingMetrics{}, slog.New(slog.DiscardHandler), unitOptions(0))
			c.handle(t.Context(), msg, received)
			if !a.at.Equal(tc.want) {
				t.Fatalf("the token was checked at %s, want %s", a.at, tc.want)
			}
		})
	}
}
