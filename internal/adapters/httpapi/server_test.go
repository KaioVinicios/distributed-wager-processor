package httpapi_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/internal/adapters/httpapi"
)

// Covers: FX-04 (spec decision 22)
func TestNewServerTimeouts(t *testing.T) {
	srv := httpapi.NewServer(":0", http.NotFoundHandler())
	if srv.ReadHeaderTimeout != 5*time.Second || srv.ReadTimeout != 10*time.Second || srv.WriteTimeout != 30*time.Second {
		t.Fatalf("timeouts = header %v, read %v, write %v; want 5s, 10s, 30s", srv.ReadHeaderTimeout, srv.ReadTimeout, srv.WriteTimeout)
	}
}
