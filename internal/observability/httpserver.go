package observability

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"
)

// ServeOnLifecycle binds srv to the Fx lifecycle: it listens synchronously on
// start (a busy port fails Start) and shuts down gracefully on stop.
func ServeOnLifecycle(lc fx.Lifecycle, srv *http.Server, shutdownTimeout time.Duration, log *slog.Logger, name string) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", srv.Addr)
			if err != nil {
				return fmt.Errorf("%s server: listen %s: %w", name, srv.Addr, err)
			}
			go func() {
				if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
					log.Error("http server stopped unexpectedly", "server", name, "error", err.Error())
				}
			}()
			log.Info("http server started", "server", name, "addr", ln.Addr().String())
			return nil
		},
		OnStop: func(ctx context.Context) error {
			ctx, cancel := context.WithTimeout(ctx, shutdownTimeout)
			defer cancel()
			log.Info("http server stopping", "server", name)
			if err := srv.Shutdown(ctx); err != nil {
				return fmt.Errorf("%s server: shutdown: %w", name, err)
			}
			log.Info("http server stopped", "server", name)
			return nil
		},
	})
}
