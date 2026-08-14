package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// testFlagSet mirrors the shapes the real commands register: a bool switch, a
// string option, and a repeatable range.
func testFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("aggressive", false, "")
	fs.Bool("f", false, "")
	fs.String("o", "", "")
	fs.String("transcript", "", "")
	var list stringList
	fs.Var(&list, "keep", "")
	return fs
}

// Go's flag package stops parsing at the first positional argument, so without
// permutation `vs filler talk.mp4 -aggressive` would silently ignore the flag.
// Silently ignoring an editing option is the worst outcome available.
func TestPermuteAllowsFlagsAfterFilenames(t *testing.T) {
	ordered, positional := permute(testFlagSet(),
		[]string{"talk.mp4", "-aggressive", "-o", "clean.mp4", "second.mp4"})

	if len(positional) != 2 || positional[0] != "talk.mp4" || positional[1] != "second.mp4" {
		t.Fatalf("positional = %v want both input files", positional)
	}

	fs := testFlagSet()
	if err := fs.Parse(ordered); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if fs.Lookup("aggressive").Value.String() != "true" {
		t.Fatal("-aggressive after a filename was dropped")
	}
	if got := fs.Lookup("o").Value.String(); got != "clean.mp4" {
		t.Fatalf("-o = %q want clean.mp4", got)
	}
	if fs.NArg() != 2 {
		t.Fatalf("NArg = %d want 2", fs.NArg())
	}
}

// A value that begins with a dash belongs to the flag before it: `-keep -0:30`
// means the first thirty seconds, not a flag named -0:30.
func TestPermuteKeepsDashLeadingValuesWithTheirFlag(t *testing.T) {
	ordered, positional := permute(testFlagSet(), []string{"-keep", "-0:30", "in.mp4"})

	if len(positional) != 1 || positional[0] != "in.mp4" {
		t.Fatalf("positional = %v want just the input", positional)
	}
	fs := testFlagSet()
	if err := fs.Parse(ordered); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := fs.Lookup("keep").Value.String(); !strings.Contains(got, "-0:30") {
		t.Fatalf("keep = %q want the range preserved", got)
	}
}

func TestPermuteStopsAtDoubleDash(t *testing.T) {
	_, positional := permute(testFlagSet(), []string{"-f", "--", "-weird-name.mp4"})
	if len(positional) != 1 || positional[0] != "-weird-name.mp4" {
		t.Fatalf("positional = %v want the dashed filename treated as input", positional)
	}
}

func TestPermuteHandlesEqualsForm(t *testing.T) {
	ordered, _ := permute(testFlagSet(), []string{"in.mp4", "-o=out.mp4"})
	fs := testFlagSet()
	if err := fs.Parse(ordered); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := fs.Lookup("o").Value.String(); got != "out.mp4" {
		t.Fatalf("-o = %q", got)
	}
}

func TestWantsHelp(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		positional []string
		want       bool
	}{
		{"long form", []string{"--help"}, nil, true},
		{"short form", []string{"-h"}, nil, true},
		{"help as a subcommand", []string{"help"}, []string{"help"}, true},
		{"no request", []string{"-f", "in.mp4"}, []string{"in.mp4"}, false},
		// -prompt help names a vocabulary hint; it is not a question.
		{"help as a flag value", []string{"-prompt", "help", "in.mp4"}, []string{"in.mp4"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := wantsHelp(tt.args, tt.positional); got != tt.want {
				t.Fatalf("wantsHelp(%v, %v) = %v want %v", tt.args, tt.positional, got, tt.want)
			}
		})
	}
}

