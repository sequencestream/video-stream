package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/ffmpeg"
)

// Command is one atomic operation.
//
// One invocation does one thing to one set of files and then exits. There is no
// session, no project file and no daemon holding state between calls, which is
// what makes the commands composable: the output of one is a file the next one
// takes as input.
type Command struct {
	// Name is what the user types.
	Name string
	// Aliases are alternative spellings, listed in help as "(also: x)".
	Aliases []string
	// Group buckets the command in the top-level help.
	Group string
	// Summary is the one-line description in the command list.
	Summary string
	// Args describes the positional arguments, e.g. "<input>...".
	Args string
	// Long is the paragraph shown above the flags in this command's help. It
	// should explain what the command does and, where it matters, what it
	// deliberately does not do.
	Long string
	// Examples are shown at the bottom of the command's help. Every command
	// gets at least one, because a flag list rarely answers "how do I use it".
	Examples []Example
	// Setup registers the command's own flags.
	Setup func(fs *flag.FlagSet)
	// Run executes the command with the positional arguments.
	Run func(ctx context.Context, env *Env, args []string) error
	// NoInput marks a command that takes no media file, so the shared input
	// checks are skipped.
	NoInput bool
}

// Example is a sample invocation with a note about what it is for.
type Example struct {
	Command string
	Note    string
}

// Usage renders the one-line usage string.
func (c *Command) Usage() string {
	parts := []string{"vs", c.Name, "[flags]"}
	if c.Args != "" {
		parts = append(parts, c.Args)
	}
	return strings.Join(parts, " ")
}

// Env is everything a command needs that did not come from its own flags.
type Env struct {
	Config config.Config
	FFmpeg ffmpeg.Tool

	// JSON prints machine-readable output instead of the human summary.
	JSON bool
	// Quiet suppresses progress and the summary, leaving only errors.
	Quiet bool
	// Verbose passes ffmpeg's own output through.
	Verbose bool
	// Force allows overwriting existing output files.
	Force bool
	// DryRun prints the commands that would run without running them.
	DryRun bool

	Stdout io.Writer
	Stderr io.Writer
}

// Printf writes a line of human-facing output, unless output is quiet or JSON.
//
// Every command routes its summary through here so that -json produces exactly
// one JSON document on stdout with nothing else mixed in. A caller parsing the
// output is the reason the flag exists.
func (e *Env) Printf(format string, args ...any) {
	if e.Quiet || e.JSON {
		return
	}
	fmt.Fprintf(e.Stdout, format, args...)
}

// Progress writes a status line to stderr, which stays out of piped output.
func (e *Env) Progress(format string, args ...any) {
	if e.Quiet || e.Verbose {
		return
	}
	fmt.Fprintf(e.Stderr, format, args...)
}

