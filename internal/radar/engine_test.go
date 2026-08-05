package radar_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/radar"
	"github.com/sequencestream/video-stream/internal/store"
)

type memoryStore struct {
	accounts     []store.RadarAccount
	observations []store.RadarObservation
}

func (m *memoryStore) PutAccount(_ context.Context, a store.RadarAccount) error {
	for _, existing := range m.accounts {
		if existing.Platform == a.Platform && existing.Handle == a.Handle {
			return store.ErrAccountExists
		}
	}
	m.accounts = append(m.accounts, a)
	return nil
}

func (m *memoryStore) UpdateAccount(_ context.Context, a store.RadarAccount) error {
	for i, existing := range m.accounts {
		if existing.ID == a.ID {
			m.accounts[i].DisplayName = a.DisplayName
			m.accounts[i].Category = a.Category
			m.accounts[i].Followers = a.Followers
			m.accounts[i].LastPolled = a.LastPolled
			return nil
		}
	}
	return store.ErrAccountNotFound
}

func (m *memoryStore) Accounts(_ context.Context, platform string) ([]store.RadarAccount, error) {
	var out []store.RadarAccount
	for _, a := range m.accounts {
		if platform == "" || a.Platform == platform {
			out = append(out, a)
		}
	}
	return out, nil
}

func (m *memoryStore) Account(_ context.Context, id string) (store.RadarAccount, error) {
	for _, a := range m.accounts {
		if a.ID == id {
			return a, nil
		}
	}
	return store.RadarAccount{}, store.ErrAccountNotFound
}

func (m *memoryStore) AppendObservations(_ context.Context, obs []store.RadarObservation) error {
	m.observations = append(m.observations, obs...)
	return nil
}

func (m *memoryStore) Observations(_ context.Context, accountID string, since time.Time, limit int) ([]store.RadarObservation, error) {
	var out []store.RadarObservation
	for _, o := range m.observations {
		if accountID != "" && o.AccountID != accountID {
			continue
		}
		if !since.IsZero() && o.PublishedAt.Before(since) {
			continue
		}
		out = append(out, o)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func TestImportAccountEnforcesTheWatchListCeiling(t *testing.T) {
	mem := &memoryStore{}
	e := radar.New(radar.Options{Store: mem})

	ctx := context.Background()
	for i := 0; i < radar.MaxAccounts; i++ {
		_, err := e.ImportAccount(ctx, radar.Account{
			Platform: "douyin",
			Handle:   string(rune('a' + i)),
		})
		if err != nil {
			t.Fatalf("import %d: %v", i, err)
		}
	}

	_, err := e.ImportAccount(ctx, radar.Account{Platform: "douyin", Handle: "overflow"})
	if !errors.Is(err, radar.ErrTooManyAccounts) {
		t.Fatalf("got %v, want ErrTooManyAccounts", err)
	}
}

func recentSamples() []radar.Sample {
	samples := cookingCategory()
	published := time.Now().UTC().Add(-7 * 24 * time.Hour)
	observed := published.Add(fixtureAgeHours * time.Hour)
	for i := range samples {
		samples[i].PublishedAt = published
		samples[i].ObservedAt = observed
	}
	return samples
}

func TestSignalsRanksAHotPostAboveAnOrdinaryOne(t *testing.T) {
	mem := &memoryStore{}
	e := radar.New(radar.Options{Store: mem})
	ctx := context.Background()

	acc, err := e.ImportAccount(ctx, radar.Account{
		Platform: "douyin", Handle: "cook", Category: "cooking", Followers: 2_000,
	})
	if err != nil {
		t.Fatalf("ImportAccount: %v", err)
	}

	samples := append(recentSamples(), ordinaryPost("small", 2_000, 0, 0.30))
	for i := range samples {
		if samples[i].PostID == "small" {
			samples[i].PublishedAt = samples[0].PublishedAt
			samples[i].ObservedAt = samples[0].ObservedAt
		}
	}
	observations := make([]store.RadarObservation, 0, len(samples))
	for _, s := range samples {
		observations = append(observations, sampleToObservation(acc.ID, s))
	}
	if err := mem.AppendObservations(ctx, observations); err != nil {
		t.Fatalf("AppendObservations: %v", err)
	}

	signals, err := e.Signals(ctx, radar.Query{})
	if err != nil {
		t.Fatalf("Signals: %v", err)
	}
	if len(signals) == 0 {
		t.Fatal("Signals returned nothing")
	}
	if !signals[0].Residuals.Hot {
		t.Fatalf("top signal should be hot: %+v", signals[0])
	}
	if signals[0].PostID != "small" {
		t.Fatalf("top signal post_id = %q, want small", signals[0].PostID)
	}
}

func sampleToObservation(accountID string, s radar.Sample) store.RadarObservation {
	return store.RadarObservation{
		ID:          s.PostID + "-obs",
		AccountID:   accountID,
		PostID:      s.PostID,
		Title:       s.Title,
		PublishedAt: s.PublishedAt,
		ObservedAt:  s.ObservedAt,
		Views:       s.Views,
		Saves:       s.Saves,
	}
}

func TestIngestReducesCommentsToQuestionCounts(t *testing.T) {
	mem := &memoryStore{}
	e := radar.New(radar.Options{Store: mem})
	ctx := context.Background()

	acc, err := e.ImportAccount(ctx, radar.Account{Platform: "douyin", Handle: "cook"})
	if err != nil {
		t.Fatalf("ImportAccount: %v", err)
	}

	n, err := e.Ingest(ctx, []radar.Reading{{
		AccountID:   acc.ID,
		PostID:      "p1",
		PublishedAt: fixturePublished,
		Views:       1000,
		CommentSample: []radar.Comment{
			{Text: "用的什么锅"},
			{Text: "太好看了"},
		},
	}})
	if err != nil {
		t.Fatalf("Ingest: %v", err)
	}
	if n != 1 {
		t.Fatalf("ingested %d, want 1", n)
	}
	if len(mem.observations) != 1 {
		t.Fatalf("got %d observations", len(mem.observations))
	}
	o := mem.observations[0]
	if o.CommentSamples != 2 || o.UnansweredQuestions != 1 {
		t.Fatalf("got samples=%d unanswered=%d, want 2 and 1", o.CommentSamples, o.UnansweredQuestions)
	}
}
