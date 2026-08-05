// Package logging builds the structured logger shared by the daemon and CLI.
//
// Every logger in the repository comes from New, which is what makes redaction
// tractable: installing redact.Attr here covers all logging output, so no call
// site has to remember to sanitise its attributes.
package logging

import (
	"io"
	"log/slog"
	"strings"

	"github.com/sequencestream/video-stream/internal/redact"
)

// Options controls logger construction.
type Options struct {
	// Level is one of debug, info, warn, error. Unknown values fall back to info.
	Level string
	// Format is either "json" or "text".
	Format string
}

// New builds a slog.Logger writing to w. Attributes pass through redaction
// before being formatted.
func New(w io.Writer, opts Options) *slog.Logger {
	handlerOpts := &slog.HandlerOptions{
		Level:       parseLevel(opts.Level),
		ReplaceAttr: redact.Attr,
	}

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
