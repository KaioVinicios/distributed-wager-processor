package testkit

import (
	"context"
	"net"
	"testing"
)

// FreeAddr reserves a loopback port and releases it for the code under test.
func FreeAddr(tb testing.TB) string {
	tb.Helper()
	addr, err := freeAddr(tb.Context())
	if err != nil {
		tb.Fatal(err)
	}
	return addr
}

// freeAddr is FreeAddr for callers without a testing.TB.
func freeAddr(ctx context.Context) (string, error) {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	return addr, ln.Close()
}
