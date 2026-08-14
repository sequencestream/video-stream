package timespan

import (
	"testing"
	"time"
)

func TestNormalizeMergesOverlapsAndTouchingRanges(t *testing.T) {
	got := Ranges{
		{StartMS: 500, EndMS: 900},
		{StartMS: 0, EndMS: 400},
		{StartMS: 300, EndMS: 600}, // overlaps both neighbours
		{StartMS: 2000, EndMS: 2000},
		{StartMS: 900, EndMS: 1200}, // touches the first exactly
	}.Normalize()

	want := Ranges{{StartMS: 0, EndMS: 1200}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestInvertIsTheComplement(t *testing.T) {
	cuts := Ranges{{StartMS: 1000, EndMS: 2000}, {StartMS: 5000, EndMS: 6000}}
	got := cuts.Invert(8000)
	want := Ranges{{StartMS: 0, EndMS: 1000}, {StartMS: 2000, EndMS: 5000}, {StartMS: 6000, EndMS: 8000}}

	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if total := got.Total() + cuts.Total(); total != 8000 {
		t.Fatalf("keep + cut = %d want the whole 8000ms accounted for", total)
	}
}

// A cut that starts at zero or runs to the end must not produce an empty
// leading or trailing range: an empty span in a concat list is a filter error.
func TestInvertAtTheEdges(t *testing.T) {
	got := Ranges{{StartMS: 0, EndMS: 1000}, {StartMS: 4000, EndMS: 5000}}.Invert(5000)
	if len(got) != 1 || got[0] != (Range{StartMS: 1000, EndMS: 4000}) {
		t.Fatalf("got %v want one range 1000-4000", got)
	}
}

func TestShrinkNarrowsAndDropsCollapsedRanges(t *testing.T) {
	got := Ranges{
		{StartMS: 1000, EndMS: 2000}, // survives
		{StartMS: 3000, EndMS: 3100}, // 100ms shrunk by 200ms: gone
	}.Shrink(100*time.Millisecond, 100*time.Millisecond)

	if len(got) != 1 {
		t.Fatalf("got %v want only the range wide enough to survive", got)
	}
	if got[0] != (Range{StartMS: 1100, EndMS: 1900}) {
		t.Fatalf("got %v want 1100-1900", got[0])
	}
}

func TestPadClampsToTheMedia(t *testing.T) {
	got := Ranges{{StartMS: 50, EndMS: 4900}}.Pad(200*time.Millisecond, 200*time.Millisecond, 5000)
	if got[0] != (Range{StartMS: 0, EndMS: 5000}) {
		t.Fatalf("got %v want padding clamped to 0-5000", got[0])
	}
}

func TestMapTimeRebasesOntoTheKeptTimeline(t *testing.T) {
	keep := Ranges{{StartMS: 1000, EndMS: 2000}, {StartMS: 5000, EndMS: 6000}}

	tests := []struct {
		name   string
		source int64
		want   int64
		inside bool
	}{
		{"start of the first kept span", 1000, 0, true},
		{"inside the first span", 1500, 500, true},
		{"inside the second span, shifted by the cut", 5500, 1500, true},
		{"inside a removed span", 3000, 1000, false},
		{"before everything", 0, 0, false},
		{"after everything", 9000, 2000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, inside := keep.MapTime(tt.source)
			if got != tt.want || inside != tt.inside {
				t.Fatalf("MapTime(%d) = %d,%v want %d,%v", tt.source, got, inside, tt.want, tt.inside)
			}
		})
	}
}

func TestParseTimeAcceptsTheFormsPeopleType(t *testing.T) {
	tests := map[string]int64{
		"12.5":         12500,
		"1:05":         65000,
		"01:02:03.250": 3723250,
		"90s":          90000,
		"1m30s":        90000,
		"0":            0,
	}
	for input, want := range tests {
		got, err := ParseTime(input)
		if err != nil {
			t.Fatalf("ParseTime(%q): %v", input, err)
		}
		if got != want {
			t.Fatalf("ParseTime(%q) = %d want %d", input, got, want)
		}
	}
}

func TestParseTimeRejectsNonsense(t *testing.T) {
	for _, input := range []string{"", "abc", "1:2:3:4", "-5"} {
		if _, err := ParseTime(input); err == nil {
			t.Fatalf("ParseTime(%q) should have failed", input)
		}
	}
}

func TestParseRangeOpenEnds(t *testing.T) {
	// A missing start means the beginning; a missing end means the media's.
	head, err := ParseRange("-0:30", 120000)
	if err != nil {
		t.Fatal(err)
	}
	if head != (Range{StartMS: 0, EndMS: 30000}) {
		t.Fatalf("got %v want 0-30000", head)
	}

	tail, err := ParseRange("1:00-", 120000)
	if err != nil {
		t.Fatal(err)
	}
	if tail != (Range{StartMS: 60000, EndMS: 120000}) {
		t.Fatalf("got %v want 60000-120000", tail)
	}

	// An open end is unanswerable without a duration, and guessing would
	// silently truncate the output.
	if _, err := ParseRange("1:00-", 0); err == nil {
		t.Fatal("an open end with an unknown duration should fail")
	}
	if _, err := ParseRange("2:00-1:00", 0); err == nil {
		t.Fatal("a backwards range should fail")
	}
}

func TestFormatTimeAndSeconds(t *testing.T) {
	if got := FormatTime(3723250); got != "01:02:03.250" {
		t.Fatalf("FormatTime = %q", got)
	}
	if got := FormatSeconds(1500); got != "1.500" {
		t.Fatalf("FormatSeconds = %q", got)
	}
}

func TestDropShorterThan(t *testing.T) {
	got := Ranges{
		{StartMS: 0, EndMS: 100},
		{StartMS: 200, EndMS: 900},
	}.DropShorterThan(200)
	if len(got) != 1 || got[0].StartMS != 200 {
		t.Fatalf("got %v want only the 700ms range", got)
	}
}