func TestResolveOutputsDefaultsBesideTheInput(t *testing.T) {
	got, err := resolveOutputs([]string{"clips/talk.mp4"}, "", "", "clean", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != filepath.Join("clips", "talk.clean.mp4") {
		t.Fatalf("got %v want clips/talk.clean.mp4", got)
	}
}

func TestResolveOutputsEmptyTagReplacesTheExtension(t *testing.T) {
	got, err := resolveOutputs([]string{"talk.mp4"}, "", "", "", ".json")
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "talk.json" {
		t.Fatalf("got %v want talk.json", got)
	}
}

func TestResolveOutputsHonoursOutDir(t *testing.T) {
	got, err := resolveOutputs([]string{"a.mp4", "b/c.mp4"}, "", "out", "cut", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join("out", "a.cut.mp4"), filepath.Join("out", "c.cut.mp4")}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

// -o names one file. Silently writing every input to it would leave the user
// with only the last one.
func TestResolveOutputsRejectsSingleOutputForManyInputs(t *testing.T) {
	_, err := resolveOutputs([]string{"a.mp4", "b.mp4"}, "out.mp4", "", "cut", "")
	if err == nil || !strings.Contains(err.Error(), "-outdir") {
		t.Fatalf("err=%v want it to point at -outdir", err)
	}
}

// An editing tool that eats its source on a typo is not one you use twice.
func TestResolveOutputsRefusesToOverwriteTheInput(t *testing.T) {
	if _, err := resolveOutputs([]string{"talk.mp4"}, "./talk.mp4", "", "cut", ""); err == nil {
		t.Fatal("writing over the input should be refused")
	}
}

func TestDisplayWidthCountsCJKAsTwoColumns(t *testing.T) {
	tests := map[string]int{
		"filler":   6,
		"嗯":        2,
		"filler 嗯": 9, // 6 + space + 2
		"":         0,
	}
	for input, want := range tests {
		if got := displayWidth(input); got != want {
			t.Fatalf("displayWidth(%q) = %d want %d", input, got, want)
		}
	}
	if got := padDisplay("嗯", 6); got != "嗯    " {
		t.Fatalf("padDisplay = %q want four trailing spaces", got)
	}
}

func TestRegistryLooksUpAliases(t *testing.T) {
	r := allCommands()
	for _, alias := range []string{"asr", "sub", "trim", "join", "info", "cred"} {
		if _, ok := r.lookup(alias); !ok {
			t.Fatalf("alias %q does not resolve", alias)
		}
	}
	if _, ok := r.lookup("nonsense"); ok {
		t.Fatal("an unknown name must not resolve")
	}
}

func TestSuggestFindsNearMisses(t *testing.T) {
	got := allCommands().suggest("subtitel")
	if len(got) == 0 || got[0] != "subtitle" {
		t.Fatalf("suggest = %v want subtitle", got)
	}
}

// Every command must be reachable, described, and able to render its help
// without a config file or any input.
func TestEveryCommandHasHelp(t *testing.T) {
	for _, cmd := range allCommands().commands {
		t.Run(cmd.Name, func(t *testing.T) {
			if cmd.Summary == "" || cmd.Long == "" {
				t.Fatal("a command needs both a summary and a description")
			}
			if len(cmd.Examples) == 0 {
				t.Fatal("a flag list rarely answers 'how do I use it'; add an example")
			}
			out := captureStdout(t, func() {
				if err := allCommands().run(context.Background(), cmd.Name, []string{"--help"}); err != nil {
					t.Fatalf("help failed: %v", err)
				}
			})
			if !strings.Contains(out, "Usage:") || !strings.Contains(out, cmd.Name) {
				t.Fatalf("help for %s looks wrong:\n%s", cmd.Name, out)
			}
		})
	}
}

// An end-to-end pass over a real file, which is the only way to know the filter
// graph is accepted rather than merely well-formed.
func TestCutProducesAShorterFile(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	source := filepath.Join(dir, "src.mp4")
	generateTestVideo(t, source, 6)

	out := filepath.Join(dir, "out.mp4")
	err := allCommands().run(context.Background(), "cut",
		[]string{"-q", "-keep", "1-2", "-keep", "4-5", "-o", out, source})
	if err != nil {
		t.Fatalf("cut: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		t.Fatalf("no usable output: %v", err)
	}
	if seconds := probeDuration(t, out); seconds < 1.5 || seconds > 2.5 {
		t.Fatalf("output is %.2fs want about 2s", seconds)
	}
}

func TestCutRefusesToCombineKeepAndDrop(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	source := filepath.Join(dir, "src.mp4")
	generateTestVideo(t, source, 3)

	err := allCommands().run(context.Background(), "cut",
		[]string{"-q", "-keep", "0-1", "-drop", "2-3", source})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("err=%v want a refusal to guess", err)
	}
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	for _, binary := range []string{"ffmpeg", "ffprobe"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is not installed", binary)
		}
	}
}

func generateTestVideo(t *testing.T, path string, seconds int) {
	t.Helper()
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=160x120:rate=15:duration="+strconv.Itoa(seconds),
		"-f", "lavfi", "-i", "sine=frequency=440:duration="+strconv.Itoa(seconds),
		"-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate fixture: %v\n%s", err, out)
	}
}

func probeDuration(t *testing.T, path string) float64 {
	t.Helper()
	out, err := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration",
		"-of", "default=nw=1:nk=1", path).Output()
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	seconds, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil {
		t.Fatalf("parse duration %q: %v", out, err)
	}
	return seconds
}

// captureStdout runs fn with os.Stdout redirected to a temp file and returns
// what it wrote. The commands print through os.Stdout by design, so that is
// where the help text has to be read from.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdout")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = file
	fn()
	os.Stdout = original
	file.Close()

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A chained invocation has to find the file it expects even on a take that
// needed no cleaning, so filler writes its output regardless.
func TestFillerWritesAnOutputEvenWithNothingToCut(t *testing.T) {
	requireFFmpeg(t)

	dir := t.TempDir()
	source := filepath.Join(dir, "clean.mp4")
	generateTestVideo(t, source, 4)

	// A transcript with one word and no filler, no stutter and no long pause.
	transcript := `{"version":"vs.transcript.v1","duration_ms":4000,"cues":[` +
		`{"start_ms":0,"end_ms":400,"text":"好","words":[` +
		`{"text":"好","start_ms":0,"end_ms":400}]}]}`
	transcriptPath := filepath.Join(dir, "t.json")
	if err := os.WriteFile(transcriptPath, []byte(transcript), 0o600); err != nil {
		t.Fatal(err)
	}

	err := allCommands().run(context.Background(), "filler",
		[]string{"-q", "-transcript", transcriptPath, "-max-pause", "0", "-trim-ends=false", source})
	if err != nil {
		t.Fatalf("filler: %v", err)
	}

	out := filepath.Join(dir, "clean.clean.mp4")
	info, err := os.Stat(out)
	if err != nil || info.Size() == 0 {
		t.Fatalf("expected a copy at %s: %v", out, err)
	}
}

// The sidecar transcript is inferred from the video's name, not chosen by the
// user, so some other tool's talk.json must not be read as an empty transcript.
func TestUnrelatedSidecarJSONIsIgnored(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "talk.mp4")
	if err := os.WriteFile(source, []byte("not really a video"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "talk.json"), []byte(`{"unrelated":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	env := &Env{Stdout: os.Stdout, Stderr: os.Stderr, Quiet: true}
	// Recognition is not reachable here, but the sidecar must have been
	// rejected before it was attempted: a silently empty transcript would have
	// produced a successful no-op instead.
	_, path, err := resolveTranscript(context.Background(), env, source, &asrFlags{})
	if err == nil {
		t.Fatal("an unrelated JSON must not be accepted as a transcript")
	}
	if path != "" {
		t.Fatalf("path=%q want no transcript adopted", path)
	}
}
