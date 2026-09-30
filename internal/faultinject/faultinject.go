// Package faultinject aborts the process at named points, to simulate a crash
// at that exact place in the e2e tests (test-plan §4, D-19). The points act
// only in a binary built with -tags faultinject and enabled by
// PDA_FAULT=<point>[,<point>]; the production binary and the Docker image are
// built without the tag, and Point is then an empty function.
package faultinject

import (
	"fmt"
	"io"
	"strings"
)

// ExitCode is the exit code of a process stopped at a fault point, the same a
// shell reports for a process killed by SIGKILL.
const ExitCode = 137

// Point stops the process here when name is enabled. It is a no-op without
// the build tag faultinject.
func Point(name string) { hit(name) }

// parse reads PDA_FAULT: point names separated by commas.
func parse(spec string) map[string]bool {
	points := map[string]bool{}
	for p := range strings.SplitSeq(spec, ",") {
		if p = strings.TrimSpace(p); p != "" {
			points[p] = true
		}
	}
	return points
}

// trigger stops the process at an enabled point: it writes "FAULT_HIT <name>",
// the line the e2e harness requires in the log, and exits with ExitCode. It
// fires at the first pass; there is no counter (spec M8, decision 14).
func trigger(points map[string]bool, name string, w io.Writer, exit func(int)) {
	if !points[name] {
		return
	}
	fmt.Fprintf(w, "FAULT_HIT %s\n", name)
	exit(ExitCode)
}
