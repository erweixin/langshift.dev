package reminder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestHTTPDeliverySenderBindsDownstreamIdempotencyAndAuth(t *testing.T) {
	digest := sha256.Sum256([]byte("delivery"))
	key := hex.EncodeToString(digest[:])
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		if request.Method != http.MethodPost || request.Header.Get("Idempotency-Key") != key || request.Header.Get("Authorization") != "Bearer 0123456789abcdef" || request.Header.Get("Content-Type") != reminderDeliveryContentType {
			t.Fatalf("method=%s headers=%v", request.Method, request.Header)
		}
		writer.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/v1/internal/reminder-deliveries")
	ready, _ := url.Parse(server.URL + "/ready")
	sender := HTTPDeliverySender{Endpoint: endpoint, Readiness: ready, Client: server.Client(), BearerToken: "0123456789abcdef", AllowInsecure: true}
	sender.Client.Timeout = time.Second
	err := sender.Send(context.Background(), Delivery{TenantID: "tenant", UserID: "user", DeliveryID: "delivery", ScheduleID: "schedule", DeliveryKey: key, Channel: "push", Locale: "en", Title: "Practice", Body: "Open Lites", OccurrenceAt: time.Now()})
	if err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestHTTPDeliverySenderClassifiesRetryableAndPermanentResponses(t *testing.T) {
	digest := sha256.Sum256([]byte("delivery"))
	delivery := Delivery{TenantID: "tenant", UserID: "user", DeliveryID: "delivery", ScheduleID: "schedule", DeliveryKey: hex.EncodeToString(digest[:]), Channel: "in_app", Locale: "zh-CN", Title: "练习", Body: "打开 Lites", OccurrenceAt: time.Now()}
	for _, test := range []struct {
		status    int
		permanent bool
	}{
		{http.StatusTooManyRequests, false},
		{http.StatusServiceUnavailable, false},
		{http.StatusUnauthorized, true},
		{http.StatusConflict, true},
	} {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(test.status) }))
		endpoint, _ := url.Parse(server.URL + "/deliver")
		ready, _ := url.Parse(server.URL + "/ready")
		client := server.Client()
		client.Timeout = time.Second
		sender := HTTPDeliverySender{Endpoint: endpoint, Readiness: ready, Client: client, BearerToken: "0123456789abcdef", AllowInsecure: true}
		err := sender.Send(context.Background(), delivery)
		_, permanent := ClassifySendError(err)
		server.Close()
		if err == nil || permanent != test.permanent {
			t.Fatalf("status=%d permanent=%v err=%v", test.status, permanent, err)
		}
	}
}

func TestHTTPDeliverySenderRejectsUnencryptedNonLoopbackEndpoint(t *testing.T) {
	endpoint, _ := url.Parse("http://notification.example.invalid/deliver")
	ready, _ := url.Parse("http://notification.example.invalid/ready")
	sender := HTTPDeliverySender{Endpoint: endpoint, Readiness: ready, Client: &http.Client{Timeout: time.Second}, BearerToken: "0123456789abcdef", AllowInsecure: true}
	if sender.validate() == nil {
		t.Fatal("accepted plaintext non-loopback notification gateway")
	}
}
