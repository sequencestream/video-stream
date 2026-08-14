// Command vs is the video-stream CLI, and the product's only interface.
//
// It is a thin HTTP client for the main service rather than a second database
// client, so a command and the service can never disagree about what a task or
// a project is. Every command takes -json, because the caller this is built for
// is an agent that parses what it gets back rather than reads it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var version = "0.1.0-dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "vs: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("a command is required")
	}

	switch args[0] {
	case "video":
		return cmdVideo(ctx, args[1:])
	case "project", "proj":
		return cmdProject(ctx, args[1:])
	case "create":
		return cmdCreate(ctx, args[1:])
	case "render":
		return cmdRender(ctx, args[1:])
	case "status":
		return cmdStatus(ctx, args[1:])
	case "credential", "cred":
		return cmdCredential(ctx, args[1:])
	case "version", "--version", "-v":
		fmt.Println(version)
		return nil
	case "help", "--help", "-h":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `vs - video-stream CLI

Usage:
  vs video -title T -script FILE [-image IMG]   script to finished MP4 in one call
  vs project <cmd>         import and inspect projects (see vs project help)
  vs render <project>      render a stored project
  vs create [flags]        submit a task (defaults to the echo fake task)
  vs status <task-id>      show a task
  vs credential <cmd>      manage provider API keys (see vs credential help)
  vs version               print the CLI version

Common flags:
  -server string   main service base URL (env VS_SERVER, default http://127.0.0.1:8080)
  -json            print the raw JSON response
`)
}

func cmdCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("create", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	taskType := fs.String("type", "echo", "task type to submit")
	title := fs.String("title", "untitled", "human readable task title")
	message := fs.String("message", "hello from vs", "message carried in the echo payload")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	wait := fs.Bool("wait", false, "poll until the task reaches a terminal state")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to wait when -wait is set")
	if err := fs.Parse(args); err != nil {
		return err
	}

	client := newClient(*server)
	task, err := client.CreateTask(ctx, createTaskRequest{
		Type:    *taskType,
		Title:   *title,
		Payload: map[string]any{"message": *message},
	})
	if err != nil {
		return err
	}

	if *wait {
		if task, err = client.WaitForTask(ctx, task.ID, *timeout); err != nil {
			return err
		}
	}
	return printTask(task, *asJSON)
}

func cmdRender(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	resolution := fs.String("resolution", "1080p", "target resolution: 720p or 1080p")
	platform := fs.String("platform", "", "target platform; selects subtitle mode and loudness")
	subtitleMode := fs.String("subtitle-mode", "", "burn_in or soft; defaults to the platform preference")
	stillImages := fs.Bool("still-images", false, "hold background images steady instead of applying Ken Burns")
	finalized := fs.Bool("finalized", false, "allow finalized-only stages such as BGM beat matching")
	wait := fs.Bool("wait", false, "poll until the render settles")
	timeout := fs.Duration("timeout", renderTimeout, "how long to wait when -wait is set")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(args); err != nil {
		return err
	}

	project := fs.Arg(0)
	if project == "" {
		return errors.New("usage: vs render <project>")
	}

	task, err := runRenderTask(ctx, newClient(*server), renderOptions{
		ProjectID: project, Resolution: *resolution, Platform: *platform,
		SubtitleMode: *subtitleMode, StillImages: *stillImages, Finalized: *finalized,
		Wait: *wait, Timeout: *timeout,
	})
	if err != nil {
		return err
	}
	return printTask(task, *asJSON)
}

func cmdStatus(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(args); err != nil {
		return err
	}

	id := fs.Arg(0)
	if id == "" {
		return errors.New("usage: vs status <task-id>")
	}

	task, err := newClient(*server).GetTask(ctx, id)
	if err != nil {
		return err
	}
	return printTask(task, *asJSON)
}

// writeJSON is the single JSON writer so every command emits the same shape:
// indented, newline-terminated, parseable by whatever called it.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func defaultServer() string {
	if v := os.Getenv("VS_SERVER"); v != "" {
		return v
	}
	return "http://127.0.0.1:8080"
}

// printTask renders the task receipt. The human form is deliberately a short
// key/value block: it is what the acceptance check reads.
func printTask(t task, asJSON bool) error {
	if asJSON {
		return writeJSON(os.Stdout, t)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "task id    %s\n", t.ID)
	fmt.Fprintf(&b, "type       %s\n", t.Type)
	fmt.Fprintf(&b, "title      %s\n", t.Title)
	fmt.Fprintf(&b, "status     %s\n", t.Status)
	fmt.Fprintf(&b, "created    %s\n", t.CreatedAt.Format(time.RFC3339))
	if t.Error != "" {
		fmt.Fprintf(&b, "error      %s\n", t.Error)
	}
	if len(t.Result) > 0 {
		encoded, err := json.Marshal(t.Result)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "result     %s\n", encoded)
	}

	fmt.Print(b.String())
	return nil
}
