package reminder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const reminderDeliveryContentType = "application/vnd.lites.internal.reminder-delivery.v1+json"

type HTTPDeliverySender struct {
	Endpoint, Readiness *url.URL
	Client              *http.Client
	BearerToken         string
	AllowInsecure       bool
}

func (sender HTTPDeliverySender) Send(ctx context.Context, delivery Delivery) error {
	if err := sender.validate(); err != nil || !validDelivery(delivery) {
		return NewSendError("delivery_configuration_invalid", true, err)
	}
	body, err := json.Marshal(struct {
		SchemaVersion int       `json:"schema_version"`
		TenantID      string    `json:"tenant_id"`
		UserID        string    `json:"user_id"`
		DeliveryID    string    `json:"delivery_id"`
		ScheduleID    string    `json:"schedule_id"`
		DeliveryKey   string    `json:"delivery_key"`
		Channel       string    `json:"channel"`
		Locale        string    `json:"locale"`
		Title         string    `json:"title"`
		Body          string    `json:"body"`
		OccurrenceAt  time.Time `json:"occurrence_at"`
	}{1, delivery.TenantID, delivery.UserID, delivery.DeliveryID, delivery.ScheduleID, delivery.DeliveryKey, delivery.Channel, delivery.Locale, delivery.Title, delivery.Body, delivery.OccurrenceAt.UTC()})
	if err != nil {
		return NewSendError("delivery_encoding_failed", true, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, sender.Endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return NewSendError("delivery_request_invalid", true, err)
	}
	request.Header.Set("Content-Type", reminderDeliveryContentType)
	request.Header.Set("Accept", reminderDeliveryContentType)
	request.Header.Set("Idempotency-Key", delivery.DeliveryKey)
	request.Header.Set("Authorization", "Bearer "+sender.BearerToken)
	response, err := sender.Client.Do(request)
	if err != nil {
		return NewSendError("delivery_dependency_unavailable", false, err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
	switch {
	case response.StatusCode == http.StatusOK || response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent:
		return nil
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		return NewSendError("delivery_dependency_unavailable", false, errors.New(response.Status))
	case response.StatusCode == http.StatusConflict:
		return NewSendError("delivery_idempotency_conflict", true, errors.New(response.Status))
	default:
		return NewSendError("delivery_request_rejected", true, errors.New(response.Status))
	}
}

func (sender HTTPDeliverySender) Ready(ctx context.Context) error {
	if err := sender.validate(); err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, sender.Readiness.String(), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+sender.BearerToken)
	response, err := sender.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK {
		return errors.New("notification gateway is not ready")
	}
	return nil
}

func (sender HTTPDeliverySender) validate() error {
	if sender.Endpoint == nil || sender.Readiness == nil || sender.Client == nil || sender.Client.Timeout <= 0 || len(sender.BearerToken) < 16 || strings.ContainsAny(sender.BearerToken, "\x00\r\n") {
		return errors.New("notification gateway configuration is invalid")
	}
	for _, endpoint := range []*url.URL{sender.Endpoint, sender.Readiness} {
		if endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Scheme != "https" && !(sender.AllowInsecure && endpoint.Scheme == "http" && isLoopbackHost(endpoint.Hostname())) {
			return errors.New("notification gateway URL is invalid")
		}
	}
	return nil
}

func validDelivery(delivery Delivery) bool {
	if delivery.TenantID == "" || delivery.UserID == "" || delivery.DeliveryID == "" || delivery.ScheduleID == "" || len(delivery.DeliveryKey) != 64 || delivery.Channel != "email" && delivery.Channel != "push" && delivery.Channel != "in_app" || delivery.Locale != "en" && delivery.Locale != "zh-CN" || delivery.Title == "" || len(delivery.Title) > 200 || delivery.Body == "" || len(delivery.Body) > 4000 || delivery.OccurrenceAt.IsZero() {
		return false
	}
	for _, value := range []string{delivery.TenantID, delivery.UserID, delivery.DeliveryID, delivery.ScheduleID, delivery.DeliveryKey, delivery.Channel, delivery.Locale, delivery.Title} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	return true
}

func isLoopbackHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
