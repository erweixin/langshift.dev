package main

import (
	"bufio"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const maxMessageBytes = 2 << 20

type storedMessage struct {
	ID      string    `json:"ID"`
	From    string    `json:"From"`
	To      string    `json:"To"`
	Raw     string    `json:"Raw"`
	Decoded string    `json:"Decoded"`
	Created time.Time `json:"Created"`
}

type mailbox struct {
	mu       sync.RWMutex
	messages []storedMessage
	next     atomic.Uint64
}

func (box *mailbox) add(from, to string, raw []byte) {
	message := storedMessage{ID: fmt.Sprintf("local-%d", box.next.Add(1)), From: from, To: to, Raw: string(raw), Decoded: decodeMIME(raw), Created: time.Now().UTC()}
	box.mu.Lock()
	defer box.mu.Unlock()
	box.messages = append([]storedMessage{message}, box.messages...)
	if len(box.messages) > 1000 {
		box.messages = box.messages[:1000]
	}
}

func decodeMIME(raw []byte) string {
	message, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return ""
	}
	mediaType, parameters, err := mime.ParseMediaType(message.Header.Get("Content-Type"))
	if err == nil && strings.HasPrefix(mediaType, "multipart/") && parameters["boundary"] != "" {
		reader := multipart.NewReader(message.Body, parameters["boundary"])
		var decoded strings.Builder
		for decoded.Len() <= maxMessageBytes {
			part, nextErr := reader.NextPart()
			if errors.Is(nextErr, io.EOF) {
				return decoded.String()
			}
			if nextErr != nil {
				return ""
			}
			bodyReader := io.Reader(part)
			if strings.EqualFold(strings.TrimSpace(part.Header.Get("Content-Transfer-Encoding")), "base64") {
				bodyReader = base64.NewDecoder(base64.StdEncoding, part)
			}
			body, readErr := io.ReadAll(io.LimitReader(bodyReader, maxMessageBytes-int64(decoded.Len())+1))
			_ = part.Close()
			if readErr != nil || len(body)+decoded.Len() > maxMessageBytes {
				return ""
			}
			decoded.Write(body)
			decoded.WriteByte('\n')
		}
		return ""
	}
	bodyReader := io.Reader(message.Body)
	if strings.EqualFold(strings.TrimSpace(message.Header.Get("Content-Transfer-Encoding")), "base64") {
		bodyReader = base64.NewDecoder(base64.StdEncoding, message.Body)
	}
	body, err := io.ReadAll(io.LimitReader(bodyReader, maxMessageBytes+1))
	if err != nil || len(body) > maxMessageBytes {
		return ""
	}
	return string(body)
}

func (box *mailbox) handler(reminderToken string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/messages", box.list)
	mux.HandleFunc("GET /api/v1/message/{id}", box.get)
	mux.HandleFunc("GET /ready", noContent)
	mux.HandleFunc("GET /live", noContent)
	mux.HandleFunc("POST /v1/internal/reminder-deliveries", func(writer http.ResponseWriter, request *http.Request) {
		candidate := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if reminderToken == "" || len(candidate) != len(reminderToken) || subtle.ConstantTimeCompare([]byte(candidate), []byte(reminderToken)) != 1 {
			writer.Header().Set("WWW-Authenticate", `Bearer realm="local-reminder-sink"`)
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		request.Body = http.MaxBytesReader(writer, request.Body, 64<<10)
		_, _ = io.Copy(io.Discard, request.Body)
		writer.Header().Set("Cache-Control", "no-store")
		writer.WriteHeader(http.StatusAccepted)
	})
	return securityHeaders(mux)
}

func (box *mailbox) list(writer http.ResponseWriter, request *http.Request) {
	query := strings.TrimSpace(request.URL.Query().Get("query"))
	to := ""
	if strings.HasPrefix(strings.ToLower(query), "to:") {
		to = strings.TrimSpace(query[3:])
	}
	box.mu.RLock()
	messages := make([]storedMessage, 0, len(box.messages))
	for _, message := range box.messages {
		if to == "" || strings.EqualFold(message.To, to) {
			messages = append(messages, storedMessage{ID: message.ID, From: message.From, To: message.To, Created: message.Created})
		}
	}
	box.mu.RUnlock()
	writeJSON(writer, http.StatusOK, map[string]any{"messages": messages})
}

func (box *mailbox) get(writer http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	box.mu.RLock()
	defer box.mu.RUnlock()
	for _, message := range box.messages {
		if message.ID == id {
			writeJSON(writer, http.StatusOK, message)
			return
		}
	}
	http.Error(writer, "message not found", http.StatusNotFound)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func noContent(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusNoContent)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(writer, request)
	})
}

