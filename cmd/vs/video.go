package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

// renderTimeout covers a full pipeline run: visuals, TTS, subtitles, loudness,
// mux, and the FFprobe readback that has to decode the result.
const renderTimeout = 30 * time.Minute

// cmdVideo runs script → project → background → render in one call.
//
// The steps exist separately too, and an author iterating on a script wants
// them separately. This exists for the other caller: an agent that has a title,
// a script, and an image, and wants a file path back without holding state
// between three invocations.
func cmdVideo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("video", flag.ContinueOnError)
	server := fs.String("server", defaultServer(), "main service base URL")
	title := fs.String("title", "", "video title")
	script := fs.String("script", "", "path to the narration text, or - for stdin")
	image := fs.String("image", "", "background image path on the vsd machine")
	anchor := fs.String("anchor", "", "top|center|bottom band to keep when cropping")
	voice := fs.String("voice", "", "TTS voice")
	id := fs.String("id", "", "explicit project id")
	resolution := fs.String("resolution", "720p", "target resolution: 720p or 1080p")
	platform := fs.String("platform", "douyin", "target platform; selects subtitle mode and loudness")
	subtitleMode := fs.String("subtitle-mode", "", "burn_in or soft; defaults to the platform preference")
	stillImages := fs.Bool("still-images", true, "hold background images steady instead of applying Ken Burns")
	asJSON := fs.Bool("json", false, "print the raw JSON result")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *title == "" || *script == "" {
		return errors.New("usage: vs video -title T -script FILE [-image IMG]")
	}

	body, err := readScript(*script)
	if err != nil {
		return err
	}

	client := newClient(*server)
	imported, err := client.withTimeout(importTimeout).CreateProject(ctx, createProjectRequest{
		ProjectID: *id, Title: *title, Script: body, Voice: *voice,
	})
	if err != nil {
		return err
	}
	projectID := imported.Project.ID

	var background backgroundResult
	if *image != "" {
		background, err = client.SetBackground(ctx, projectID, backgroundRequest{
			Image: *image, Anchor: *anchor, Resolution: *resolution,
		})
		if err != nil {
			return err
		}
	}

	rendered, err := runRenderTask(ctx, client, renderOptions{
		ProjectID: projectID, Resolution: *resolution, Platform: *platform,
		SubtitleMode: *subtitleMode, StillImages: *stillImages, Wait: true,
	})
	if err != nil {
		return err
	}

	output, _ := rendered.Result["output_uri"].(string)
	if *asJSON {
		return printJSON(map[string]any{
			"project_id":  projectID,
			"seg_count":   imported.SegCount,
			"total_ms":    imported.TotalMS,
			"backgrounds": len(background.Files),
			"task":        rendered,
			"output_uri":  output,
		})
	}

	var b strings.Builder
	fmt.Fprintf(&b, "project id %s\n", projectID)
	fmt.Fprintf(&b, "segs       %d\n", imported.SegCount)
	fmt.Fprintf(&b, "duration   %s\n", formatMS(imported.TotalMS))
	if *image != "" {
		fmt.Fprintf(&b, "background %d segs at %dx%d (anchor %s)\n",
			len(background.Files), background.Width, background.Height, background.Anchor)
	}
	fmt.Fprintf(&b, "status     %s\n", rendered.Status)
	if rendered.Error != "" {
		fmt.Fprintf(&b, "error      %s\n", rendered.Error)
	}
	if output != "" {
		fmt.Fprintf(&b, "output     %s\n", output)
	}
	fmt.Print(b.String())

	if rendered.Status != "succeeded" {
		return fmt.Errorf("render task %s %s", rendered.ID, rendered.Status)
	}
	return nil
}

// renderOptions is the payload shape shared by `vs render` and `vs video`.
type renderOptions struct {
	ProjectID    string
	Resolution   string
	Platform     string
	SubtitleMode string
	StillImages  bool
	Finalized    bool
	Wait         bool
	Timeout      time.Duration
}

func runRenderTask(ctx context.Context, c *client, opts renderOptions) (task, error) {
	payload := map[string]any{
		"project":      opts.ProjectID,
		"resolution":   opts.Resolution,
		"still_images": opts.StillImages,
	}
	if opts.Platform != "" {
		payload["platform"] = opts.Platform
	}
	if opts.SubtitleMode != "" {
		payload["subtitle_mode"] = opts.SubtitleMode
	}
	if opts.Finalized {
		payload["finalized"] = true
	}

	created, err := c.CreateTask(ctx, createTaskRequest{
		Type: "render", Title: opts.ProjectID, Payload: payload,
	})
	if err != nil {
		return task{}, err
	}
	if !opts.Wait {
		return created, nil
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = renderTimeout
	}
	return c.WaitForTask(ctx, created.ID, timeout)
}

func printJSON(v any) error {
	return writeJSON(os.Stdout, v)
}
