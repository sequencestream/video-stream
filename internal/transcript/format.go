package transcript

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Format is an output serialization for a transcript.
type Format string

// The supported output formats.
const (
	// FormatJSON is vs's own schema: the only one that keeps word-level
	// timings, and therefore the only one another vs command can consume.
	FormatJSON Format = "json"
	// FormatSRT is SubRip, the format every player and platform accepts.
	FormatSRT Format = "srt"
	// FormatVTT is WebVTT, which browsers want.
	FormatVTT Format = "vtt"
	// FormatText is the words with no timing, for reading or feeding to an LLM.
	FormatText Format = "txt"
)

// ParseFormat resolves a format name.
func ParseFormat(s string) (Format, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "json":
		return FormatJSON, nil
	case "srt":
		return FormatSRT, nil
	case "vtt", "webvtt":
		return FormatVTT, nil
	case "txt", "text":
		return FormatText, nil
	default:
		return "", fmt.Errorf("unknown format %q: want json, srt, vtt or txt", s)
	}
}

// FormatForPath infers the format from a file extension, defaulting to JSON.
func FormatForPath(path string) Format {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "srt":
		return FormatSRT
	case "vtt", "webvtt":
		return FormatVTT
	case "txt", "text":
		return FormatText
	default:
		return FormatJSON
	}
}

// Ext is the conventional file extension for the format.
func (f Format) Ext() string {
	switch f {
	case FormatSRT:
		return ".srt"
	case FormatVTT:
		return ".vtt"
	case FormatText:
		return ".txt"
	default:
		return ".json"
	}
}

// Write serializes the transcript in the requested format. Subtitle formats
// re-break the transcript into readable captions using opts; JSON and text do
// not, because they are not read off a screen against a clock.
func (t Transcript) Write(w io.Writer, format Format, opts LineOptions) error {
	switch format {
	case FormatJSON:
		return t.Encode(w)
	case FormatText:
		_, err := fmt.Fprintln(w, t.Text())
		return err
	case FormatSRT:
		return WriteSRT(w, t.Subtitles(opts))
	case FormatVTT:
		return WriteVTT(w, t.Subtitles(opts))
	default:
		return fmt.Errorf("unknown format %q", format)
	}
}

// WriteTo writes the transcript to path, inferring the format from the
// extension and creating parent directories.
func (t Transcript) WriteTo(path string, opts LineOptions) error {
	return t.WriteAs(path, FormatForPath(path), opts)
}

// WriteAs writes the transcript to path in an explicit format.
func (t Transcript) WriteAs(path string, format Format, opts LineOptions) error {
	if strings.TrimSpace(path) == "" || path == "-" {
		return t.Write(os.Stdout, format, opts)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := t.Write(f, format, opts); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteSRT renders captions as SubRip.
func WriteSRT(w io.Writer, subs []Subtitle) error {
	for i, s := range subs {
		if _, err := fmt.Fprintf(w, "%d\n%s --> %s\n%s\n\n",
			i+1, srtTime(s.StartMS), srtTime(s.EndMS), s.Text()); err != nil {
			return err
		}
	}
	return nil
}

// WriteVTT renders captions as WebVTT.
func WriteVTT(w io.Writer, subs []Subtitle) error {
	if _, err := fmt.Fprint(w, "WEBVTT\n\n"); err != nil {
		return err
	}
	for _, s := range subs {
		if _, err := fmt.Fprintf(w, "%s --> %s\n%s\n\n",
			vttTime(s.StartMS), vttTime(s.EndMS), s.Text()); err != nil {
			return err
		}
	}
	return nil
}

// srtTime renders hh:mm:ss,mmm. SubRip uses a comma before the milliseconds
// and players are unforgiving about it.
func srtTime(ms int64) string { return clockTime(ms, ',') }

// vttTime renders hh:mm:ss.mmm, WebVTT's period-separated variant.
func vttTime(ms int64) string { return clockTime(ms, '.') }

func clockTime(ms int64, sep byte) string {
	if ms < 0 {
		ms = 0
	}
	h := ms / 3600000
	ms %= 3600000
	m := ms / 60000
	ms %= 60000
	s := ms / 1000
	ms %= 1000
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", h, m, s, sep, ms)
}