// EmitJSON writes the machine-readable result, if -json was given.
func (e *Env) EmitJSON(v any) error {
	if !e.JSON {
		return nil
	}
	enc := json.NewEncoder(e.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// globalFlags are the flags every command accepts.
//
// They are registered per command rather than parsed before the command name
// so that `vs filler -v clip.mp4` works. Requiring `vs -v filler clip.mp4` is
// the kind of ordering rule nobody remembers.
type globalFlags struct {
	config  string
	json    bool
	quiet   bool
	verbose bool
	force   bool
	dryRun  bool
}

func (g *globalFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&g.config, "config", "", "config file path (default "+config.DefaultPath()+")")
	fs.BoolVar(&g.json, "json", false, "print the result as JSON")
	fs.BoolVar(&g.quiet, "q", false, "suppress everything but errors")
	fs.BoolVar(&g.verbose, "v", false, "show ffmpeg's own output")
	fs.BoolVar(&g.force, "f", false, "overwrite existing output files")
	fs.BoolVar(&g.dryRun, "n", false, "print the commands that would run, then stop")
}

// env builds the shared environment from the parsed global flags.
func (g *globalFlags) env() (*Env, error) {
	cfg, err := config.Load(g.config)
	if err != nil {
		return nil, err
	}
	return &Env{
		Config: cfg,
		FFmpeg: ffmpeg.Tool{
			FFmpeg: cfg.Tools.FFmpeg, FFprobe: cfg.Tools.FFprobe,
			Verbose: g.verbose, Overwrite: g.force, DryRun: g.dryRun,
		},
		JSON: g.json, Quiet: g.quiet, Verbose: g.verbose,
		Force: g.force, DryRun: g.dryRun,
		Stdout: os.Stdout, Stderr: os.Stderr,
	}, nil
}

// registry holds the commands in the order they should be listed.
type registry struct {
	commands []*Command
	byName   map[string]*Command
	groups   []string
}

func newRegistry(commands ...*Command) *registry {
	r := &registry{byName: make(map[string]*Command)}
	seenGroup := make(map[string]bool)
	for _, c := range commands {
		r.commands = append(r.commands, c)
		r.byName[c.Name] = c
		for _, alias := range c.Aliases {
			r.byName[alias] = c
		}
		if !seenGroup[c.Group] {
			seenGroup[c.Group] = true
			r.groups = append(r.groups, c.Group)
		}
	}
	return r
}

func (r *registry) lookup(name string) (*Command, bool) {
	c, ok := r.byName[name]
	return c, ok
}

// suggest finds commands whose name is close to what was typed, so a typo gets
// an answer instead of a list of forty commands.
func (r *registry) suggest(name string) []string {
	var out []string
	for _, c := range r.commands {
		if strings.HasPrefix(c.Name, name) || strings.HasPrefix(name, c.Name) || editDistance(c.Name, name) <= 2 {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// run parses the command's flags and executes it.
func (r *registry) run(ctx context.Context, name string, args []string) error {
	cmd, ok := r.lookup(name)
	if !ok {
		return unknownCommand(r, name)
	}

	fs := flag.NewFlagSet(cmd.Name, flag.ContinueOnError)
	// Suppress the flag package's own usage dump: it prints an unordered flag
	// list with no context, and this file prints a better one.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	var global globalFlags
	global.register(fs)
	if cmd.Setup != nil {
		cmd.Setup(fs)
	}

	// Reorder before parsing so flags may follow the file names. Go's flag
	// package stops at the first positional argument, which would make
	// `vs filler talk.mp4 -aggressive` silently ignore the flag — and silently
	// ignoring an editing option is the worst possible outcome.
	args, positional := permute(fs, args)

	if wantsHelp(args, positional) {
		printCommandHelp(os.Stdout, cmd, fs)
		return nil
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printCommandHelp(os.Stdout, cmd, fs)
			return nil
		}
		fmt.Fprintf(os.Stderr, "vs %s: %v\n\n", cmd.Name, err)
		printCommandHelp(os.Stderr, cmd, fs)
		return errSilent
	}

	env, err := global.env()
	if err != nil {
		return err
	}
	if err := cmd.Run(ctx, env, fs.Args()); err != nil {
		return err
	}
	if env.DryRun {
		// The summary above describes the output as though it exists. Say
		// plainly that it does not, rather than leaving the user to infer it
		// from the flag they passed several minutes ago.
		fmt.Fprintln(env.Stderr, "dry run: no files were written")
	}
	return nil
}

// errSilent ends the process with a failure status without printing again.
var errSilent = errors.New("")

// permute splits arguments into flags and positionals and returns them with the
// flags first, which is the order flag.Parse requires.
//
// A flag that takes a value swallows the argument after it, including one that
// starts with a dash: `-keep -0:30` means "the first thirty seconds", not a
// flag named -0:30.
func permute(fs *flag.FlagSet, args []string) (ordered, positional []string) {
	var flags []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}

		flags = append(flags, arg)
		name := strings.TrimLeft(arg, "-")
		if strings.Contains(name, "=") {
			continue
		}
		// An unknown flag is left for flag.Parse to reject: its error message
		// names the flag, which is better than anything guessed here.
		f := fs.Lookup(name)
		if f == nil || flagKind(f) == "" {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...), positional
}

// wantsHelp reports whether the arguments are a request for help rather than
// work. It is checked before parsing so that `vs cut --help` explains cut
// instead of complaining that no input was given.
//
// Only the flags and the first positional are considered: a caller passing
// -prompt help is naming a vocabulary hint, not asking a question.
func wantsHelp(args, positional []string) bool {
	if len(positional) > 0 && positional[0] == "help" {
		return true
	}
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "-h" || a == "--help" {
			return true
		}
	}
	return false
}

func unknownCommand(r *registry, name string) error {
	msg := fmt.Sprintf("unknown command %q", name)
	if suggestions := r.suggest(name); len(suggestions) > 0 {
		msg += "\n\nDid you mean:\n"
		for _, s := range suggestions {
			msg += "  vs " + s + "\n"
		}
	}
	return errors.New(msg + "\nRun `vs --help` for the full list.")
}

// printCommandHelp renders the help for one command: what it does, how to call
// it, every flag with its default, and worked examples.
func printCommandHelp(w io.Writer, cmd *Command, fs *flag.FlagSet) {
	fmt.Fprintf(w, "%s\n\n", cmd.Summary)
	fmt.Fprintf(w, "Usage:\n  %s\n", cmd.Usage())
	if len(cmd.Aliases) > 0 {
		fmt.Fprintf(w, "\nAlso available as: %s\n", strings.Join(cmd.Aliases, ", "))
	}
	if long := strings.TrimSpace(cmd.Long); long != "" {
		fmt.Fprintf(w, "\n%s\n", long)
	}

	own, recognition, global := splitFlags(fs)
	if len(own) > 0 {
		fmt.Fprint(w, "\nFlags:\n")
		writeFlags(w, own)
	}
	if len(recognition) > 0 {
		fmt.Fprint(w, "\nSpeech recognition flags (used only if a transcript has to be produced):\n")
		writeFlags(w, recognition)
	}
	if len(global) > 0 {
		fmt.Fprint(w, "\nCommon flags:\n")
		writeFlags(w, global)
	}

	if len(cmd.Examples) > 0 {
		fmt.Fprint(w, "\nExamples:\n")
		for _, ex := range cmd.Examples {
			if ex.Note != "" {
				fmt.Fprintf(w, "  # %s\n", ex.Note)
			}
			fmt.Fprintf(w, "  %s\n\n", ex.Command)
		}
	}
}

// globalFlagNames is the set separated out under "Common flags", so a
// command's own options are not buried among the six every command shares.
var globalFlagNames = map[string]bool{
	"config": true, "json": true, "q": true, "v": true, "f": true, "n": true,
}

// asrFlagNames is the recognition group. Three commands carry all eleven of
// them, and listing them beside the command's own two or three options makes
// the command look far more complicated than it is.
var asrFlagNames = map[string]bool{
	"transcript": true, "model": true, "lang": true, "device": true,
	"compute-type": true, "model-dir": true, "prompt": true,
	"threads": true, "beam": true, "no-vad": true,
}

func splitFlags(fs *flag.FlagSet) (own, recognition, global []*flag.Flag) {
	fs.VisitAll(func(f *flag.Flag) {
		switch {
		case globalFlagNames[f.Name]:
			global = append(global, f)
		case asrFlagNames[f.Name]:
			recognition = append(recognition, f)
		default:
			own = append(own, f)
		}
	})
	return own, recognition, global
}

func writeFlags(w io.Writer, flags []*flag.Flag) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, f := range flags {
		name := "-" + f.Name
		if kind := flagKind(f); kind != "" {
			name += " " + kind
		}
		usage := f.Usage
		if def := f.DefValue; showsDefault(def) {
			usage += fmt.Sprintf(" (default %s)", def)
		}
		fmt.Fprintf(tw, "  %s\t%s\n", name, usage)
	}
	tw.Flush()
}

