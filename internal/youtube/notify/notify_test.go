package notify_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sequencestream/video-stream/internal/youtube/notify"
)

func TestWebhookNotifierIntegration(t *testing.T) {
	var got notify.Event
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	n := notify.Webhook{URL: srv.URL, Client: srv.Client()}
	ev := notify.Event{
		ProjectID: "p1", OutputURI: "/out/v.mp4", Title: "demo",
		CompletedAt: time.Now().UTC(),
	}
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if got.ProjectID != "p1" || got.OutputURI != ev.OutputURI {
		t.Fatalf("got %+v", got)
	}
}

func TestEmailNotifierIntegration(t *testing.T) {
	rec := &notify.RecordMailer{}
	n := notify.Email{To: "user@example.com", Mailer: rec}
	ev := notify.Event{ProjectID: "p1", OutputURI: "/out/v.mp4", Title: "demo"}
	if err := n.Notify(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if rec.LastTo != "user@example.com" {
		t.Fatalf("to = %q", rec.LastTo)
	}
	if rec.LastBody == "" || rec.LastSubject == "" {
		t.Fatalf("empty email: subj=%q body=%q", rec.LastSubject, rec.LastBody)
	}
}
