package httpapi

import (
	"net/http"

	"github.com/KaioVinicios/pda/internal/auth"
)

var (
	internalOnly = []auth.Role{auth.RoleWalletInternal}
	providerOnly = []auth.Role{auth.RoleProvider}
	anyCaller    = []auth.Role{auth.RoleProvider, auth.RoleWalletInternal}
)

// route is one line of the route table: the single list that registers the
// ServeMux and that I15 compares with api/openapi.yaml.
type route struct {
	method, path string
	roles        []auth.Role // nil = public
	jsonBody     bool
	handle       http.HandlerFunc
}

func routes(opts Options, s Services) []route {
	hh := healthHandler{health: s.Health}
	h := handlers{s: s, log: opts.Log, metrics: opts.Metrics}
	rs := []route{
		{http.MethodGet, "/health/live", nil, false, hh.live},
		{http.MethodGet, "/health/ready", nil, false, hh.ready},
		{http.MethodPost, "/wallets", internalOnly, true, h.openWallet},
		{http.MethodGet, "/wallets/{walletId}", internalOnly, false, h.getWallet},
		{http.MethodGet, "/wallets/{walletId}/ledger", internalOnly, false, h.listLedger},
		{http.MethodPost, "/wallets/{walletId}/reconciliation", internalOnly, false, h.reconcile},
		{http.MethodPost, "/wagering/transactions", providerOnly, true, h.submitWager},
		{http.MethodGet, "/wagering/transactions/{transactionId}", anyCaller, false, h.getTransaction},
		{http.MethodGet, "/providers/{providerId}/wagering/transactions/{externalTransactionId}", anyCaller, false, h.getTransactionByExternalID},
	}
	if opts.DocsEnabled {
		rs = append(rs,
			route{http.MethodGet, "/openapi.yaml", nil, false, openAPI},
			route{http.MethodGet, "/docs", nil, false, swaggerUI},
		)
	}
	return rs
}

// New returns the API handler: the routes behind the middlewares, from the
// outside in: correlation, access log, panic recovery and the problem+json
// fallback for unmatched routes; then, per route, authentication and roles,
// and the JSON body checks.
func New(opts Options, s Services) http.Handler {
	if opts.Metrics == nil {
		opts.Metrics = nopMetrics{}
	}
	mux := http.NewServeMux()
	for _, rt := range routes(opts, s) {
		var h http.Handler = rt.handle
		if rt.jsonBody {
			h = jsonBody(h)
		}
		if rt.roles != nil {
			h = authenticate(s.Auth, opts.Log, opts.Metrics, rt.roles, h)
		}
		mux.Handle(rt.method+" "+rt.path, named(rt.method+" "+rt.path, h))
	}
	return withCorrelation(logAccess(opts.Log, opts.Metrics, recoverPanic(opts.Log, routeFallback(mux))))
}

// named records the matched route for the access log.
func named(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		infoOf(r.Context()).route = pattern
		next.ServeHTTP(w, r)
	})
}

// Routes lists the "METHOD /path" patterns New registers (I15).
func Routes(docsEnabled bool) []string {
	var out []string
	for _, rt := range routes(Options{DocsEnabled: docsEnabled}, Services{}) {
		out = append(out, rt.method+" "+rt.path)
	}
	return out
}
