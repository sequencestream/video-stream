package notify

import (
	"os"

	"github.com/sequencestream/video-stream/internal/config"
)

// FromConfig builds a multi-channel notifier from runtime config.
func FromConfig(cfg config.Notifications) Notifier {
	var channels []Notifier
	if cfg.WebhookURL != "" {
		channels = append(channels, Webhook{URL: cfg.WebhookURL})
	}
	if cfg.EmailTo != "" && cfg.SMTPHost != "" {
		channels = append(channels, Email{
			To: cfg.EmailTo,
			Mailer: SMTPMailer{
				Host: cfg.SMTPHost, Port: cfg.SMTPPort,
				From: cfg.SMTPFrom, User: cfg.SMTPUser,
				Pass: os.Getenv("VS_SMTP_PASS"),
			},
		})
	}
	if len(channels) == 0 {
		return Multi{}
	}
	return Multi{Channels: channels}
}
