// Package ffmpeg wraps the ffmpeg and ffprobe binaries.
//
// vs does not reimplement anything ffmpeg already does. Everything here builds
// an argument list and runs it; the value vs adds is that you do not have to
// remember that trimming without re-encoding lands on the previous keyframe,
// that concat needs a script file once the filter graph gets long, or that
// burning subtitles means escaping a path twice for the filter parser.
//
// Two rules hold throughout:
//
//   - An output file appears complete or not at all. Every run writes to a
//     temporary file beside the destination and renames it on success, so an
//     interrupted encode never leaves a half-written MP4 that looks finished.
//   - A failure reports what ffmpeg said. The exit status alone is useless;
//     the last lines of stderr are the actual diagnosis.
package ffmpeg

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Tool locates the binaries and carries the run-wide switches.
type Tool struct {
	// FFmpeg and FFprobe may be bare names resolved through PATH or absolute
	// paths. Empty falls back to "ffmpeg" and "ffprobe".
	FFmpeg  string
	FFprobe string
	// Verbose streams ffmpeg's own progress output to stderr instead of
	// capturing it. It is what -v turns on.
	Verbose bool
	// Overwrite allows replacing an existing output file.
	Overwrite bool
	// DryRun prints the command that would run and skips it.
	DryRun bool
	// Stderr receives verbose and dry-run output. Nil means os.Stderr.
	Stderr *os.File
}

// ErrOutputExists is returned when the destination is already there and
// Overwrite is not set. Silently replacing a file the user spent an hour
// rendering is not a reasonable default.
var ErrOutputExists = errors.New("output file already exists (pass -f to overwrite)")

func (t Tool) ffmpegBin() string {
	if s := strings.TrimSpace(t.FFmpeg); s != "" {
		return s
	}
	return "ffmpeg"
}

func (t Tool) ffprobeBin() string {
	if s := strings.TrimSpace(t.FFprobe); s != "" {
		return s
	}
	return "ffprobe"
}

func (t Tool) errWriter() *os.File {
	if t.Stderr != nil {
		return t.Stderr
	}
	return os.Stderr
}

// Check reports whether the configured binaries are runnable, naming the one
// that is missing. Every command calls this before doing work: "ffmpeg not
// found in PATH" is a far better first line than a failure forty seconds in.
func (t Tool) Check() error {
	if _, err := exec.LookPath(t.ffmpegBin()); err != nil {
		return fmt.Errorf("ffmpeg not found (%s): install it, or set tools.ffmpeg in the config", t.ffmpegBin())
	}
	if _, err := exec.LookPath(t.ffprobeBin()); err != nil {
		return fmt.Errorf("ffprobe not found (%s): it ships with ffmpeg; set tools.ffprobe if it lives elsewhere", t.ffprobeBin())
	}
	return nil
}

// HasFilter reports whether this ffmpeg build provides the named filter.
//
// Builds vary in what they include, and the failure mode when one is missing is
// a filter-graph parse error twenty lines long that never mentions the real
// cause. Asking first turns that into one sentence naming the missing piece.
func (t Tool) HasFilter(ctx context.Context, name string) bool {
	cmd := exec.CommandContext(ctx, t.ffmpegBin(), "-hide_banner", "-h", "filter="+name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	// ffmpeg reports an absent filter on stdout and still exits zero, so the
	// exit status alone cannot answer this.
	return !strings.Contains(string(out), "Unknown filter")
}

// Run invokes ffmpeg with the given arguments and no output-file handling.
func (t Tool) Run(ctx context.Context, args ...string) error {
	return t.run(ctx, t.ffmpegBin(), args)
}

// RunAtomic invokes ffmpeg with an argument list built around a temporary
// output path, then publishes that file to output on success.
//
// build receives the temporary path and must place it last, in ffmpeg's output
// position. The temporary file lives in the destination directory so the
// rename stays on one filesystem and therefore stays atomic.
func (t Tool) RunAtomic(ctx context.Context, output string, build func(tmp string) []string) error {
	if strings.TrimSpace(output) == "" {
		return errors.New("output path is required")
	}
	if !t.Overwrite && !t.DryRun {
		if _, err := os.Stat(output); err == nil {
			return fmt.Errorf("%s: %w", output, ErrOutputExists)
		}
	}
	dir := filepath.Dir(output)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	// Keep the destination's extension: ffmpeg picks the muxer from it.
	tmp, err := os.CreateTemp(dir, ".vs-*"+filepath.Ext(output))
	if err != nil {
		return fmt.Errorf("create temporary output: %w", err)
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	defer os.Remove(tmpPath)

	args := build(tmpPath)
	if t.DryRun {
		// Show the real destination, not the scratch file: the point of a dry
		// run is a command the user can read, and paste.
		fmt.Fprintln(t.errWriter(), formatCommand(t.ffmpegBin(), replaceLast(args, tmpPath, output)))
		return nil
	}
	if err := t.run(ctx, t.ffmpegBin(), args); err != nil {
		return err
	}

	info, err := os.Stat(tmpPath)
	if err != nil {
		return fmt.Errorf("stat ffmpeg output: %w", err)
	}
	if info.Size() == 0 {
		return errors.New("ffmpeg produced an empty file")
	}
	if err := os.Rename(tmpPath, output); err != nil {
		return fmt.Errorf("publish output: %w", err)
	}
	return nil
}

// run executes a binary, turning a non-zero exit into an error carrying the
// tail of stderr.
func (t Tool) run(ctx context.Context, binary string, args []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.DryRun {
		fmt.Fprintln(t.errWriter(), formatCommand(binary, args))
		return nil
	}

	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	if t.Verbose {
		cmd.Stderr = t.errWriter()
	} else {
		cmd.Stderr = &stderr
	}

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("%s interrupted: %w", filepath.Base(binary), ctxErr)
		}
		if detail := tail(stderr.String(), 4096); detail != "" {
			return fmt.Errorf("%s failed: %w\n%s", filepath.Base(binary), err, detail)
		}
		return fmt.Errorf("%s failed: %w", filepath.Base(binary), err)
	}
	return nil
}

// BaseArgs are the flags every ffmpeg invocation carries: no banner, and
// errors only unless the user asked to watch.
func (t Tool) BaseArgs() []string {
	level := "error"
	if t.Verbose {
		level = "info"
	}
	return []string{"-hide_banner", "-loglevel", level, "-nostdin", "-y"}
}

// tail returns at most n trailing bytes, cut at a line boundary.
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}

func replaceLast(args []string, from, to string) []string {
	out := append([]string(nil), args...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i] == from {
			out[i] = to
			break
		}
	}
	return out
}

// formatCommand renders a command line that can be pasted into a shell.
func formatCommand(binary string, args []string) string {
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, shellQuote(binary))
	for _, a := range args {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n\"'\\$`*?[]{}()<>|&;#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
