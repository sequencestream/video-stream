package tasks

import (
	"context"
	"errors"
	"fmt"

	"github.com/sequencestream/video-stream/internal/audio"
	"github.com/sequencestream/video-stream/internal/queue"
	"github.com/sequencestream/video-stream/internal/render"
	"github.com/sequencestream/video-stream/internal/store"
)

// RenderHandler runs the staged FFmpeg pipeline for a stored project.
func RenderHandler(engine *render.Engine, projects store.ProjectStore) queue.Handler {
	return func(ctx context.Context, t store.Task) (map[string]any, error) {
		if engine == nil {
			return nil, errors.New("render engine is not configured")
		}
		if projects == nil {
			return nil, errors.New("project store is not configured")
		}

		projectID, _ := t.Payload["project"].(string)
		if projectID == "" {
			return nil, errors.New("render task requires \"project\" (project id) in its payload")
		}
		project, err := projects.GetProject(ctx, projectID)
		if err != nil {
			return nil, fmt.Errorf("load project %s: %w", projectID, err)
		}

		res := render.Resolution1080p
		if raw, _ := t.Payload["resolution"].(string); raw != "" {
			res = render.Resolution(raw)
		}
		finalized, _ := t.Payload["finalized"].(bool)
		includeBGM, _ := t.Payload["include_bgm"].(bool)
		bgm := render.BGMConfig{}
		if raw, ok := t.Payload["bgm"].(map[string]any); ok {
			bgm.URI, _ = raw["uri"].(string)
			bgm.BPM, _ = raw["bpm"].(float64)
			if n, ok := raw["beat_offset_ms"].(float64); ok {
				bgm.BeatOffsetMS = int64(n)
			}
			bgm.GainDB, _ = raw["gain_db"].(float64)
		}
		resumeFrom, _ := t.Payload["resume_from"].(string)
		runID, _ := t.Payload["run_id"].(string)
		platform, _ := t.Payload["platform"].(string)
		subtitleMode, _ := t.Payload["subtitle_mode"].(string)
		stillImages, _ := t.Payload["still_images"].(bool)

		result, err := engine.Run(ctx, render.RunRequest{
			RunID: runID, Project: project, Resolution: res,
			Finalized: finalized, IncludeBGM: includeBGM || bgm.URI != "", BGM: bgm, ResumeFrom: resumeFrom,
			Platform: platform, SubtitleMode: audio.SubtitleMode(subtitleMode), StillImages: stillImages,
		})
		if err != nil {
			return nil, err
		}

		return map[string]any{
			"run_id":           result.RunID,
			"project_id":       result.ProjectID,
			"resolution":       result.Resolution,
			"output_uri":       result.OutputURI,
			"completed_stages": result.CompletedStages,
			"seg_artifacts":    len(result.SegArtifacts),
		}, nil
	}
}
