package radar

import (
	"testing"
	"time"
)

func TestLimiterSpacesRequestsAtTheConfiguredRate(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(60, 1)
	l.now = func() time.Time { return now }
	l.last = now

	if delay := l.reserve(); delay != 0 {
		t.Fatalf("first reserve delay = %s, want 0", delay)
	}
	if delay := l.reserve(); delay != time.Second {
		t.Fatalf("second reserve delay = %s, want 1s at 60/min", delay)
	}
}

func TestLimiterAllowsABurstAfterIdle(t *testing.T) {
	now := time.Unix(0, 0)
	l := NewLimiter(60, 3)
	l.now = func() time.Time { return now }
	l.last = now

	for i := 0; i < 3; i++ {
		if delay := l.reserve(); delay != 0 {
			t.Fatalf("burst grant %d delay = %s, want 0", i, delay)
		}
	}
	if delay := l.reserve(); delay != time.Second {
		t.Fatalf("after burst, delay = %s, want 1s", delay)
	}
}

func TestLimiterWithZeroRateIsUnlimited(t *testing.T) {
	l := NewLimiter(0, 1)
	for i := 0; i < 100; i++ {
		if delay := l.reserve(); delay != 0 {
			t.Fatalf("grant %d delay = %s, want 0", i, delay)
		}
	}
}
