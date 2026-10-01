package auth_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/KaioVinicios/pda/internal/auth"
	"github.com/KaioVinicios/pda/internal/config"
)

func moduleApp(t *testing.T, jwksURL string) *fxtest.App {
	t.Helper()
	cfg := config.Config{OIDCIssuer: issuer, OIDCJWKSURL: jwksURL, OIDCAudience: audience}
	return fxtest.New(t, fx.NopLogger, fx.Supply(cfg), auth.Module, fx.Invoke(func(*auth.Verifier) {}))
}

// Covers: FX-02, AUTH-02
func TestModule(t *testing.T) {
	t.Run("starts with the key set", func(t *testing.T) {
		app := moduleApp(t, newIDP(t).server.URL)
		app.RequireStart()
		app.RequireStop()
	})

	t.Run("fails to start without the key set", func(t *testing.T) {
		i := newIDP(t)
		i.status.Store(http.StatusServiceUnavailable)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		err := moduleApp(t, i.server.URL).Start(ctx)
		if err == nil || !strings.Contains(err.Error(), "JWKS") {
			t.Fatalf("Start() = %v, want a JWKS error", err)
		}
	})
}
