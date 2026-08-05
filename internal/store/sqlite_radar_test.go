package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/store"
)

func TestPutAccountRoundTripsEveryField(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	want := store.RadarAccount{
		ID:          "acc-1",
		Platform:    "douyin",
		Handle:      "cook_daily",
		DisplayName: "每日下厨",
		Category:    "cooking",
		Followers:   12_000,
		Owned:       true,
		AddedAt:     time.UnixMilli(1_700_000_000_000).UTC(),
		LastPolled:  time.UnixMilli(1_700_000_600_000).UTC(),
	}
	if err := s.PutAccount(ctx, want); err != nil {
		t.Fatalf("PutAccount: %v", err)
	}

	got, err := s.Account(ctx, "acc-1")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if got != want {
		t.Fatalf("round trip changed the account:\n got %+v\nwant %+v", got, want)
	}
}

func TestAccountReportsAMissingID(t *testing.T) {
	if _, err := openStore(t).Account(context.Background(), "nope"); !errors.Is(err, store.ErrAccountNotFound) {
		t.Fatalf("got %v, want ErrAccountNotFound", err)
	}
}

// Re-importing is how a user corrects a typo, so it has to be told apart from
// a generic database failure. Silently upserting would also reset the follower
// count and poll cursor the radar has since gathered.
func TestPutAccountRejectsTheSameHandleTwiceOnOnePlatform(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	first := store.RadarAccount{ID: "acc-1", Platform: "douyin", Handle: "cook_daily", AddedAt: time.UnixMilli(1)}
	if err := s.PutAccount(ctx, first); err != nil {
		t.Fatalf("PutAccount first: %v", err)
	}
	second := store.RadarAccount{ID: "acc-2", Platform: "douyin", Handle: "cook_daily", AddedAt: time.UnixMilli(2)}
	if err := s.PutAccount(ctx, second); !errors.Is(err, store.ErrAccountExists) {
		t.Fatalf("got %v, want ErrAccountExists", err)
	}
}

// The same creator name on two platforms is two accounts with two audiences,
// and two baselines. A UNIQUE on handle alone would refuse the second one.
func TestPutAccountAllowsTheSameHandleOnADifferentPlatform(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	if err := s.PutAccount(ctx, store.RadarAccount{ID: "acc-1", Platform: "douyin", Handle: "cook_daily", AddedAt: time.UnixMilli(1)}); err != nil {
		t.Fatalf("PutAccount douyin: %v", err)
	}
	if err := s.PutAccount(ctx, store.RadarAccount{ID: "acc-2", Platform: "xiaohongshu", Handle: "cook_daily", AddedAt: time.UnixMilli(2)}); err != nil {
		t.Fatalf("PutAccount xiaohongshu: %v", err)
	}

	accounts, err := s.Accounts(ctx, "")
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(accounts) != 2 {
		t.Fatalf("got %d accounts, want 2", len(accounts))
	}
}

func TestAccountsFiltersByPlatformAndOrdersOldestFirst(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	seed := []store.RadarAccount{
		{ID: "b", Platform: "douyin", Handle: "second", AddedAt: time.UnixMilli(200)},
		{ID: "a", Platform: "douyin", Handle: "first", AddedAt: time.UnixMilli(100)},
		{ID: "c", Platform: "bilibili", Handle: "third", AddedAt: time.UnixMilli(300)},
	}
	for _, a := range seed {
		if err := s.PutAccount(ctx, a); err != nil {
			t.Fatalf("PutAccount %s: %v", a.ID, err)
		}
	}

	got, err := s.Accounts(ctx, "douyin")
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" {
		t.Fatalf("got %v, want [a b] in that order", ids(got))
	}
}

func TestUpdateAccountRefreshesFollowersWithoutTouchingTheImport(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	original := store.RadarAccount{
		ID: "acc-1", Platform: "douyin", Handle: "cook_daily",
		Followers: 12_000, AddedAt: time.UnixMilli(100),
	}
	if err := s.PutAccount(ctx, original); err != nil {
		t.Fatalf("PutAccount: %v", err)
	}

	original.Followers = 30_000
	original.DisplayName = "每日下厨"
	original.LastPolled = time.UnixMilli(500).UTC()
	if err := s.UpdateAccount(ctx, original); err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}

	got, err := s.Account(ctx, "acc-1")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if got.Followers != 30_000 || got.DisplayName != "每日下厨" {
		t.Fatalf("update did not apply: %+v", got)
	}
	// Platform, handle and added_at are import facts; a refresh must not be
	// able to move an account to a different platform.
	if got.Platform != "douyin" || got.Handle != "cook_daily" || !got.AddedAt.Equal(time.UnixMilli(100).UTC()) {
		t.Fatalf("update touched an import field: %+v", got)
	}
}

func TestUpdateAccountReportsAMissingID(t *testing.T) {
	err := openStore(t).UpdateAccount(context.Background(), store.RadarAccount{ID: "nope"})
	if !errors.Is(err, store.ErrAccountNotFound) {
		t.Fatalf("got %v, want ErrAccountNotFound", err)
	}
}

