package label_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sequencestream/video-stream/internal/label"
)

func TestInjectAndVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(out, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := label.Build("proj-1", "run-1")
	inj := label.SidecarInjector{}
	if err := label.InjectAndVerify(&inj, out, l); err != nil {
		t.Fatal(err)
	}
}

func TestTamperedMetadataRejected(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.mp4")
	if err := os.WriteFile(out, []byte("mp4"), 0o644); err != nil {
		t.Fatal(err)
	}

	l := label.Build("proj-1", "run-1")
	inj := label.SidecarInjector{}
	if err := inj.Inject(out, l); err != nil {
		t.Fatal(err)
	}
	if err := label.TamperSidecar(out); err != nil {
		t.Fatal(err)
	}
	if err := label.VerifyReadback(&inj, out, l); !errors.Is(err, label.ErrReadbackMismatch) {
		t.Fatalf("got %v, want ErrReadbackMismatch", err)
	}
}

func TestNilInjectorIsBypassForbidden(t *testing.T) {
	err := label.InjectAndVerify(nil, "/tmp/x.mp4", label.Build("p", "r"))
	if !errors.Is(err, label.ErrBypassForbidden) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildUsesFixedAttributeAndProvider(t *testing.T) {
	l := label.Build("p1", "r1")
	if l.ContentAttribute != label.ContentAttributeValue {
		t.Fatalf("attribute = %q", l.ContentAttribute)
	}
	if l.ServiceProviderCode != label.ServiceProviderCode {
		t.Fatalf("provider = %q", l.ServiceProviderCode)
	}
	if l.ContentID != "p1:r1" {
		t.Fatalf("content_id = %q", l.ContentID)
	}
}
