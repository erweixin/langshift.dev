package mail

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	htmltemplate "html/template"
	"io"
	"net"
	"net/url"
	"strings"
	texttemplate "text/template"
	"time"

	identityemail "github.com/langshift/lites/internal/identity/email"
)

var ErrInvalidCommand = errors.New("mail command is invalid")

type Command struct {
	Template     string    `json:"template"`
	Locale       string    `json:"locale,omitempty"`
	Recipient    string    `json:"recipient"`
	Token        string    `json:"token,omitempty"`
	InvitationID string    `json:"invitation_id,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
	ChangedAt    time.Time `json:"changed_at,omitempty"`
	RequestedAt  time.Time `json:"requested_at,omitempty"`
	Reason       string    `json:"reason,omitempty"`
}

type Message struct {
	To, Subject, Text, HTML string
}

type templateDefinition struct {
	subjectEN, subjectZH string
	textEN, textZH       string
	htmlEN, htmlZH       string
	path                 string
	requiresToken        bool
	requiresInvitation   bool
	requiresChangedAt    bool
	requiresRequestedAt  bool
	requiresReason       bool
}

var definitions = map[string]templateDefinition{
	"verify-email-v1": {
		subjectEN: "Verify your Lites email", subjectZH: "验证你的 Lites 邮箱",
		textEN: "Verify your email to activate Lites:\n{{.Link}}\n\nThis link expires at {{.ExpiresAt}}.", textZH: "验证邮箱以启用 Lites：\n{{.Link}}\n\n链接将在 {{.ExpiresAt}} 过期。",
		htmlEN: "<p>Verify your email to activate Lites:</p><p><a href=\"{{.Link}}\">Verify email</a></p><p>This link expires at {{.ExpiresAt}}.</p>", htmlZH: "<p>验证邮箱以启用 Lites：</p><p><a href=\"{{.Link}}\">验证邮箱</a></p><p>链接将在 {{.ExpiresAt}} 过期。</p>",
		path: "/verify-email", requiresToken: true,
	},
	"password-reset-v1": {
		subjectEN: "Reset your Lites password", subjectZH: "重置你的 Lites 密码",
		textEN: "Reset your Lites password:\n{{.Link}}\n\nThis link expires at {{.ExpiresAt}}. If you did not request it, ignore this email.", textZH: "重置你的 Lites 密码：\n{{.Link}}\n\n链接将在 {{.ExpiresAt}} 过期。如果不是你发起的，请忽略此邮件。",
		htmlEN: "<p>Reset your Lites password:</p><p><a href=\"{{.Link}}\">Reset password</a></p><p>This link expires at {{.ExpiresAt}}. If you did not request it, ignore this email.</p>", htmlZH: "<p>重置你的 Lites 密码：</p><p><a href=\"{{.Link}}\">重置密码</a></p><p>链接将在 {{.ExpiresAt}} 过期。如果不是你发起的，请忽略此邮件。</p>",
		path: "/reset-password", requiresToken: true,
	},
	"confirm-email-change-v1": {
		subjectEN: "Confirm your new Lites email", subjectZH: "确认你的 Lites 新邮箱",
		textEN: "Confirm this email address for your Lites account:\n{{.Link}}\n\nThis link expires at {{.ExpiresAt}}.", textZH: "确认将此邮箱用于你的 Lites 账号：\n{{.Link}}\n\n链接将在 {{.ExpiresAt}} 过期。",
		htmlEN: "<p>Confirm this email address for your Lites account:</p><p><a href=\"{{.Link}}\">Confirm email</a></p><p>This link expires at {{.ExpiresAt}}.</p>", htmlZH: "<p>确认将此邮箱用于你的 Lites 账号：</p><p><a href=\"{{.Link}}\">确认邮箱</a></p><p>链接将在 {{.ExpiresAt}} 过期。</p>",
		path: "/confirm-email-change", requiresToken: true,
	},
	"tenant-invitation-v1": {
		subjectEN: "You are invited to Lites", subjectZH: "你收到了 Lites 邀请",
		textEN: "Accept your Lites organization invitation:\n{{.Link}}\n\nThis link expires at {{.ExpiresAt}}.", textZH: "接受你的 Lites 组织邀请：\n{{.Link}}\n\n链接将在 {{.ExpiresAt}} 过期。",
		htmlEN: "<p>Accept your Lites organization invitation:</p><p><a href=\"{{.Link}}\">Accept invitation</a></p><p>This link expires at {{.ExpiresAt}}.</p>", htmlZH: "<p>接受你的 Lites 组织邀请：</p><p><a href=\"{{.Link}}\">接受邀请</a></p><p>链接将在 {{.ExpiresAt}} 过期。</p>",
		path: "/accept-invitation", requiresToken: true, requiresInvitation: true,
	},
	"password-changed-security-v1": {
		subjectEN: "Your Lites password changed", subjectZH: "你的 Lites 密码已更改",
		textEN: "Your Lites password changed at {{.ChangedAt}} ({{.Reason}}). If this was not you, reset your password and contact support.", textZH: "你的 Lites 密码已于 {{.ChangedAt}} 更改（{{.Reason}}）。如果不是你本人操作，请立即重置密码并联系支持。",
		htmlEN: "<p>Your Lites password changed at {{.ChangedAt}} ({{.Reason}}).</p><p>If this was not you, reset your password and contact support.</p>", htmlZH: "<p>你的 Lites 密码已于 {{.ChangedAt}} 更改（{{.Reason}}）。</p><p>如果不是你本人操作，请立即重置密码并联系支持。</p>",
		requiresChangedAt: true, requiresReason: true,
	},
	"email-change-requested-security-v1": {
		subjectEN: "Lites email change requested", subjectZH: "Lites 邮箱变更请求",
		textEN: "An email change was requested for your Lites account at {{.RequestedAt}}. If this was not you, change your password and contact support.", textZH: "你的 Lites 账号于 {{.RequestedAt}} 发起了邮箱变更。如果不是你本人操作，请更改密码并联系支持。",
		htmlEN: "<p>An email change was requested for your Lites account at {{.RequestedAt}}.</p><p>If this was not you, change your password and contact support.</p>", htmlZH: "<p>你的 Lites 账号于 {{.RequestedAt}} 发起了邮箱变更。</p><p>如果不是你本人操作，请更改密码并联系支持。</p>",
		requiresRequestedAt: true,
	},
	"email-changed-old-address-security-v1": {
		subjectEN: "Your Lites email changed", subjectZH: "你的 Lites 邮箱已更改",
		textEN: "The email address on your Lites account changed at {{.ChangedAt}}. If this was not you, contact support immediately.", textZH: "你的 Lites 账号邮箱已于 {{.ChangedAt}} 更改。如果不是你本人操作，请立即联系支持。",
		htmlEN: "<p>The email address on your Lites account changed at {{.ChangedAt}}.</p><p>If this was not you, contact support immediately.</p>", htmlZH: "<p>你的 Lites 账号邮箱已于 {{.ChangedAt}} 更改。</p><p>如果不是你本人操作，请立即联系支持。</p>",
		requiresChangedAt: true,
	},
	"email-changed-new-address-security-v1": {
		subjectEN: "Your new Lites email is active", subjectZH: "你的 Lites 新邮箱已启用",
		textEN: "This email address became active on your Lites account at {{.ChangedAt}}.", textZH: "此邮箱已于 {{.ChangedAt}} 成为你的 Lites 账号邮箱。",
		htmlEN: "<p>This email address became active on your Lites account at {{.ChangedAt}}.</p>", htmlZH: "<p>此邮箱已于 {{.ChangedAt}} 成为你的 Lites 账号邮箱。</p>",
		requiresChangedAt: true,
	},
}

func Decode(body []byte) (Command, error) {
	if len(body) == 0 || len(body) > 32<<10 {
		return Command{}, ErrInvalidCommand
	}
	var command Command
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		return Command{}, ErrInvalidCommand
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Command{}, ErrInvalidCommand
	}
	definition, ok := definitions[command.Template]
	if !ok || command.Recipient == "" || len(command.Recipient) > 320 || strings.ContainsAny(command.Recipient, "\r\n") {
		return Command{}, ErrInvalidCommand
	}
	normalized, err := identityemail.Normalize(command.Recipient)
	if err != nil || normalized != command.Recipient {
		return Command{}, ErrInvalidCommand
	}
	if command.Locale == "" {
		command.Locale = "en"
	}
	if command.Locale != "en" && command.Locale != "zh-CN" {
		return Command{}, ErrInvalidCommand
	}
	if definition.requiresToken != (command.Token != "") || definition.requiresInvitation != (command.InvitationID != "") || definition.requiresChangedAt != !command.ChangedAt.IsZero() || definition.requiresRequestedAt != !command.RequestedAt.IsZero() || definition.requiresReason != (command.Reason != "") {
		return Command{}, ErrInvalidCommand
	}
	if definition.requiresToken && (len(command.Token) < 32 || len(command.Token) > 2048 || command.ExpiresAt.IsZero()) {
		return Command{}, ErrInvalidCommand
	}
	if strings.ContainsAny(command.Token, "\r\n\x00") || len(command.InvitationID) > 200 || strings.ContainsAny(command.InvitationID, "\r\n\x00") {
		return Command{}, ErrInvalidCommand
	}
	if !definition.requiresToken && !command.ExpiresAt.IsZero() {
		return Command{}, ErrInvalidCommand
	}
	if command.Reason != "" && command.Reason != "authenticated_change" && command.Reason != "password_reset" {
		return Command{}, ErrInvalidCommand
	}
	return command, nil
}

func Render(command Command, appURL *url.URL) (Message, error) {
	definition, ok := definitions[command.Template]
	loopbackHTTP := appURL != nil && appURL.Scheme == "http" && net.ParseIP(appURL.Hostname()) != nil && net.ParseIP(appURL.Hostname()).IsLoopback()
	if !ok || appURL == nil || (appURL.Scheme != "https" && !loopbackHTTP) || appURL.Host == "" || appURL.User != nil || appURL.RawQuery != "" || appURL.Fragment != "" {
		return Message{}, ErrInvalidCommand
	}
	data := struct{ Link, ExpiresAt, ChangedAt, RequestedAt, Reason string }{
		ExpiresAt: command.ExpiresAt.UTC().Format(time.RFC3339), ChangedAt: command.ChangedAt.UTC().Format(time.RFC3339), RequestedAt: command.RequestedAt.UTC().Format(time.RFC3339), Reason: command.Reason,
	}
	if definition.path != "" {
		link := *appURL
		link.Path = strings.TrimRight(link.Path, "/") + definition.path
		query := url.Values{"token": []string{command.Token}}
		query.Set("locale", command.Locale)
		if command.InvitationID != "" {
			query.Set("invitation_id", command.InvitationID)
		}
		link.RawQuery = query.Encode()
		data.Link = link.String()
	}
	subject, textBody, htmlBody := definition.subjectEN, definition.textEN, definition.htmlEN
	if command.Locale == "zh-CN" {
		subject, textBody, htmlBody = definition.subjectZH, definition.textZH, definition.htmlZH
	}
	var textOutput, htmlOutput bytes.Buffer
	if err := texttemplate.Must(texttemplate.New("text").Option("missingkey=error").Parse(textBody)).Execute(&textOutput, data); err != nil {
		return Message{}, fmt.Errorf("%w: render text", ErrInvalidCommand)
	}
	if err := htmltemplate.Must(htmltemplate.New("html").Option("missingkey=error").Parse(htmlBody)).Execute(&htmlOutput, data); err != nil {
		return Message{}, fmt.Errorf("%w: render html", ErrInvalidCommand)
	}
	return Message{To: command.Recipient, Subject: subject, Text: textOutput.String(), HTML: htmlOutput.String()}, nil
}
