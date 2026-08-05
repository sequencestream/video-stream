package scriptagents

import (
	"fmt"
	"time"

	"github.com/sequencestream/video-stream/internal/model"
)

// Direction labels a Writer draft's rhetorical stance.
type Direction string

const (
	DirectionQuestion   Direction = "question"
	DirectionStory      Direction = "story"
	DirectionContrarian Direction = "contrarian"
)

// Draft is one Writer output before judging.
type Draft struct {
	ID        string          `json:"id"`
	Direction Direction       `json:"direction"`
	Segs      []model.Seg     `json:"segs"`
	Spike     string          `json:"spike"`
	HookText  string          `json:"hook_text,omitempty"`
	TokensUsed int64          `json:"tokens_used,omitempty"`
}

// Validate checks seg structure and spike presence.
func (d Draft) Validate() error {
	if d.Direction == "" {
		return fmt.Errorf("draft %s: direction must not be empty", d.ID)
	}
	if len(d.Segs) == 0 {
		return fmt.Errorf("draft %s: must have at least one seg", d.ID)
	}
	for _, s := range d.Segs {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("draft %s: %w", d.ID, err)
		}
	}
	if d.Spike != "" && !containsSpike(d, d.Spike) {
		return fmt.Errorf("draft %s: spike %q not found in text", d.ID, d.Spike)
	}
	return nil
}

func containsSpike(d Draft, spike string) bool {
	for _, s := range d.Segs {
		if spikeInText(s.Text, spike) {
			return true
		}
	}
	return false
}

func spikeInText(text, spike string) bool {
	return spike != "" && (text == spike || len(spike) <= len(text) && findSubstring(text, spike))
}

func findSubstring(text, sub string) bool {
	for i := 0; i+len(sub) <= len(text); i++ {
		if text[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ToProject seals segs into a valid project document.
func (d Draft) ToProject(id, title string, profile model.RenderProfile) (model.Project, error) {
	p := model.NewProject(id, title, time.Now().UTC())
	p.RenderProfile = profile
	p.Segs = append([]model.Seg{}, d.Segs...)
	p.Seal()
	if err := p.Validate(); err != nil {
		return model.Project{}, err
	}
	return p, nil
}
