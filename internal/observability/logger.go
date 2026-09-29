// Package observability holds logging, metrics and health aggregation.
package observability

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/KaioVinicios/pda/internal/config"
)

// NewLogger builds the process JSON logger on stdout.
func NewLogger(cfg config.Config) (*slog.Logger, error) {
	return NewJSONLogger(os.Stdout, cfg.LogLevel)
}

// NewJSONLogger builds a JSON logger writing to w at the given level.
func NewJSONLogger(w io.Writer, level string) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("observability: invalid log level %q", level)
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: lvl})), nil
}