// showsDefault hides the values that mean "unset" rather than a real default.
// A help line reading "(default -1ns)" describes an implementation detail — the
// sentinel that keeps an explicit zero distinguishable — and tells the reader
// nothing they can act on.
func showsDefault(def string) bool {
	switch def {
	case "", "false", "0", "0s", "-1", "-1s", "-1ns", "[]":
		return false
	default:
		return true
	}
}

// flagKind names the value a flag takes, so help reads "-lang string" rather
// than leaving the reader guess whether it is a switch.
//
// The flag package's value types are unexported, so this reads the concrete
// type's name. That is fragile in principle and stable in practice: these
// names have not changed since Go 1.
func flagKind(f *flag.Flag) string {
	switch fmt.Sprintf("%T", f.Value) {
	case "*flag.boolValue":
		return ""
	case "*flag.stringValue":
		return "string"
	case "*flag.intValue", "*flag.int64Value", "*flag.uintValue", "*flag.uint64Value":
		return "int"
	case "*flag.float64Value":
		return "number"
	case "*flag.durationValue":
		return "duration"
	case "*main.stringList":
		return "range"
	default:
		return "value"
	}
}

// printMainHelp renders the top-level help: what vs is, then every command
// grouped by what it is for.
func printMainHelp(w io.Writer, r *registry) {
	fmt.Fprint(w, `vs — a video editing CLI.

Each command does one thing to one or more files and exits. Everything that
ffmpeg already does well is handed straight to ffmpeg; what vs adds is that you
do not have to remember the filter graph, the escaping rules or the flag order.

Usage:
  vs <command> [flags] <input>...

`)

	// One column width across every group, not one per group: a tabwriter
	// restarts its alignment at each header line, which leaves the sections
	// visibly out of step with each other.
	width := 0
	for _, c := range r.commands {
		width = max(width, len(c.Name))
	}
	for _, group := range r.groups {
		fmt.Fprintf(w, "%s\n", group)
		for _, c := range r.commands {
			if c.Group != group {
				continue
			}
			fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Summary)
		}
		fmt.Fprintln(w)
	}

	fmt.Fprint(w, `Common flags (accepted by every command):
  -json      print the result as JSON
  -f         overwrite existing output files
  -n         print the commands that would run, then stop
  -v         show ffmpeg's own output
  -q         suppress everything but errors
  -config    config file path

Run `+"`vs <command> --help`"+` for a command's own flags and examples.
`)
}

