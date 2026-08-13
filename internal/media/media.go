// Package media fits a source image to the render frame and files it where the
// render pipeline looks for it.
//
// The render pipeline reads local assets from <MediaDir>/<project>/<seg>, and
// it scales whatever it finds to cover the frame, cropping the overflow. That
// crop is the problem this package exists for: a 4:3 poster placed in a 16:9
// frame loses a band off the top and the bottom, and composed text lives
// exactly there. Choosing the anchor before the render — rather than
// discovering the headline was cut after a 40-second pipeline run — is the
// whole point.
package media

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Anchor selects which band of an over-tall source survives the crop.
type Anchor string

const (
	// AnchorCenter keeps the middle and is right for photographs.
	AnchorCenter Anchor = "center"
	// AnchorTop keeps the top, which is where posters put their headline.
	AnchorTop Anchor = "top"
	// AnchorBottom keeps the bottom.
	AnchorBottom Anchor = "bottom"
)

// Valid reports whether the anchor is a known value.
func (a Anchor) Valid() bool {
	switch a {
	case AnchorCenter, AnchorTop, AnchorBottom:
		return true
	default:
		return false
	}
}

// ErrNoSegs means the request named no segs to file the asset under.
var ErrNoSegs = errors.New("no seg ids to place the background under")

// Request is one background placement.
type Request struct {
	ProjectID string
	SegIDs    []string
	// Source is the image to fit. It is read, never modified.
	Source string
	Width  int
	Height int
	Anchor Anchor
}

// Result reports what was written.
type Result struct {
	ProjectID string   `json:"project_id"`
	Files     []string `json:"files"`
	Width     int      `json:"width"`
	Height    int      `json:"height"`
	Anchor    Anchor   `json:"anchor"`
}

// Preparer fits and files background images.
type Preparer struct {
	// MediaDir is the root the render pipeline reads assets from.
	MediaDir string
	// FFmpegBinary defaults to ffmpeg on PATH.
	FFmpegBinary string
}

// PlaceBackground fits Source to the frame once and copies it to every seg.
//
// Every seg gets its own file rather than a shared one because the pipeline
// resolves assets per seg: one file per seg is what lets a later edit swap the
// visual for a single line without touching the rest.
func (p Preparer) PlaceBackground(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return Result{}, errors.New("project id is required")
	}
	if len(req.SegIDs) == 0 {
		return Result{}, ErrNoSegs
	}
	if strings.TrimSpace(req.Source) == "" {
		return Result{}, errors.New("source image is required")
	}
	if req.Width <= 0 || req.Height <= 0 {
		return Result{}, fmt.Errorf("frame size %dx%d is not usable", req.Width, req.Height)
	}
	anchor := req.Anchor
	if anchor == "" {
		anchor = AnchorCenter
	}
	if !anchor.Valid() {
		return Result{}, fmt.Errorf("unknown anchor %q", anchor)
	}
	if info, err := os.Stat(req.Source); err != nil {
		return Result{}, fmt.Errorf("read source image: %w", err)
	} else if !info.Mode().IsRegular() {
		return Result{}, fmt.Errorf("source %s is not a regular file", req.Source)
	}
	if strings.TrimSpace(p.MediaDir) == "" {
		return Result{}, errors.New("media dir is not configured")
	}

	dir := filepath.Join(p.MediaDir, req.ProjectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Result{}, err
	}

	// Fit once, then copy: re-encoding per seg would burn CPU to produce
	// byte-identical files.
	fitted := filepath.Join(dir, ".background.jpg")
	if err := p.fit(ctx, req.Source, fitted, req.Width, req.Height, anchor); err != nil {
		return Result{}, err
	}
	defer os.Remove(fitted)

	payload, err := os.ReadFile(fitted)
	if err != nil {
		return Result{}, err
	}

	files := make([]string, 0, len(req.SegIDs))
	for _, segID := range req.SegIDs {
		if strings.TrimSpace(segID) == "" {
			return Result{}, errors.New("seg id must not be empty")
		}
		path := filepath.Join(dir, segID+".jpg")
		if err := os.WriteFile(path, payload, 0o644); err != nil {
			return Result{}, err
		}
		files = append(files, path)
	}

	return Result{ProjectID: req.ProjectID, Files: files, Width: req.Width, Height: req.Height, Anchor: anchor}, nil
}

// fit scales the source to cover the frame and crops the overflow at the anchor.
func (p Preparer) fit(ctx context.Context, source, output string, width, height int, anchor Anchor) error {
	binary := p.FFmpegBinary
	if strings.TrimSpace(binary) == "" {
		binary = "ffmpeg"
	}
	filter := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d:%s:%s",
		width, height, width, height, cropX(anchor), cropY(anchor))

	cmd := exec.CommandContext(ctx, binary, "-hide_banner", "-loglevel", "error",
		"-i", source, "-vf", filter, "-frames:v", "1", "-q:v", "2", "-y", output)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("fit background: %w: %s", err, detail)
		}
		return fmt.Errorf("fit background: %w", err)
	}
	return nil
}

// cropX always centres horizontally: the anchor exists for the vertical band,
// where composed text lives.
func cropX(Anchor) string { return "(in_w-out_w)/2" }

func cropY(anchor Anchor) string {
	switch anchor {
	case AnchorTop:
		return "0"
	case AnchorBottom:
		return "in_h-out_h"
	default:
		return "(in_h-out_h)/2"
	}
}
