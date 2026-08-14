// Command vs is a video editing CLI.
//
// One invocation performs one atomic operation — transcribe a file, cut the
// filler words out of it, burn subtitles onto it — and exits. There is no
// daemon, no project database and no state carried between runs: the unit of
// work is a file on disk, and the way two operations compose is that the second
// one reads what the first one wrote.
//
// Everything ffmpeg already does is delegated to ffmpeg unchanged. What this
// tool contributes is the part that is genuinely hard to remember: which filter
// graph expresses "keep these 180 spans and drop the rest", how a subtitle path
// has to be escaped before libass will read it, and which of the two -ss
// positions seeks instead of decoding.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		if !errors.Is(err, errSilent) {
			fmt.Fprintf(os.Stderr, "vs: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	commands := allCommands()

	if len(args) == 0 {
		printMainHelp(os.Stdout, commands)
		return nil
	}

	switch args[0] {
	case "help", "--help", "-h":
		// `vs help subtitle` and `vs subtitle --help` must print the same page.
		if len(args) > 1 {
			return commands.run(ctx, args[1], []string{"--help"})
		}
		printMainHelp(os.Stdout, commands)
		return nil
	case "version", "--version", "-V":
		fmt.Println(version)
		return nil
	}

	return commands.run(ctx, args[0], args[1:])
}

// Command groups, in the order they appear in the top-level help. Speech comes
// first because transcription is what most other commands depend on.
const (
	groupSpeech    = "Speech and subtitles:"
	groupCut       = "Cutting:"
	groupTransform = "Transforming:"
	groupInspect   = "Inspecting:"
	groupSetup     = "Setup:"
)

func allCommands() *registry {
	return newRegistry(
		transcribeCommand(),
		subtitleCommand(),

		fillerCommand(),
		silenceCommand(),
		cutCommand(),

		resizeCommand(),
		speedCommand(),
		concatCommand(),
		audioCommand(),

		probeCommand(),

		doctorCommand(),
		credentialCommand(),
	)
}
