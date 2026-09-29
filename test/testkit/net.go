package testkit

import (
	"context"
	"net"
	"testing"
)

// FreeAddr reserves a loopback port and releases it for the code under test.
func FreeAddr(tb testing.TB) string {
	tb.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		tb.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		tb.Fatalf("close: %v", err)
	}
	return addr
}
