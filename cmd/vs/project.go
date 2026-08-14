package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// importTimeout covers a full script import. Every line is synthesized once to
// measure it, so the wall clock scales with the script rather than with the
// request.
const importTimeout = 10 * time.Minute

func cmdProject(ctx context.Context, args []string) error {
	if len(args) == 0 {
		projectUsage()
		return errors.New("a project subcommand is required")
	}
	switch args[0] {
	case "create":
		return cmdProjectCreate(ctx, args[1:])
	case "list", "ls":
		return cmdProjectList(ctx, args[1:])
	case "show":
		return cmdProjectShow(ctx, args[1:])
	case "rm", "delete":
		return cmdProjectRemove(ctx, args[1:])
	case "background", "bg":
		return cmdProjectBackground(ctx, args[1:])
	case "help", "--help", "-h":
		projectUsage()
		return nil
	default:
		projectUsage()
		return fmt.Errorf("unknown project subcommand %q", args[0])
	}
}

func projectUsage() {
	fmt.Fprint(os.Stderr, `vs project - turn narration into a renderable project

Usage:
  vs project create -title T -script FILE   import a script (- reads stdin)
  vs project list                           list stored projects
  vs project show <id>                      show one project
  vs project rm <id>                        delete a project
  vs project background <id> -image FILE    fit a background image to the frame

Create flags:
  -title string      video title (required)
  -script string     path to the narration text, or - for stdin (required)
  -voice string      TTS voice; defaults to the server's configured voice
  -id string         explicit project id; defaults to a slug of the title
  -max-runes int     split any line longer than this at its last comma
  -min-runes int     merge any line shorter than this into the one before it

Background flags:
  -image string      source image on the machine running vsd (required)
  -anchor string     top|center|bottom band to keep when cropping (default center)
  -resolution string frame to fit: 720p or 1080p (default 1080p)
`)
}

func cmdProjectCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("project create", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	title := fs.String("title", "", "video title")
	script := fs.String("script", "", "path to the narration text, or - for stdin")
	voice := fs.String("voice", "", "TTS voice")
	id := fs.String("id", "", "explicit project id")
	maxRunes := fs.Int("max-runes", 0, "split lines longer than this")
	minRunes := fs.Int("min-runes", 0, "merge lines shorter than this")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *title == "" {
		return errors.New("usage: vs project create -title T -script FILE")
	}
	if *script == "" {
		return errors.New("-script is required (use - to read stdin)")
	}

	body, err := readScript(*script)
	if err != nil {
		return err
	}

	result, err := newClient(*server).withTimeout(importTimeout).CreateProject(ctx, createProjectRequest{
		ProjectID: *id, Title: *title, Script: body, Voice: *voice,
		MaxRunes: *maxRunes, MinRunes: *minRunes,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(result)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "project id %s\n", result.Project.ID)
	fmt.Fprintf(&b, "title      %s\n", result.Project.Title)
	fmt.Fprintf(&b, "segs       %d\n", result.SegCount)
	fmt.Fprintf(&b, "duration   %s\n", formatMS(result.TotalMS))
	for _, line := range result.Lines {
		fmt.Fprintf(&b, "  %-4s %6s  %s\n", line.SegID, formatMS(line.ProbedMS), line.Text)
	}
	fmt.Print(b.String())
	return nil
}

func cmdProjectList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	limit := fs.Int("limit", 0, "maximum projects to list")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(args); err != nil {
		return err
	}

	projects, err := newClient(*server).ListProjects(ctx, *limit)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(map[string]any{"projects": projects})
	}
	if len(projects) == 0 {
		fmt.Println("no projects yet")
		return nil
	}
	var b strings.Builder
	for _, p := range projects {
		fmt.Fprintf(&b, "%-32s %3d segs  %s  %s\n", p.ID, p.SegCount, p.UpdatedAt.Format(time.RFC3339), p.Title)
	}
	fmt.Print(b.String())
	return nil
}

func cmdProjectShow(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("project show", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := fs.Arg(0)
	if id == "" {
		return errors.New("usage: vs project show <id>")
	}

	p, err := newClient(*server).GetProject(ctx, id)
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(p)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "project id %s\n", p.ID)
	fmt.Fprintf(&b, "title      %s\n", p.Title)
	fmt.Fprintf(&b, "segs       %d\n", len(p.Segs))
	for _, s := range p.Segs {
		fmt.Fprintf(&b, "  %-4s %s\n", s.SegID, s.Text)
	}
	fmt.Print(b.String())
	return nil
}

func cmdProjectRemove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("project rm", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := fs.Arg(0)
	if id == "" {
		return errors.New("usage: vs project rm <id>")
	}
	if err := newClient(*server).DeleteProject(ctx, id); err != nil {
		return err
	}
	fmt.Printf("deleted %s\n", id)
	return nil
}

func cmdProjectBackground(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("project background", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	image := fs.String("image", "", "source image path on the vsd machine")
	anchor := fs.String("anchor", "", "top|center|bottom band to keep when cropping")
	resolution := fs.String("resolution", "1080p", "frame to fit: 720p or 1080p")
	asJSON := fs.Bool("json", false, "print the raw JSON response")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := fs.Arg(0)
	if id == "" {
		return errors.New("usage: vs project background <id> -image FILE")
	}
	if *image == "" {
		return errors.New("-image is required")
	}

	result, err := newClient(*server).SetBackground(ctx, id, backgroundRequest{
		Image: *image, Anchor: *anchor, Resolution: *resolution,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return printJSON(result)
	}
	fmt.Printf("placed %d seg backgrounds at %dx%d (anchor %s)\n",
		len(result.Files), result.Width, result.Height, result.Anchor)
	return nil
}

// readScript loads narration from a file, or from stdin when the path is "-".
// Stdin is what makes the command composable from an agent's shell.
func readScript(path string) (string, error) {
	var (
		body []byte
		err  error
	)
	if path == "-" {
		body, err = io.ReadAll(os.Stdin)
	} else {
		body, err = os.ReadFile(path)
	}
	if err != nil {
		return "", fmt.Errorf("read script: %w", err)
	}
	if strings.TrimSpace(string(body)) == "" {
		return "", errors.New("script is empty")
	}
	return string(body), nil
}

func formatMS(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).Round(time.Millisecond).String()
}
