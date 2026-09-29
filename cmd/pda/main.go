// Command pda runs the wallet service; `pda healthcheck` probes a running instance.
package main

import (
	"os"

	"go.uber.org/fx"

	"github.com/KaioVinicios/pda/internal/bootstrap"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(runHealthcheck())
	}
	fx.New(bootstrap.Options()...).Run()
}
