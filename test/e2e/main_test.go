//go:build e2e

// Package e2e_test runs the scenarios of test-plan §5.4 against 3 processes
// of the binary, each with its own pool and memory (CONC-04).
package e2e_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/KaioVinicios/pda/test/testkit"
)

// cluster is the application under test: 3 processes over this package's
// database and queues, started once (spec M8, decision 2).
var cluster *testkit.Cluster

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

// Covers: TST-C12 (the instances are built with -race, and a data race logged
// by any of them fails the package through stop)
// Sensitivity: a data race added to cmd/pda/main.go → stop reports "instance N logged a data race" and the package exits with 1.
func run(m *testing.M) (code int) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env, cleanup, err := testkit.NewEnv(ctx, "e2e")
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
	c, stop, err := env.StartCluster(ctx, 3)
	if err != nil {
		fmt.Fprintln(os.Stderr, "testkit.StartCluster:", err)
		return 1
	}
	// Deferred after the cleanup, so it runs before it: no instance may hold a
	// connection when the database is dropped (spec M8, decision 11).
	defer func() {
		if err := stop(); err != nil {
			fmt.Fprintln(os.Stderr, "testkit: stop the cluster:", err)
			if code == 0 {
				code = 1
			}
		}
	}()
	cluster = c
	return m.Run()
}
