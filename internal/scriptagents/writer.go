package scriptagents

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
)

// Writer produces parallel heterogeneous drafts.
type Writer interface {
	Write(ctx context.Context, req WriteRequest) ([]Draft, error)
}

// WriteRequest is the brief handed to the Writer.
type WriteRequest struct {
	Topic       string
	UserQuotes  []string
	Spike       string
	RenderVoice string
}

// RuleWriter is a deterministic Writer for tests and offline fixtures.
type RuleWriter struct{}

// Write implements Writer with three direction-distinct drafts.
func (RuleWriter) Write(_ context.Context, req WriteRequest) ([]Draft, error) {
	spike := req.Spike
	if spike == "" {
		spike = "nobody talks about this"
	}
	topic := strings.TrimSpace(req.Topic)
	if topic == "" {
		topic = "the topic"
	}

	profile := model.RenderProfile{Voice: req.RenderVoice}
	_ = profile

	type spec struct {
		dir   Direction
		hook  string
		body  string
		emo   model.EmotionTag
	}
	specs := []spec{
		{DirectionQuestion, fmt.Sprintf("What if everything you know about %s is wrong?", topic),
			fmt.Sprintf("Most creators miss the one lever that actually moves %s. %s.", topic, spike), model.EmotionUrgent},
		{DirectionStory, fmt.Sprintf("Three years ago I ignored %s completely.", topic),
			fmt.Sprintf("Then one afternoon changed how I think about it. %s — and that detail still matters.", spike), model.EmotionWarm},
		{DirectionContrarian, fmt.Sprintf("Stop optimizing %s the way everyone says.", topic),
			fmt.Sprintf("The conventional playbook hides a sharper edge. %s is the part they skip.", spike), model.EmotionSerious},
	}

	drafts := make([]Draft, 0, WriterCount)
	for _, s := range specs {
		segHook := model.NewSeg("hook", s.hook, 3000)
		segHook.EmotionTag = s.emo
		segBody := model.NewSeg("body", s.body, 8000)
		segBody.EmotionTag = model.EmotionNeutral
		segBody.DependsOn = []string{"hook"}

		drafts = append(drafts, Draft{
			ID:         newDraftID(),
			Direction:  s.dir,
			Segs:       []model.Seg{segHook, segBody},
			Spike:      spike,
			HookText:   s.hook,
			TokensUsed: 120,
		})
	}
	return drafts, nil
}

func newDraftID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// nowUTC is a test seam.
var nowUTC = func() time.Time { return time.Now().UTC() }
