package render

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/audio"
)

const (
	DefaultBGMBPM    = 120.0
	DefaultBGMGainDB = -18.0
)

// BGMConfig describes a local music track and its beat grid. BeatOffsetMS is
// the location of a known downbeat in the source track. The mixer shifts that
// grid to best fit the locked segment cuts; it never changes the final edit.
type BGMConfig struct {
	URI          string  `json:"uri"`
	BPM          float64 `json:"bpm,omitempty"`
	BeatOffsetMS int64   `json:"beat_offset_ms,omitempty"`
	GainDB       float64 `json:"gain_db,omitempty"`
}

func (c BGMConfig) withDefaults() BGMConfig {
	if c.BPM == 0 {
		c.BPM = DefaultBGMBPM
	}
	if c.GainDB == 0 {
		c.GainDB = DefaultBGMGainDB
	}
	return c
}

func (c BGMConfig) Validate() error {
	c = c.withDefaults()
	if strings.TrimSpace(c.URI) == "" {
		return errors.New("BGM uri is required")
	}
	if c.BPM < 40 || c.BPM > 240 {
		return fmt.Errorf("BGM bpm %.2f must be between 40 and 240", c.BPM)
	}
	if c.BeatOffsetMS < 0 {
		return errors.New("BGM beat offset must not be negative")
	}
	if c.GainDB < -60 || c.GainDB > 0 {
		return fmt.Errorf("BGM gain %.2f dB must be between -60 and 0", c.GainDB)
	}
	return nil
}

type BGMMixPlan struct {
	SpeechPath    string
	OutputPath    string
	Config        BGMConfig
	CutPoints     []time.Duration
	TotalDuration time.Duration
	TargetLUFS    float64
	ToleranceLUFS float64
}

type BGMMixResult struct {
	TimelineBeatPhase time.Duration
	SourceStart       time.Duration
	OutputLUFS        float64
}

type BGMMixer interface {
	Mix(context.Context, BGMMixPlan) (BGMMixResult, error)
}

// ExecBGMMixer loops and phase-aligns music, ducks it under speech, mixes both
// tracks, then re-normalizes and verifies the delivered mix.
type ExecBGMMixer struct{ Binary string }

func (m ExecBGMMixer) Mix(ctx context.Context, plan BGMMixPlan) (BGMMixResult, error) {
	if err := ctx.Err(); err != nil {
		return BGMMixResult{}, err
	}
	plan.Config = plan.Config.withDefaults()
	if err := plan.Config.Validate(); err != nil {
		return BGMMixResult{}, err
	}
	if plan.TotalDuration <= 0 {
		return BGMMixResult{}, errors.New("BGM mix duration must be positive")
	}
	for name, path := range map[string]string{"speech": plan.SpeechPath, "BGM": plan.Config.URI} {
		info, err := os.Stat(path)
		if err != nil {
			return BGMMixResult{}, fmt.Errorf("stat %s input: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return BGMMixResult{}, fmt.Errorf("%s input %s is not a regular file", name, path)
		}
	}
	if strings.TrimSpace(plan.OutputPath) == "" {
		return BGMMixResult{}, errors.New("BGM output path is required")
	}

	period := time.Duration(float64(time.Minute) / plan.Config.BPM)
	phase := bestBeatPhase(plan.CutPoints, period)
	knownBeat := time.Duration(plan.Config.BeatOffsetMS) * time.Millisecond
	sourceStart := positiveModulo(knownBeat-phase, period)

	outDir := filepath.Dir(plan.OutputPath)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return BGMMixResult{}, err
	}
	raw, err := os.CreateTemp(outDir, ".bgm-mix-*.wav")
	if err != nil {
		return BGMMixResult{}, err
	}
	rawPath := raw.Name()
	if err := raw.Close(); err != nil {
		os.Remove(rawPath)
		return BGMMixResult{}, err
	}
	if err := os.Remove(rawPath); err != nil {
		return BGMMixResult{}, err
	}
	defer os.Remove(rawPath)

	binary := strings.TrimSpace(m.Binary)
	if binary == "" {
		binary = "ffmpeg"
	}
	duration := formatFFmpegDuration(plan.TotalDuration)
	filter := fmt.Sprintf(
		"[0:a:0]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=mono,apad=whole_dur=%s,atrim=duration=%s,asplit=2[speech][sidechain];"+
			"[1:a:0]aresample=48000,aformat=sample_fmts=fltp:channel_layouts=mono,volume=%sdB[music];"+
			"[music][sidechain]sidechaincompress=threshold=0.03:ratio=8:attack=20:release=250[ducked];"+
			"[speech][ducked]amix=inputs=2:duration=first:normalize=0,atrim=duration=%s,asetpts=PTS-STARTPTS[mix]",
		duration, duration, strconv.FormatFloat(plan.Config.GainDB, 'f', 2, 64), duration,
	)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", plan.SpeechPath, "-stream_loop", "-1", "-ss", formatFFmpegDuration(sourceStart), "-i", plan.Config.URI,
		"-filter_complex", filter, "-map", "[mix]", "-t", duration,
		"-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", rawPath,
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return BGMMixResult{}, ctxErr
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 8*1024 {
			message = message[len(message)-8*1024:]
		}
		return BGMMixResult{}, fmt.Errorf("ffmpeg BGM mix: %w: %s", err, message)
	}

	loudness, err := audio.NormalizeFileLUFS(ctx, binary, rawPath, plan.OutputPath, plan.TargetLUFS, plan.ToleranceLUFS)
	if err != nil {
		return BGMMixResult{}, fmt.Errorf("normalize BGM mix: %w", err)
	}
	return BGMMixResult{TimelineBeatPhase: phase, SourceStart: sourceStart, OutputLUFS: loudness.OutputLUFS}, nil
}

// bestBeatPhase chooses the beat-grid phase with the smallest squared distance
// to all locked edit points. Millisecond search is cheap (a beat is <= 1.5s)
// and deterministic across platforms.
func bestBeatPhase(cuts []time.Duration, period time.Duration) time.Duration {
	if period <= 0 || len(cuts) == 0 {
		return 0
	}
	periodMS := period.Milliseconds()
	bestMS := int64(0)
	bestScore := math.Inf(1)
	for phaseMS := int64(0); phaseMS < periodMS; phaseMS++ {
		score := 0.0
		for _, cut := range cuts {
			delta := positiveModulo(cut-time.Duration(phaseMS)*time.Millisecond, period)
			d := math.Min(float64(delta), float64(period-delta)) / float64(time.Millisecond)
			score += d * d
		}
		if score < bestScore {
			bestScore, bestMS = score, phaseMS
		}
	}
	return time.Duration(bestMS) * time.Millisecond
}

func positiveModulo(value, modulus time.Duration) time.Duration {
	if modulus <= 0 {
		return 0
	}
	value %= modulus
	if value < 0 {
		value += modulus
	}
	return value
}

type StubBGMMixer struct{}

func (StubBGMMixer) Mix(_ context.Context, plan BGMMixPlan) (BGMMixResult, error) {
	if err := plan.Config.withDefaults().Validate(); err != nil {
		return BGMMixResult{}, err
	}
	if err := writeStubFile(plan.OutputPath, "bgm-mix"); err != nil {
		return BGMMixResult{}, err
	}
	period := time.Duration(float64(time.Minute) / plan.Config.withDefaults().BPM)
	return BGMMixResult{TimelineBeatPhase: bestBeatPhase(plan.CutPoints, period), OutputLUFS: plan.TargetLUFS}, nil
}
