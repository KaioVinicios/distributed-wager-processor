package testkit

import (
	"net/url"
	"testing"
)

// AppDatabaseURL is the pda database as pda_app, seen from the host.
// M2 replaces it with isolated per-package databases (test-plan §3.2).
func AppDatabaseURL(tb testing.TB) string {
	tb.Helper()
	u := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword("pda_app", DotEnv(tb)["PDA_APP_PASSWORD"]),
		Host:     "localhost:5432",
		Path:     "/pda",
		RawQuery: "sslmode=disable",
	}
	return u.String()
}