// The second derivative of the save rate needs a series. If a second reading of
// the same post replaced the first, there would be nothing to differentiate,
// so this table must append where the artifacts table replaces.
func TestAppendObservationsKeepsEveryReadingOfOnePost(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seedAccount(t, s, "acc-1")

	published := time.UnixMilli(1_700_000_000_000).UTC()
	obs := []store.RadarObservation{
		{ID: "o1", AccountID: "acc-1", PostID: "p1", PublishedAt: published, ObservedAt: published.Add(time.Hour), Views: 1000, Saves: 10},
		{ID: "o2", AccountID: "acc-1", PostID: "p1", PublishedAt: published, ObservedAt: published.Add(2 * time.Hour), Views: 2500, Saves: 40},
		{ID: "o3", AccountID: "acc-1", PostID: "p1", PublishedAt: published, ObservedAt: published.Add(3 * time.Hour), Views: 4000, Saves: 90},
	}
	if err := s.AppendObservations(ctx, obs); err != nil {
		t.Fatalf("AppendObservations: %v", err)
	}

	got, err := s.Observations(ctx, "acc-1", time.Time{}, 0)
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d observations, want 3", len(got))
	}
	// Newest first, so the last reading leads.
	if got[0].ID != "o3" || got[2].ID != "o1" {
		t.Fatalf("got order %v, want [o3 o2 o1]", observationIDs(got))
	}
}

func TestAppendObservationsRoundTripsEveryField(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seedAccount(t, s, "acc-1")

	want := store.RadarObservation{
		ID: "o1", AccountID: "acc-1", PostID: "p1", Title: "三分钟番茄炒蛋",
		DurationSeconds: 180,
		PublishedAt:     time.UnixMilli(1_700_000_000_000).UTC(),
		ObservedAt:      time.UnixMilli(1_700_000_600_000).UTC(),
		Views:           98_000, Likes: 4_100, Comments: 320, Shares: 210, Saves: 6_400,
		CompletionRate: 0.62, CommentSamples: 100, UnansweredQuestions: 31,
	}
	if err := s.AppendObservations(ctx, []store.RadarObservation{want}); err != nil {
		t.Fatalf("AppendObservations: %v", err)
	}

	got, err := s.Observations(ctx, "", time.Time{}, 0)
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("round trip changed the observation:\n got %+v\nwant %+v", got, want)
	}
}

// A count above the sample size is not a large number, it is a broken
// denominator, and it would report a density above 1.
func TestAppendObservationsRejectsMoreQuestionsThanCommentsSampled(t *testing.T) {
	s := openStore(t)
	seedAccount(t, s, "acc-1")

	err := s.AppendObservations(context.Background(), []store.RadarObservation{{
		ID: "o1", AccountID: "acc-1", PostID: "p1",
		PublishedAt:    time.UnixMilli(1),
		CommentSamples: 10, UnansweredQuestions: 11,
	}})
	if err == nil {
		t.Fatal("AppendObservations accepted more questions than comments sampled")
	}
}

// A half-written round leaves a hole in the series, and the second derivative
// reads a hole as collapsing momentum rather than as missing data.
func TestAppendObservationsWritesNothingWhenOneRowIsInvalid(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seedAccount(t, s, "acc-1")

	published := time.UnixMilli(1_700_000_000_000).UTC()
	err := s.AppendObservations(ctx, []store.RadarObservation{
		{ID: "o1", AccountID: "acc-1", PostID: "p1", PublishedAt: published},
		{ID: "o2", AccountID: "acc-1", PostID: "p2", PublishedAt: published, CompletionRate: 1.5},
	})
	if err == nil {
		t.Fatal("AppendObservations accepted a completion rate above 1")
	}

	got, err := s.Observations(ctx, "acc-1", time.Time{}, 0)
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d rows written, want 0 after a rejected batch", len(got))
	}
}

func TestObservationsDropsPostsPublishedBeforeTheWindow(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seedAccount(t, s, "acc-1")

	now := time.UnixMilli(1_700_000_000_000).UTC()
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-3 * 24 * time.Hour)
	if err := s.AppendObservations(ctx, []store.RadarObservation{
		{ID: "o-old", AccountID: "acc-1", PostID: "p-old", PublishedAt: old, ObservedAt: now},
		{ID: "o-new", AccountID: "acc-1", PostID: "p-new", PublishedAt: recent, ObservedAt: now},
	}); err != nil {
		t.Fatalf("AppendObservations: %v", err)
	}

	got, err := s.Observations(ctx, "", now.Add(-30*24*time.Hour), 0)
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}
	if len(got) != 1 || got[0].ID != "o-new" {
		t.Fatalf("got %v, want only o-new", observationIDs(got))
	}
}

func seedAccount(t *testing.T, s *store.SQLiteStore, id string) {
	t.Helper()
	err := s.PutAccount(context.Background(), store.RadarAccount{
		ID: id, Platform: "douyin", Handle: id, Followers: 10_000, AddedAt: time.UnixMilli(1),
	})
	if err != nil {
		t.Fatalf("seed account %s: %v", id, err)
	}
}

func ids(accounts []store.RadarAccount) []string {
	out := make([]string, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, a.ID)
	}
	return out
}

func observationIDs(obs []store.RadarObservation) []string {
	out := make([]string, 0, len(obs))
	for _, o := range obs {
		out = append(out, o.ID)
	}
	return out
}
