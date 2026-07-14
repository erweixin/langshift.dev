package mail

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDecodeAndRenderAllFixedTemplates(t *testing.T) {
	appURL, err := url.Parse("https://app.lites.dev/account")
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{
		`{"template":"verify-email-v1","locale":"zh-CN","recipient":"person@example.com","token":"abcdefghijklmnopqrstuvwxyz0123456789","expires_at":"2026-07-15T12:00:00Z"}`,
		`{"template":"password-reset-v1","recipient":"person@example.com","token":"abcdefghijklmnopqrstuvwxyz0123456789","expires_at":"2026-07-15T12:00:00Z"}`,
		`{"template":"confirm-email-change-v1","recipient":"person@example.com","token":"abcdefghijklmnopqrstuvwxyz0123456789","expires_at":"2026-07-15T12:00:00Z"}`,
		`{"template":"tenant-invitation-v1","recipient":"person@example.com","token":"abcdefghijklmnopqrstuvwxyz0123456789","invitation_id":"invitation-1","expires_at":"2026-07-15T12:00:00Z"}`,
		`{"template":"password-changed-security-v1","recipient":"person@example.com","changed_at":"2026-07-14T12:00:00Z","reason":"password_reset"}`,
		`{"template":"email-change-requested-security-v1","recipient":"person@example.com","requested_at":"2026-07-14T12:00:00Z"}`,
		`{"template":"email-changed-old-address-security-v1","recipient":"person@example.com","changed_at":"2026-07-14T12:00:00Z"}`,
		`{"template":"email-changed-new-address-security-v1","recipient":"person@example.com","changed_at":"2026-07-14T12:00:00Z"}`,
	}
	for _, encoded := range tests {
		command, decodeErr := Decode([]byte(encoded))
		if decodeErr != nil {
			t.Fatalf("decode %s: %v", encoded, decodeErr)
		}
		message, renderErr := Render(command, appURL)
		if renderErr != nil || message.To != "person@example.com" || message.Subject == "" || message.Text == "" || message.HTML == "" {
			t.Fatalf("message=%#v error=%v", message, renderErr)
		}
		if strings.Contains(message.Text, "{{") || strings.Contains(message.HTML, "{{") {
			t.Fatal("unrendered template marker")
		}
		if command.Token != "" && (!strings.Contains(message.Text, "token=") || strings.Contains(message.Subject, command.Token)) {
			t.Fatal("token link or subject boundary failed")
		}
	}
}

func TestDecodeRejectsTemplateConfusionAndHeaderInjection(t *testing.T) {
	for _, encoded := range []string{
		`{"template":"unknown","recipient":"person@example.com"}`,
		`{"template":"verify-email-v1","recipient":"person@example.com","token":"short","expires_at":"2026-07-15T12:00:00Z"}`,
		`{"template":"password-changed-security-v1","recipient":"person@example.com","changed_at":"2026-07-14T12:00:00Z","reason":"attacker"}`,
		`{"template":"email-changed-new-address-security-v1","recipient":"person@example.com\r\nBcc: attacker@example.com","changed_at":"2026-07-14T12:00:00Z"}`,
		`{"template":"email-changed-new-address-security-v1","recipient":"Person@example.com","changed_at":"2026-07-14T12:00:00Z"}`,
		`{"template":"email-changed-new-address-security-v1","recipient":"person@example.com","changed_at":"2026-07-14T12:00:00Z","token":"abcdefghijklmnopqrstuvwxyz0123456789"}`,
		`{"template":"email-changed-new-address-security-v1","recipient":"person@example.com","changed_at":"2026-07-14T12:00:00Z","extra":true}`,
	} {
		if _, err := Decode([]byte(encoded)); err == nil {
			t.Fatalf("invalid command accepted: %s", encoded)
		}
	}
}

func TestEncodeMessageUsesStableIDAndDoesNotExposeBodiesInHeaders(t *testing.T) {
	configuration := SMTPConfig{Address: "smtp.example.com:587", ServerName: "smtp.example.com", FromAddress: "security@lites.dev", FromName: "Lites", DialTimeout: 10 * time.Second}
	encoded, err := encodeMessage(configuration, "10000000-0000-4000-8000-000000000001", Message{To: "person@example.com", Subject: "安全提醒", Text: "plain token", HTML: "<p>html token</p>"})
	if err != nil {
		t.Fatal(err)
	}
	value := string(encoded)
	if !strings.Contains(value, "Message-ID: <10000000-0000-4000-8000-000000000001@smtp.example.com>") || !strings.Contains(value, "multipart/alternative") || strings.Contains(strings.Split(value, "\r\n\r\n")[0], "plain token") {
		t.Fatalf("unexpected message: %s", value)
	}
}
