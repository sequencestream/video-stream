package intake_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/sequencestream/video-stream/internal/intake"
)

func TestSplitBreaksOnSentenceEndingPunctuation(t *testing.T) {
	got := intake.Split("今天带着小孩去看了一场电影。整个影片反映了战争的残酷！有什么方法能避免战争呢？", 0, 0)
	want := []string{
		"今天带着小孩去看了一场电影。",
		"整个影片反映了战争的残酷！",
		"有什么方法能避免战争呢？",
	}
	assertLines(t, got, want)
}

func TestSplitCutsAnOverLongSentenceAtItsLastClauseBreak(t *testing.T) {
	// One sentence, no full stop until the end, well past the cap.
	script := "面对双方都很难改变的仇恨和敌视，徐福夹在中间，会去帮助两边的人，也会告诉他们生活才是最重要的。"
	got := intake.Split(script, 20, 0)
	if len(got) < 2 {
		t.Fatalf("over-long sentence was not split: %q", got)
	}
	for _, line := range got {
		if utf8.RuneCountInString(line) > 20 {
			t.Errorf("line exceeds the cap: %q (%d runes)", line, utf8.RuneCountInString(line))
		}
	}
	if joined := strings.Join(got, ""); joined != script {
		t.Errorf("splitting lost or reordered text:\n got %q\nwant %q", joined, script)
	}
}

func TestSplitLeavesAnUncuttableSentenceWhole(t *testing.T) {
	// No clause break anywhere, so there is no safe cut point.
	script := "好好吃饭别那么多仇恨和敌视好好吃饭别那么多仇恨和敌视。"
	got := intake.Split(script, 10, 0)
	assertLines(t, got, []string{script})
}

func TestSplitMergesRuntLinesIntoTheirPredecessor(t *testing.T) {
	got := intake.Split("但他没办法改变这一切。真的。深陷其中的人被迫一路走到黑。", 0, 8)
	want := []string{
		"但他没办法改变这一切。真的。",
		"深陷其中的人被迫一路走到黑。",
	}
	assertLines(t, got, want)
}

func TestSplitMergesALeadingRuntForward(t *testing.T) {
	got := intake.Split("你好。今天带着小孩去看了一场很好的电影。", 0, 8)
	assertLines(t, got, []string{"你好。今天带着小孩去看了一场很好的电影。"})
}

func TestSplitKeepsASpaceBetweenASCIIWordsOnly(t *testing.T) {
	got := intake.Split("Hello. World is wide and full of things.", 0, 8)
	assertLines(t, got, []string{"Hello. World is wide and full of things."})
}

func TestSplitReturnsNothingForBlankInput(t *testing.T) {
	if got := intake.Split("   \n\n  ", 0, 0); len(got) != 0 {
		t.Fatalf("got %q, want no lines", got)
	}
}

func TestSplitTreatsBlankLinesAsBreaks(t *testing.T) {
	got := intake.Split("第一段没有句号的内容\n\n第二段同样没有句号", 0, 0)
	assertLines(t, got, []string{"第一段没有句号的内容", "第二段同样没有句号"})
}

func assertLines(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d lines %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}
