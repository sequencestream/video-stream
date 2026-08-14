package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Media is what ffprobe reports about one file.
type Media struct {
	Path       string   `json:"path"`
	Format     string   `json:"format,omitempty"`
	DurationMS int64    `json:"duration_ms"`
	SizeBytes  int64    `json:"size_bytes,omitempty"`
	BitrateBPS int64    `json:"bitrate_bps,omitempty"`
	Streams    []Stream `json:"streams,omitempty"`
}

// Stream is one track inside a media file.
type Stream struct {
	Index      int     `json:"index"`
	Type       string  `json:"type"`
	Codec      string  `json:"codec,omitempty"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	FPS        float64 `json:"fps,omitempty"`
	SampleRate int     `json:"sample_rate,omitempty"`
	Channels   int     `json:"channels,omitempty"`
	Language   string  `json:"language,omitempty"`
	DurationMS int64   `json:"duration_ms,omitempty"`
}

// Stream type names as ffprobe reports them.
const (
	StreamVideo    = "video"
	StreamAudio    = "audio"
	StreamSubtitle = "subtitle"
)

// Video returns the first video stream.
func (m Media) Video() (Stream, bool) { return m.firstOfType(StreamVideo) }

// Audio returns the first audio stream.
func (m Media) Audio() (Stream, bool) { return m.firstOfType(StreamAudio) }

// HasAudio reports whether the file carries any audio.
//
// Callers must ask. A filter graph that maps [0:a] against a silent screen
// recording fails with "Stream specifier matches no streams", which is a much
// worse message than "this file has no audio track".
func (m Media) HasAudio() bool { _, ok := m.Audio(); return ok }

// HasVideo reports whether the file carries any video.
func (m Media) HasVideo() bool { _, ok := m.Video(); return ok }

// Subtitles returns every subtitle stream.
func (m Media) Subtitles() []Stream {
	var out []Stream
	for _, s := range m.Streams {
		if s.Type == StreamSubtitle {
			out = append(out, s)
		}
	}
	return out
}

func (m Media) firstOfType(kind string) (Stream, bool) {
	for _, s := range m.Streams {
		if s.Type == kind {
			return s, true
		}
	}
	return Stream{}, false
}

// Resolution renders the video size as WxH, or an empty string.
func (m Media) Resolution() string {
	v, ok := m.Video()
	if !ok || v.Width == 0 {
		return ""
	}
	return fmt.Sprintf("%dx%d", v.Width, v.Height)
}

// Probe runs ffprobe and parses its JSON report.
func (t Tool) Probe(ctx context.Context, path string) (Media, error) {
	if strings.TrimSpace(path) == "" {
		return Media{}, fmt.Errorf("input path is required")
	}
	if info, err := os.Stat(path); err != nil {
		return Media{}, fmt.Errorf("read input: %w", err)
	} else if info.IsDir() {
		return Media{}, fmt.Errorf("%s is a directory, not a media file", path)
	}

	args := []string{
		"-v", "error", "-print_format", "json",
		"-show_format", "-show_streams", path,
	}
	cmd := exec.CommandContext(ctx, t.ffprobeBin(), args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := tail(stderr.String(), 2048); detail != "" {
			return Media{}, fmt.Errorf("probe %s: %w\n%s", path, err, detail)
		}
		return Media{}, fmt.Errorf("probe %s: %w", path, err)
	}

	var raw probeReport
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return Media{}, fmt.Errorf("parse ffprobe output for %s: %w", path, err)
	}
	return raw.toMedia(path), nil
}

type probeReport struct {
	Format struct {
		FormatName string `json:"format_name"`
		Duration   string `json:"duration"`
		Size       string `json:"size"`
		BitRate    string `json:"bit_rate"`
	} `json:"format"`
	Streams []struct {
		Index        int    `json:"index"`
		CodecType    string `json:"codec_type"`
		CodecName    string `json:"codec_name"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		AvgFrameRate string `json:"avg_frame_rate"`
		RFrameRate   string `json:"r_frame_rate"`
		SampleRate   string `json:"sample_rate"`
		Channels     int    `json:"channels"`
		Duration     string `json:"duration"`
		Tags         struct {
			Language string `json:"language"`
		} `json:"tags"`
	} `json:"streams"`
}

func (p probeReport) toMedia(path string) Media {
	m := Media{
		Path:       path,
		Format:     p.Format.FormatName,
		DurationMS: secondsToMS(p.Format.Duration),
		SizeBytes:  parseInt(p.Format.Size),
		BitrateBPS: parseInt(p.Format.BitRate),
	}
	for _, s := range p.Streams {
		stream := Stream{
			Index:      s.Index,
			Type:       s.CodecType,
			Codec:      s.CodecName,
			Width:      s.Width,
			Height:     s.Height,
			FPS:        parseRate(s.AvgFrameRate, s.RFrameRate),
			SampleRate: int(parseInt(s.SampleRate)),
			Channels:   s.Channels,
			Language:   s.Tags.Language,
			DurationMS: secondsToMS(s.Duration),
		}
		m.Streams = append(m.Streams, stream)
		// A container without a duration is common for raw streams; the
		// longest track is the honest answer for how long the file plays.
		if m.DurationMS < stream.DurationMS {
			m.DurationMS = stream.DurationMS
		}
	}
	return m
}

func secondsToMS(s string) int64 {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 {
		return 0
	}
	return int64(f*1000 + 0.5)
}

func parseInt(s string) int64 {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// parseRate reads ffprobe's "num/den" frame rates, preferring the average over
// the nominal one: a variable-frame-rate screen recording reports r_frame_rate
// as something like 1000/1, which is not a frame rate anyone should encode to.
func parseRate(avg, nominal string) float64 {
	for _, candidate := range []string{avg, nominal} {
		num, den, ok := strings.Cut(strings.TrimSpace(candidate), "/")
		if !ok {
			continue
		}
		n, err1 := strconv.ParseFloat(num, 64)
		d, err2 := strconv.ParseFloat(den, 64)
		if err1 != nil || err2 != nil || d == 0 || n == 0 {
			continue
		}
		return n / d
	}
	return 0
}
