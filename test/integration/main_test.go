//go:build integration

package integration_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// server is the application under test: in process, over this package's
// database and queues, with the real Keycloak (spec decision 21).
var server *testkit.App

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) (code int) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env, cleanup, err := testkit.NewEnv(ctx, "integration")
	if err != nil {
		fmt.Fprintln(os.Stderr, "testkit.NewEnv:", err)
		return 1
	}
	defer func() {
		if err := cleanup(); err != nil {
			fmt.Fprintln(os.Stderr, "testkit: cleanup:", err)
			if code == 0 {
				code = 1
			}
		}
	}()
	app, stop, err := env.StartApp(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testkit.StartApp:", err)
		return 1
	}
	defer stop()
	server = app
	return m.Run()
}
