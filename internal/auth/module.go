package auth

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/config"
)

// jwksTimeout bounds each fetch of the key set.
const jwksTimeout = 5 * time.Second

// Module provides the Verifier. The key set is fetched on start, so an
// unreachable IdP fails the start (FX-02, spec decision 15); the HTTP client
// is the module's own, so its idle connections close on stop (goleak, I07b).
var Module = fx.Module("auth", fx.Provide(newModuleVerifier))

func newModuleVerifier(lc fx.Lifecycle, cfg config.Config) *Verifier {
	client := &http.Client{Timeout: jwksTimeout, Transport: &http.Transport{MaxIdleConnsPerHost: 2, IdleConnTimeout: time.Minute}}
	v := NewVerifier(cfg, client)
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, jwksTimeout)
			defer cancel()
			return v.CheckKeys(ctx)
		},
		OnStop: func(context.Context) error {
			client.CloseIdleConnections()
			return nil
		},
	})
	return v
}
