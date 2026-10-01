package sqsconsumer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/KaioVinicios/pda/internal/app"
	"github.com/KaioVinicios/pda/internal/apperrors"
	"github.com/KaioVinicios/pda/internal/auth"
)

// AccessTokenAttribute is the message attribute with the provider's access
// token (messaging.md §3.2, D-23).
const AccessTokenAttribute = "accessToken"

// Authenticator verifies an access token at an instant: auth.Verifier (D-23).
type Authenticator interface {
	AuthenticateAt(ctx context.Context, raw string, at time.Time) (auth.Principal, error)
}

// Codes of the refused messages: those of the HTTP edge (lifecycle §5.3).
const (
	codeUnauthenticated  = "UNAUTHENTICATED"
	codeForbidden        = "FORBIDDEN"
	codeProviderMismatch = "PROVIDER_MISMATCH"
)

// authenticate verifies the accessToken of msg at its SentTimestamp (the
// receive instant when the broker gave none) and requires the provider role.
// A refusal is KindForbidden with its code; keys the IdP could not serve are
// KindTransient (D-23).
func authenticate(ctx context.Context, a Authenticator, msg types.Message, receivedAt time.Time) (auth.Principal, error) {
	raw := aws.ToString(msg.MessageAttributes[AccessTokenAttribute].StringValue)
	if raw == "" {
		return auth.Principal{}, refused(codeUnauthenticated, errors.New("no access token"))
	}
	p, err := a.AuthenticateAt(ctx, raw, sentAt(msg, receivedAt))
	switch {
	case errors.Is(err, auth.ErrKeysUnavailable):
		return auth.Principal{}, apperrors.New(apperrors.KindTransient, "", err)
	case err != nil:
		return auth.Principal{}, refused(codeUnauthenticated, err)
	case !auth.HasRole(p, auth.RoleProvider):
		return auth.Principal{}, refused(codeForbidden, errors.New("the token has no provider role"))
	}
	return p, nil
}

// matchProvider requires data.providerId, whenever the message carries it
// (even empty), to be the provider of the token. An absent one is left to the
// validation of the use case (MISSING_FIELD).
func matchProvider(p auth.Principal, m app.WagerMessage) error {
	if id := m.Input.ProviderID; id != nil && !auth.ActsAs(p, *id) {
		return refused(codeProviderMismatch, errors.New("data.providerId is not the provider of the token"))
	}
	return nil
}

// sentAt is the SentTimestamp of msg (epoch milliseconds), or receivedAt.
func sentAt(msg types.Message, receivedAt time.Time) time.Time {
	ms, err := strconv.ParseInt(msg.Attributes[string(types.MessageSystemAttributeNameSentTimestamp)], 10, 64)
	if err != nil {
		return receivedAt
	}
	return time.UnixMilli(ms)
}

func refused(code string, err error) error {
	return apperrors.New(apperrors.KindForbidden, code, fmt.Errorf("sqsconsumer: %w", err))
}

// countRefusal reports a message refused by the authorization to
// auth_failures_total, with the reasons of the HTTP edge (D-23).
func (c *Consumer) countRefusal(err error) {
	if apperrors.Classify(err) == apperrors.KindForbidden {
		c.metrics.AuthFailure(strings.ToLower(apperrors.CodeOf(err)))
	}
}
