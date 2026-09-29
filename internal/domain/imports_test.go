package domain_test

import (
	"os/exec"
	"strings"
	"testing"
)

const domainPrefix = "github.com/KaioVinicios/pda/internal/domain"

func TestDomainHasNoInfraImports(t *testing.T) {
	// Covers: DOM-07
	// Sensitivity: importing net/http (or github.com/google/uuid) in any domain
	// package makes this test fail.
	goBin, err := exec.LookPath("go") // go test puts GOROOT/bin first in PATH
	if err != nil {
		t.Fatalf("go tool not found in PATH: %v", err)
	}
	out, err := exec.CommandContext(t.Context(), goBin, "list", "-deps",
		"-f", "{{if .Standard}}std {{end}}{{.ImportPath}}", "./...").Output()
	if err != nil {
		t.Fatalf("go list -deps: %v", err)
	}

	forbiddenStd := []string{"net/http", "database/sql"}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		path, std := strings.CutPrefix(line, "std ")
		if std {
			for _, f := range forbiddenStd {
				if path == f || strings.HasPrefix(path, f+"/") {
					t.Errorf("the domain depends on %s", path)
				}
			}
			continue
		}
		if !strings.HasPrefix(path, domainPrefix) {
			t.Errorf("the domain depends on %s: only the standard library and internal/domain are allowed", path)
		}
	}
}
