//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// env is this package's isolated database (test-plan §3.2).
var env *testkit.Env

func TestMain(m *testing.M) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	e, cleanup, err := testkit.NewEnv(ctx, "postgres")
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "testkit.NewEnv:", err)
		os.Exit(1)
	}
	env = e
	code := m.Run()
	if err := cleanup(); err != nil {
		fmt.Fprintln(os.Stderr, "testkit: cleanup:", err)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
