package mail

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"strings"
	"time"
)

var ErrSMTPConfiguration = errors.New("smtp configuration is invalid")

type Sender interface {
	Send(context.Context, string, Message) error
}

type SMTPConfig struct {
	Address, ServerName, FromAddress, FromName, Username, Password string
	RootCAs                                                        *x509.CertPool
	ClientCertificate                                              *tls.Certificate
	DialTimeout                                                    time.Duration
}

type SMTPSender struct{ Config SMTPConfig }

func (sender SMTPSender) Send(ctx context.Context, messageID string, message Message) error {
	configuration := sender.Config
	if err := validateSMTP(configuration, messageID, message); err != nil {
		return err
	}
	client, err := sender.connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	if err = client.Mail(configuration.FromAddress); err != nil {
		return err
	}
	if err = client.Rcpt(message.To); err != nil {
		return err
	}
	data, err := client.Data()
	if err != nil {
		return err
	}
	encoded, err := encodeMessage(configuration, messageID, message)
	if err == nil {
		_, err = data.Write(encoded)
	}
	closeErr := data.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// DATA Close waits for the server's final 250 reply. At that point the
	// message is accepted; a broken QUIT response must not trigger a duplicate.
	_ = client.Quit()
	return nil
}

func (sender SMTPSender) Ready(ctx context.Context) error {
	if err := validateSMTP(sender.Config, "readiness", Message{To: sender.Config.FromAddress, Subject: "readiness", Text: "readiness", HTML: "<p>readiness</p>"}); err != nil {
		return err
	}
	client, err := sender.connect(ctx)
	if err != nil {
		return err
	}
	defer client.Close()
	return client.Quit()
}

func (sender SMTPSender) connect(ctx context.Context) (*smtp.Client, error) {
	configuration := sender.Config
	dialer := net.Dialer{Timeout: configuration.DialTimeout, KeepAlive: 30 * time.Second}
	connection, err := dialer.DialContext(ctx, "tcp", configuration.Address)
	if err != nil {
		return nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(configuration.DialTimeout)
	}
	if err = connection.SetDeadline(deadline); err != nil {
		_ = connection.Close()
		return nil, err
	}
	client, err := smtp.NewClient(connection, configuration.ServerName)
	if err != nil {
		_ = connection.Close()
		return nil, err
	}
	if supported, _ := client.Extension("STARTTLS"); !supported {
		_ = client.Close()
		return nil, errors.New("smtp server does not advertise STARTTLS")
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: configuration.ServerName, RootCAs: configuration.RootCAs}
	if configuration.ClientCertificate != nil {
		tlsConfig.Certificates = []tls.Certificate{*configuration.ClientCertificate}
	}
	if err = client.StartTLS(tlsConfig); err != nil {
		_ = client.Close()
		return nil, err
	}
	if configuration.Username != "" {
		if supported, _ := client.Extension("AUTH"); !supported {
			_ = client.Close()
			return nil, errors.New("smtp server does not advertise AUTH")
		}
		if err = client.Auth(smtp.PlainAuth("", configuration.Username, configuration.Password, configuration.ServerName)); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	return client, nil
}

func validateSMTP(configuration SMTPConfig, messageID string, message Message) error {
	if configuration.Address == "" || configuration.ServerName == "" || net.ParseIP(configuration.ServerName) != nil || configuration.FromAddress == "" || configuration.DialTimeout <= 0 || configuration.DialTimeout > 2*time.Minute || messageID == "" || message.To == "" || message.Subject == "" || message.Text == "" || message.HTML == "" {
		return ErrSMTPConfiguration
	}
	for _, value := range []string{configuration.ServerName, configuration.FromAddress, configuration.FromName, configuration.Username, messageID, message.To, message.Subject} {
		if strings.ContainsAny(value, "\r\n\x00") {
			return ErrSMTPConfiguration
		}
	}
	if (configuration.Username == "") != (configuration.Password == "") {
		return ErrSMTPConfiguration
	}
	from, fromErr := stdmail.ParseAddress(configuration.FromAddress)
	to, toErr := stdmail.ParseAddress(message.To)
	if fromErr != nil || toErr != nil || from.Address != configuration.FromAddress || to.Address != message.To {
		return ErrSMTPConfiguration
	}
	return nil
}

func encodeMessage(configuration SMTPConfig, messageID string, message Message) ([]byte, error) {
	boundary := "lites-" + base64.RawURLEncoding.EncodeToString([]byte(messageID))
	if len(boundary) > 180 {
		return nil, ErrSMTPConfiguration
	}
	var output bytes.Buffer
	writer := bufio.NewWriter(&output)
	from := configuration.FromAddress
	if configuration.FromName != "" {
		from = mime.QEncoding.Encode("UTF-8", configuration.FromName) + " <" + configuration.FromAddress + ">"
	}
	headers := []string{
		"From: " + from,
		"To: " + message.To,
		"Subject: " + mime.QEncoding.Encode("UTF-8", message.Subject),
		"Message-ID: <" + messageID + "@" + configuration.ServerName + ">",
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Auto-Submitted: auto-generated",
		"Content-Type: multipart/alternative; boundary=\"" + boundary + "\"",
	}
	for _, header := range headers {
		if _, err := writer.WriteString(header + "\r\n"); err != nil {
			return nil, err
		}
	}
	if _, err := writer.WriteString("\r\n--" + boundary + "\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"); err != nil {
		return nil, err
	}
	writeBase64(writer, []byte(message.Text))
	if _, err := writer.WriteString("\r\n--" + boundary + "\r\nContent-Type: text/html; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"); err != nil {
		return nil, err
	}
	writeBase64(writer, []byte(message.HTML))
	if _, err := writer.WriteString("\r\n--" + boundary + "--\r\n"); err != nil {
		return nil, err
	}
	if err := writer.Flush(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func writeBase64(writer io.Writer, value []byte) {
	encoded := base64.StdEncoding.EncodeToString(value)
	for len(encoded) > 76 {
		_, _ = fmt.Fprintf(writer, "%s\r\n", encoded[:76])
		encoded = encoded[76:]
	}
	_, _ = fmt.Fprint(writer, encoded)
}
