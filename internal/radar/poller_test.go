package radar_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/radar"
)

type countingSource struct {
	platform string
	calls    atomic.Int32
	err      error
}

func (s *countingSource) Platform() string { return s.platform }

func (s *countingSource) Recent(ctx context.Context, a radar.Account, since time.Time) ([]radar.Reading, error) {
	s.calls.Add(1)
	if s.err != nil {
		return nil, s.err
	}
	return nil, nil
}

// Twenty accounts at six requests per minute should complete one round without
// hitting a platform's limit. This is the acceptance case for the watch-list
// ceiling: the cap is really a rate budget, not a storage limit.
func TestPollerReadsTwentyAccountsWithinTheConfiguredPace(t *testing.T) {
	source := &countingSource{platform: "douyin"}
	p := radar.NewPoller(600, 1, nil) // ten per second for the test
	p.Register(source)

	accounts := make([]radar.Account, 20)
	for i := range accounts {
		accounts[i] = radar.Account{ID: "a", Platform: "douyin", Handle: "h"}
	}

	result, err := p.Poll(context.Background(), accounts, time.Now().Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if result.Polled != 20 {
		t.Fatalf("polled %d accounts, want 20", result.Polled)
	}
	if got := source.calls.Load(); got != 20 {
		t.Fatalf("source saw %d calls, want 20", got)
	}
}

func TestPollerSkipsAccountsWithNoRegisteredSource(t *testing.T) {
	p := radar.NewPoller(600, 1, nil)
	result, err := p.Poll(context.Background(), []radar.Account{
		{Platform: "douyin", Handle: "a"},
	}, time.Now())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if result.NoSource != 1 || result.Polled != 0 {
		t.Fatalf("got %+v, want one NoSource and zero Polled", result)
	}
}

func TestPollerBacksOffAfterRateLimit(t *testing.T) {
	source := &countingSource{platform: "douyin", err: radar.ErrRateLimited}
	p := radar.NewPoller(600, 1, nil)
	p.Register(source)

	accounts := []radar.Account{
		{Platform: "douyin", Handle: "first"},
		{Platform: "douyin", Handle: "second"},
	}

	result, err := p.Poll(context.Background(), accounts, time.Now())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if result.RateLimited != 1 {
		t.Fatalf("rate limited count = %d, want 1", result.RateLimited)
	}
	if source.calls.Load() != 1 {
		t.Fatalf("source should stop after the first 429, got %d calls", source.calls.Load())
	}
	if result.NoSource != 1 {
		t.Fatalf("second account should be skipped while blocked: %+v", result)
	}
}

func TestPollerTreatsSourceErrorsAsPerAccountFailures(t *testing.T) {
	source := &countingSource{platform: "douyin", err: errors.New("timeout")}
	p := radar.NewPoller(600, 1, nil)
	p.Register(source)

	result, err := p.Poll(context.Background(), []radar.Account{
		{Platform: "douyin", Handle: "a"},
		{Platform: "douyin", Handle: "b"},
	}, time.Now())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if result.Failed != 2 || result.Polled != 0 {
		t.Fatalf("got %+v, want two failures", result)
	}
}
