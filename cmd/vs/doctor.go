package main

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"strings"

	"github.com/sequencestream/video-stream/internal/asr"
	"github.com/sequencestream/video-stream/internal/config"
	"github.com/sequencestream/video-stream/internal/ffmpeg"
)

type check struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

func doctorCommand() *Command {
	return &Command{
		Name:    "doctor",
		Group:   groupSetup,
		Summary: "Check that everything vs shells out to is installed and working",
		NoInput: true,
		Long: `Runs each external dependency once and reports what is missing, with the
command that fixes it.

ffmpeg and ffprobe are required by every command. Python and faster-whisper are
required only by the commands that recognize speech — ` + "`vs transcribe`" + `,
` + "`vs subtitle`" + ` and ` + "`vs filler`" + ` — so a missing recognizer is reported as a
warning, not a failure.`,
		Examples: []Example{
			{Command: "vs doctor"},
			{Command: "vs doctor -json", Note: "for a setup script to parse"},
		},
		Setup: func(fs *flag.FlagSet) {},
		Run: func(ctx context.Context, env *Env, args []string) error {
			checks := runChecks(ctx, env.Config)
			for _, c := range checks {
				mark := "✗"
				if c.OK {
					mark = "✓"
				}
				env.Printf("%s %-16s %s\n", mark, c.Name, c.Detail)
				if !c.OK && c.Fix != "" {
					env.Printf("    fix: %s\n", c.Fix)
				}
			}
			if err := env.EmitJSON(checks); err != nil {
				return err
			}
			// Only the required tools decide the exit status; a setup script
			// that only needs cutting should not be told the whole thing is
			// broken because it has no speech model.
			for _, c := range checks[:2] {
				if !c.OK {
					return errSilent
				}
			}
			return nil
		},
	}
}

func runChecks(ctx context.Context, cfg config.Config) []check {
	checks := []check{
		binaryCheck("ffmpeg", cfg.Tools.FFmpeg, "install ffmpeg (brew install ffmpeg, apt install ffmpeg)"),
		binaryCheck("ffprobe", cfg.Tools.FFprobe, "ffprobe ships with ffmpeg; set tools.ffprobe if it lives elsewhere"),
		binaryCheck("python", cfg.Tools.Python, "install Python 3, or set tools.python in the config"),
	}

	// Burning subtitles is the one operation a plausible ffmpeg build can be
	// missing, so it gets its own line rather than surfacing as a filter-graph
	// error the first time someone runs vs subtitle.
	tool := ffmpeg.Tool{FFmpeg: cfg.Tools.FFmpeg, FFprobe: cfg.Tools.FFprobe}
	if checks[0].OK {
		if tool.HasFilter(ctx, "subtitles") {
			checks = append(checks, check{Name: "subtitle burn-in", OK: true, Detail: "libass available"})
		} else {
			checks = append(checks, check{
				Name: "subtitle burn-in", OK: false,
				Detail: "this ffmpeg has no `subtitles` filter, so vs subtitle -mode burn will not work",
				Fix:    "install an ffmpeg built with libass; vs subtitle -mode soft works either way",
			})
		}
	}

	recognizer := asr.FasterWhisper{Python: cfg.Tools.Python}
	if err := recognizer.Check(ctx); err != nil {
		checks = append(checks, check{
			Name: "faster-whisper", OK: false,
			Detail: "not available — vs transcribe, subtitle and filler will not work",
			Fix:    strings.TrimSpace(cfg.Tools.Python) + " -m pip install faster-whisper",
		})
	} else {
		checks = append(checks, check{
			Name: "faster-whisper", OK: true,
			Detail: "installed (model " + cfg.ASR.Model + ")",
		})
	}

	path := config.DefaultPath()
	if _, err := os.Stat(path); err == nil {
		checks = append(checks, check{Name: "config", OK: true, Detail: path})
	} else {
		checks = append(checks, check{
			Name: "config", OK: true,
			Detail: "using built-in defaults (no file at " + path + ")",
		})
	}
	return checks
}

func binaryCheck(name, binary, fix string) check {
	if strings.TrimSpace(binary) == "" {
		binary = name
	}
	resolved, err := exec.LookPath(binary)
	if err != nil {
		return check{Name: name, OK: false, Detail: "not found: " + binary, Fix: fix}
	}
	return check{Name: name, OK: true, Detail: resolved}
}
