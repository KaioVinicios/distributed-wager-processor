//go:build !faultinject

package faultinject

import "testing"

// Covers: test-plan §4 ("o binário de produção não contém nenhum ponto de falha") (U27)
// Sensitivity: a hit wired to trigger in faultinject_off.go → the test binary exits with 137 at Point.
//
// Without the build tag an enabled point does nothing: were it wired, this
// test binary would exit with 137 here.
func TestPointIsNoOpWithoutTag(t *testing.T) {
	t.Setenv("PDA_FAULT", "test.point")
	Point("test.point")
}