func serveSMTP(listener net.Listener, certificate tls.Certificate, box *mailbox) error {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		go func() { _ = handleSMTP(connection, certificate, box) }()
	}
}

func handleSMTP(connection net.Conn, certificate tls.Certificate, box *mailbox) error {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Minute))
	reader, writer := bufio.NewReader(connection), bufio.NewWriter(connection)
	if err := reply(writer, "220 local-mailbox ESMTP ready"); err != nil {
		return err
	}
	secure, greeted := false, false
	from, to := "", ""
	for {
		line, err := readLine(reader, 4096)
		if err != nil {
			return err
		}
		command, argument := splitCommand(line)
		switch command {
		case "EHLO", "HELO":
			if argument == "" {
				_ = reply(writer, "501 hostname required")
				continue
			}
			greeted = true
			if !secure {
				if err = reply(writer, "250-local-mailbox\r\n250-STARTTLS\r\n250 SIZE 2097152"); err != nil {
					return err
				}
			} else if err = reply(writer, "250-local-mailbox\r\n250 SIZE 2097152"); err != nil {
				return err
			}
		case "STARTTLS":
			if secure || !greeted {
				_ = reply(writer, "503 invalid command sequence")
				continue
			}
			if err = reply(writer, "220 begin TLS"); err != nil {
				return err
			}
			tlsConnection := tls.Server(connection, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
			if err = tlsConnection.Handshake(); err != nil {
				return err
			}
			connection, reader, writer = tlsConnection, bufio.NewReader(tlsConnection), bufio.NewWriter(tlsConnection)
			secure, greeted, from, to = true, false, "", ""
		case "MAIL":
			if !secure || !greeted {
				_ = reply(writer, "530 STARTTLS and EHLO required")
				continue
			}
			from = smtpAddress(argument, "FROM:")
			if from == "" {
				_ = reply(writer, "501 invalid sender")
				continue
			}
			to = ""
			_ = reply(writer, "250 sender accepted")
		case "RCPT":
			if from == "" {
				_ = reply(writer, "503 MAIL required")
				continue
			}
			to = smtpAddress(argument, "TO:")
			if to == "" {
				_ = reply(writer, "501 invalid recipient")
				continue
			}
			_ = reply(writer, "250 recipient accepted")
		case "DATA":
			if to == "" {
				_ = reply(writer, "503 RCPT required")
				continue
			}
			if err = reply(writer, "354 end with <CRLF>.<CRLF>"); err != nil {
				return err
			}
			raw, readErr := readData(reader)
			if readErr != nil {
				_ = reply(writer, "552 message too large or invalid")
				return readErr
			}
			box.add(from, to, raw)
			from, to = "", ""
			_ = reply(writer, "250 message accepted")
		case "RSET":
			from, to = "", ""
			_ = reply(writer, "250 reset")
		case "NOOP":
			_ = reply(writer, "250 ok")
		case "QUIT":
			_ = reply(writer, "221 bye")
			return nil
		default:
			_ = reply(writer, "502 command not implemented")
		}
	}
}

func splitCommand(line string) (string, string) {
	parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
	command := strings.ToUpper(parts[0])
	if len(parts) == 1 {
		return command, ""
	}
	return command, strings.TrimSpace(parts[1])
}

func smtpAddress(argument, prefix string) string {
	if !strings.HasPrefix(strings.ToUpper(argument), prefix) {
		return ""
	}
	value := strings.TrimSpace(argument[len(prefix):])
	if strings.HasPrefix(value, "<") && strings.Contains(value, ">") {
		value = value[1:strings.IndexByte(value, '>')]
	}
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.ContainsAny(value, "\r\n\x00") {
		return ""
	}
	return value
}

func readLine(reader *bufio.Reader, maximum int) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil || len(line) > maximum || !strings.HasSuffix(line, "\r\n") {
		return "", errors.New("invalid SMTP line")
	}
	return strings.TrimSuffix(line, "\r\n"), nil
}

func readData(reader *bufio.Reader) ([]byte, error) {
	var output strings.Builder
	for output.Len() <= maxMessageBytes {
		line, err := readLine(reader, maxMessageBytes+2)
		if err != nil {
			return nil, err
		}
		if line == "." {
			return []byte(output.String()), nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		output.WriteString(line)
		output.WriteString("\r\n")
	}
	return nil, errors.New("message exceeds limit")
}

func reply(writer *bufio.Writer, response string) error {
	if _, err := writer.WriteString(response + "\r\n"); err != nil {
		return err
	}
	return writer.Flush()
}
