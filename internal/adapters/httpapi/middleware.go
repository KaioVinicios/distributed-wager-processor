package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"runtime/debug"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/KaioVinicios/pda/internal/auth"
)

// maxBodyBytes bounds a request body (lifecycle §3.1).
const maxBodyBytes = 64 << 10

type ctxKey int

const (
	correlationKey ctxKey = iota
	requestInfoKey
)

// correlationPattern is what a client may send as X-Correlation-Id (D-18).
var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

// withCorrelation keeps a valid X-Correlation-Id or generates a UUIDv7, and
// returns it in the response header.
func withCorrelation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-Id")
		if !correlationPattern.MatchString(id) {
			id = uuid.Must(uuid.NewV7()).String()
		}
		w.Header().Set("X-Correlation-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationKey, id)))
	})
}

// correlationID returns the correlation id of the request.
func correlationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationKey).(string)
	return id
}

// requestInfo is filled while the request goes down the chain and read by
// the access log on the way back.
type requestInfo struct {
	route      string
	providerID string
}

func infoOf(ctx context.Context) *requestInfo {
	info, _ := ctx.Value(requestInfoKey).(*requestInfo)
	if info == nil {
		return &requestInfo{}
	}
	return info
}

// statusRecorder remembers the status written by the handler.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	return s.ResponseWriter.Write(b)
}

// unmatchedRoute labels what no route pattern matched, keeping the metric's
// cardinality fixed (spec M7, decision 8).
const unmatchedRoute = "unmatched"

// logAccess writes one line per request with a fixed set of fields: never a
// header or a body (OBS-02), and reports the request to the metrics.
func logAccess(log *slog.Logger, m Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		info := &requestInfo{}
		rec := &statusRecorder{ResponseWriter: w}
		ctx := context.WithValue(r.Context(), requestInfoKey, info)
		next.ServeHTTP(rec, r.WithContext(ctx))
		route := info.route
		if route == "" {
			route = unmatchedRoute
		}
		elapsed := time.Since(start)
		m.HTTPRequest(route, r.Method, rec.status, elapsed)
		log.InfoContext(ctx, "http request",
			"method", r.Method, "route", route, "status", rec.status,
			"durationMs", elapsed.Milliseconds(),
			"correlationId", correlationID(ctx), "providerId", info.providerID)
	})
}

// recoverPanic turns a panic, always a bug, into 500 INTERNAL_ERROR: nothing
// was recorded, since the unit of work rolls back on panic.
func recoverPanic(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer answerPanic(log, w, r)
		next.ServeHTTP(w, r)
	})
}

// answerPanic is deferred by recoverPanic, so recover sees the panic.
// http.ErrAbortHandler keeps its meaning: abort without an answer.
func answerPanic(log *slog.Logger, w http.ResponseWriter, r *http.Request) {
	v := recover()
	if v == nil {
		return
	}
	if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(v)
	}
	ctx := r.Context()
	log.ErrorContext(ctx, "panic serving the request",
		"correlationId", correlationID(ctx), "panic", fmt.Sprint(v), "stack", string(debug.Stack()))
	writeProblem(w, r, codeInternalError, "")
}

// discard remembers the status and headers a handler writes, without the body.
type discard struct {
	header http.Header
	status int
}

func (d *discard) Header() http.Header { return d.header }

func (d *discard) Write(b []byte) (int, error) {
	if d.status == 0 {
		d.status = http.StatusOK
	}
	return len(b), nil
}

func (d *discard) WriteHeader(code int) {
	if d.status == 0 {
		d.status = code
	}
}

// routeFallback answers 404 ROUTE_NOT_FOUND and 405 METHOD_NOT_ALLOWED as
// problem+json. The ServeMux decides (its own 404 and 405, with Allow); the
// fallback only rewrites the body. Its redirects pass through.
func routeFallback(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		d := &discard{header: http.Header{}}
		h.ServeHTTP(d, r)
		switch d.status {
		case http.StatusNotFound:
			writeProblem(w, r, codeRouteNotFound, "")
		case http.StatusMethodNotAllowed:
			w.Header().Set("Allow", d.header.Get("Allow"))
			writeProblem(w, r, codeMethodNotAllowed, "")
		default:
			mux.ServeHTTP(w, r)
		}
	})
}

// authenticate requires a bearer token of one of roles and puts the
// principal in the context (D-07). Authorization runs before any read or
// write (AUTH-07).
func authenticate(a Authenticator, log *slog.Logger, m Metrics, roles []auth.Role, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		raw, ok := bearerToken(r)
		if !ok {
			m.AuthFailure("unauthenticated")
			unauthenticated(w, r, false)
			return
		}
		p, err := a.Authenticate(ctx, raw)
		if err != nil {
			m.AuthFailure("unauthenticated")
			log.DebugContext(ctx, "bearer token rejected", "correlationId", correlationID(ctx), "reason", err.Error())
			unauthenticated(w, r, true)
			return
		}
		infoOf(ctx).providerID = p.ProviderID
		if !auth.HasAnyRole(p, roles...) {
			m.AuthFailure("forbidden")
			writeProblem(w, r, codeForbidden, "")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(ctx, p)))
	})
}

// bearerToken returns the token of "Authorization: Bearer <token>"; ok is
// false when there is no bearer credential at all.
func bearerToken(r *http.Request) (token string, ok bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// unauthenticated answers 401 with the challenge of RFC 6750. The answer never
// says whether the token was malformed, forged or expired.
func unauthenticated(w http.ResponseWriter, r *http.Request, invalidToken bool) {
	challenge := `Bearer realm="pda"`
	if invalidToken {
		challenge += `, error="invalid_token"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeProblem(w, r, codeUnauthenticated, "")
}

// jsonBody requires Content-Type application/json and bounds the body.
func jsonBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeProblem(w, r, codeUnsupportedMediaType, "")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		next.ServeHTTP(w, r)
	})
}
