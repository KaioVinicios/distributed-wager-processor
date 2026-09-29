package money_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// floatUses lists every float32/float64 identifier, ParseFloat/FormatFloat
// reference and floating-point literal in the non-test Go files of dir. The
// literal check covers what forbidigo cannot see (x := 1.5), see
// docs/dev/spike-lint.md.
func floatUses(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var uses []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Ident:
				switch n.Name {
				case "float32", "float64", "ParseFloat", "FormatFloat":
					uses = append(uses, fset.Position(n.Pos()).String()+": "+n.Name)
				}
			case *ast.BasicLit:
				if n.Kind == token.FLOAT {
					uses = append(uses, fset.Position(n.Pos()).String()+": "+n.Value)
				}
			}
			return true
		})
	}
	return uses
}

func TestNoFloatInMoney(t *testing.T) {
	// Covers: TST-U01, MON-01, E3
	// Sensitivity: adding `var _ = 1.5` to money.go makes the second subtest fail.
	t.Run("detector finds every kind of use", func(t *testing.T) {
		dir := t.TempDir()
		src := "package x\n\nimport \"strconv\"\n\nvar a float64\nvar b = 1.5\nvar c, _ = strconv.ParseFloat(\"1\", 64)\n"
		if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := floatUses(t, dir); len(got) != 3 {
			t.Fatalf("detector found %v, want 3 uses", got)
		}
	})
	t.Run("money package has none", func(t *testing.T) {
		if got := floatUses(t, "."); len(got) != 0 {
			t.Fatalf("floating point in package money: %v", got)
		}
	})
}
