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
		resumeFrom, _ := t.Payload["resume_from"].(string)
		runID, _ := t.Payload["run_id"].(string)
		platform, _ := t.Payload["platform"].(string)
		subtitleMode, _ := t.Payload["subtitle_mode"].(string)

		result, err := engine.Run(ctx, render.RunRequest{
			RunID: runID, Project: project, Resolution: res,
			Finalized: finalized, IncludeBGM: includeBGM, ResumeFrom: resumeFrom,
			Platform: platform, SubtitleMode: audio.SubtitleMode(subtitleMode),
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
