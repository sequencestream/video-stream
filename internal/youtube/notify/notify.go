package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Event is a job completion payload.
type Event struct {
	ProjectID  string    `json:"project_id"`
	SessionID  string    `json:"session_id,omitempty"`
	OutputURI  string    `json:"output_uri"`
	VideoID    string    `json:"video_id,omitempty"`
	Title      string    `json:"title"`
	CompletedAt time.Time `json:"completed_at"`
}

// Notifier delivers completion events.
type Notifier interface {
	Notify(ctx context.Context, ev Event) error
}

// Multi fans out to configured channels.
type Multi struct {
	Channels []Notifier
}

func (m Multi) Notify(ctx context.Context, ev Event) error {
	var errs []string
	for _, ch := range m.Channels {
		if ch == nil {
			continue
		}
		if err := ch.Notify(ctx, ev); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("notify: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Webhook posts JSON to a URL.
type Webhook struct {
	URL    string
	Client *http.Client
}

func (w Webhook) Notify(ctx context.Context, ev Event) error {
	if strings.TrimSpace(w.URL) == "" {
		return nil
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook status %d", resp.StatusCode)
	}
	return nil
}

// Mailer sends email notifications.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// Email uses Mailer to notify.
type Email struct {
	To     string
	Mailer Mailer
}

func (e Email) Notify(ctx context.Context, ev Event) error {
	if strings.TrimSpace(e.To) == "" || e.Mailer == nil {
		return nil
	}
	subject := "video-stream: render complete — " + ev.Title
	var buf strings.Builder
	fmt.Fprintf(&buf, "Your video is ready.\n\nProject: %s\nOutput: %s\n", ev.ProjectID, ev.OutputURI)
	if ev.VideoID != "" {
		fmt.Fprintf(&buf, "YouTube: https://youtu.be/%s\n", ev.VideoID)
	}
	return e.Mailer.Send(ctx, e.To, subject, buf.String())
}

// SMTPMailer sends via net/smtp.
type SMTPMailer struct {
	Host string
	Port int
	From string
	User string
	Pass string
}

func (s SMTPMailer) Send(_ context.Context, to, subject, body string) error {
	if s.Host == "" {
		return fmt.Errorf("smtp host not configured")
	}
	port := s.Port
	if port == 0 {
		port = 587
	}
	from := s.From
	if from == "" {
		from = "video-stream@localhost"
	}
	msg := []byte("To: " + to + "\r\nSubject: " + subject + "\r\n\r\n" + body)
	addr := fmt.Sprintf("%s:%d", s.Host, port)
	if s.User != "" {
		auth := smtp.PlainAuth("", s.User, s.Pass, s.Host)
		return smtp.SendMail(addr, auth, from, []string{to}, msg)
	}
	return smtp.SendMail(addr, nil, from, []string{to}, msg)
}

// RecordMailer captures emails for tests.
type RecordMailer struct {
	LastTo, LastSubject, LastBody string
}

func (r *RecordMailer) Send(_ context.Context, to, subject, body string) error {
	r.LastTo, r.LastSubject, r.LastBody = to, subject, body
	return nil
}
