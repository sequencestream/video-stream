// Package logging builds the structured logger shared by the daemon and CLI.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// Options controls logger construction.
type Options struct {
	// Level is one of debug, info, warn, error. Unknown values fall back to info.
	Level string
	// Format is either "json" or "text".
	Format string
}

// New builds a slog.Logger writing to w.
func New(w io.Writer, opts Options) *slog.Logger {
	handlerOpts := &slog.HandlerOptions{Level: parseLevel(opts.Level)}

	var handler slog.Handler
	if strings.EqualFold(opts.Format, "text") {
		handler = slog.NewTextHandler(w, handlerOpts)
	} else {
		handler = slog.NewJSONHandler(w, handlerOpts)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
