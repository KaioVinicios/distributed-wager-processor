//go:build !faultinject

package faultinject

// hit does nothing: the binary carries no fault point (test-plan §4).
func hit(string) {}
