package ffmpeg

import (
	"strconv"
	"strings"
)

// Encode is the output codec configuration shared by every command that has to
// re-encode. Commands that can pass the stream through untouched ignore it.
type Encode struct {
	VideoCodec   string
	AudioCodec   string
	CRF          int
	Preset       string
	AudioBitrate string
}

// DefaultEncode is H.264 at a quality most people cannot distinguish from the
// source, in a container everything plays.
func DefaultEncode() Encode {
	return Encode{
		VideoCodec: "libx264", AudioCodec: "aac",
		CRF: 20, Preset: "medium", AudioBitrate: "192k",
	}
}

// VideoArgs renders the video encoding flags.
//
// yuv420p is forced because it is what browsers, phones and QuickTime can all
// decode. ffmpeg will happily hand a 4:4:4 or 10-bit stream through from an
// exotic source, and the resulting file plays fine on the machine that made it
// and nowhere else.
func (e Encode) VideoArgs() []string {
	codec := firstNonEmpty(e.VideoCodec, "libx264")
	if codec == "copy" {
		return []string{"-c:v", "copy"}
	}
	args := []string{"-c:v", codec}
	if isX26x(codec) {
		args = append(args, "-crf", strconv.Itoa(e.crf()), "-preset", firstNonEmpty(e.Preset, "medium"))
	}
	return append(args, "-pix_fmt", "yuv420p")
}

// AudioArgs renders the audio encoding flags.
func (e Encode) AudioArgs() []string {
	codec := firstNonEmpty(e.AudioCodec, "aac")
	if codec == "copy" {
		return []string{"-c:a", "copy"}
	}
	args := []string{"-c:a", codec}
	if bitrate := strings.TrimSpace(e.AudioBitrate); bitrate != "" {
		args = append(args, "-b:a", bitrate)
	}
	return args
}

func (e Encode) crf() int {
	if e.CRF <= 0 || e.CRF > 51 {
		return 20
	}
	return e.CRF
}

func isX26x(codec string) bool {
	return strings.HasPrefix(codec, "libx264") || strings.HasPrefix(codec, "libx265")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