// resolveOutputs decides where each input's result is written.
//
// The rules are the ones a shell user already expects: -o names the file when
// there is exactly one input, -outdir names the directory when there are many,
// and with neither the result lands beside its source under a name that says
// what happened to it. Overwriting an input is refused outright — an editing
// tool that eats its source on a typo is not one you use twice.
func resolveOutputs(inputs []string, out, outDir, tag, ext string) ([]string, error) {
	if len(inputs) == 0 {
		return nil, errors.New("no input files")
	}
	if out != "" && len(inputs) > 1 {
		return nil, fmt.Errorf("-o names a single file but %d inputs were given; use -outdir instead", len(inputs))
	}

	outputs := make([]string, 0, len(inputs))
	for _, in := range inputs {
		var path string
		switch {
		case out != "":
			path = out
		default:
			base := strings.TrimSuffix(filepath.Base(in), filepath.Ext(in))
			suffix := ext
			if suffix == "" {
				suffix = filepath.Ext(in)
			}
			// An empty tag means the result replaces the input's extension
			// rather than sitting beside it under a decorated name — right for
			// a transcript, wrong for an edited video.
			name := base + suffix
			if tag != "" {
				name = base + "." + tag + suffix
			}
			dir := outDir
			if dir == "" {
				dir = filepath.Dir(in)
			}
			path = filepath.Join(dir, name)
		}
		if sameFile(path, in) {
			return nil, fmt.Errorf("output %s is the input file; pick another name", path)
		}
		outputs = append(outputs, path)
	}
	return outputs, nil
}

// sameFile compares two paths after resolving them, catching the ./x.mp4 and
// x.mp4 spelling of the same file.
func sameFile(a, b string) bool {
	if a == b {
		return true
	}
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}

// editDistance is the Levenshtein distance, used only to suggest a command
// after a typo.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
