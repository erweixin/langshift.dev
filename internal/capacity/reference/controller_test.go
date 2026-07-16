package reference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type managedWorkloadStub struct {
	ready   chan struct{}
	errors  chan error
	started bool
	stopped bool
	mu      sync.Mutex
}

func newManagedWorkloadStub() *managedWorkloadStub {
	return &managedWorkloadStub{ready: make(chan struct{}), errors: make(chan error, 1)}
}

func (stub *managedWorkloadStub) Start(context.Context) error {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.started = true
	return nil
}
func (stub *managedWorkloadStub) Ready() <-chan struct{} { return stub.ready }
func (stub *managedWorkloadStub) Errors() <-chan error   { return stub.errors }
func (stub *managedWorkloadStub) Stop() {
	stub.mu.Lock()
	defer stub.mu.Unlock()
	stub.stopped = true
}

func TestManagerRequiresWarmupAndFullMeasuredDuration(t *testing.T) {
	now := time.Date(2026, time.July, 16, 10, 0, 0, 0, time.UTC)
	workload := newManagedWorkloadStub()
	manager := &Manager{Profile: testProfile(), ProfileSHA256: strings.Repeat("a", 64), SourceCommit: strings.Repeat("b", 40), DatasetSHA256: strings.Repeat("c", 64), Now: func() time.Time { return now }, Factory: func(string) (ManagedWorkload, error) { return workload, nil }}
	command := StartCommand{ProfileID: ProfileID, ProfileSHA256: strings.Repeat("a", 64), SourceCommit: strings.Repeat("b", 40), DurationSeconds: 1800}
	status, err := manager.Start(t.Context(), command)
	if err != nil || status.Status != "warming" || status.StartedAt != nil || !workload.started {
		t.Fatalf("start status=%#v err=%v", status, err)
	}
	replayed, err := manager.Start(t.Context(), command)
	if err != nil || replayed.RunID != status.RunID {
		t.Fatalf("start replay status=%#v err=%v", replayed, err)
	}
	close(workload.ready)
	deadline := time.Now().Add(time.Second)
	for {
		status, err = manager.Get(status.RunID)
		if err == nil && status.Status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run never became ready: %#v %v", status, err)
		}
		time.Sleep(time.Millisecond)
	}
	if status.StartedAt == nil || !status.StartedAt.Equal(now) {
		t.Fatalf("startedAt=%v", status.StartedAt)
	}
	if _, err = manager.Complete(status.RunID); err != ErrTooEarly {
		t.Fatalf("early completion err=%v", err)
	}
	now = now.Add(1800 * time.Second)
	completed, err := manager.Complete(status.RunID)
	if err != nil || completed.Status != "completed" || completed.CompletedAt == nil || !workload.stopped {
		t.Fatalf("completed=%#v err=%v", completed, err)
	}
}

func TestControllerHandlerAuthenticatesAndRejectsProtocolDrift(t *testing.T) {
	workload := newManagedWorkloadStub()
	manager := &Manager{Profile: testProfile(), ProfileSHA256: strings.Repeat("a", 64), SourceCommit: strings.Repeat("b", 40), DatasetSHA256: strings.Repeat("c", 64), Factory: func(string) (ManagedWorkload, error) { return workload, nil }}
	handler := Handler{Manager: manager, BearerToken: "controller-secret-token"}
	body := `{"profileId":"stage3-reference-production-v1","profileSha256":"` + strings.Repeat("a", 64) + `","sourceCommit":"` + strings.Repeat("b", 40) + `","durationSeconds":1800}`
	unauthorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/reference-capacity-runs", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", unauthorized.Code)
	}
	response := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/reference-capacity-runs", bytes.NewBufferString(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer controller-secret-token")
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var status Status
	if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Status != "warming" || status.RunID == "" {
		t.Fatalf("status=%#v err=%v", status, err)
	}
	drift := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/reference-capacity-runs", bytes.NewBufferString(strings.Replace(body, `"durationSeconds":1800`, `"durationSeconds":1799`, 1)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer controller-secret-token")
	handler.ServeHTTP(drift, request)
	if drift.Code != http.StatusBadRequest {
		t.Fatalf("drift status=%d body=%s", drift.Code, drift.Body.String())
	}
	nonempty := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/v1/reference-capacity-runs/"+status.RunID+"/abort", bytes.NewBufferString(`{}`))
	request.Header.Set("Authorization", "Bearer controller-secret-token")
	handler.ServeHTTP(nonempty, request)
	if nonempty.Code != http.StatusBadRequest {
		t.Fatalf("nonempty abort status=%d body=%s", nonempty.Code, nonempty.Body.String())
	}
	_, _ = manager.Abort(status.RunID)
}

func TestDatasetFixesFiveEqualTenantsAndHotUser(t *testing.T) {
	profile := testProfile()
	dataset := testDataset(strings.Repeat("b", 40))
	if err := dataset.Validate(dataset.SourceCommit, profile); err != nil {
		t.Fatal(err)
	}
	dataset.Principals[0].ConnectionCount--
	dataset.Principals[1].ConnectionCount++
	if err := dataset.Validate(dataset.SourceCommit, profile); err == nil {
		t.Fatal("unequal tenant load share was accepted")
	}
}

func testProfile() Profile {
	vectors := map[string]float64{
		"concurrentConnections": 10000, "apiRequestsPerSecond": 500, "eventAppendsPerSecond": 2000,
		"activeRuns": 10000, "concurrentProviderRequests": 200, "activeRuntimeSessions": 500,
		"tokensPerSecond": 100000, "artifactMiBPerSecond": 500, "retentionTiBPerDay": 1,
		"hotTenantCapacityPercent": 20, "hotTenantUserEventAppendsPerSecond": 100,
		"leaseHeartbeatsPerSecond": 1000, "runReplaysPerSecond": 100,
	}
	slos := map[string]float64{}
	for index := 0; index < 18; index++ {
		slos[string(rune('a'+index))] = 1
	}
	return Profile{ProfileVersion: ProfileVersion, ProfileID: ProfileID, DurationSeconds: 1800, SampleIntervalSeconds: 15, MinimumObservationsPerMeasure: 116, Vectors: vectors, SLOs: slos, ZeroTolerance: []string{"a", "b", "c", "d", "e"}}
}

func testDataset(sourceCommit string) Dataset {
	principals := make([]Principal, 0, 6)
	for tenant := 0; tenant < 5; tenant++ {
		principal := Principal{Label: "principal-" + string(rune('a'+tenant)), TenantBucket: "tenant-" + string(rune('a'+tenant)), SessionTokenFile: "/run/secrets/session-" + string(rune('a'+tenant)), CSRFTokenFile: "/run/secrets/csrf-" + string(rune('a'+tenant)), ConnectionCount: 2000, Conversations: []Conversation{{ID: "conversation-" + string(rune('a'+tenant)), Version: 1, Scenario: "coach"}}}
		if tenant == 0 {
			principal.HotUser = true
			principal.Conversations = make([]Conversation, 34)
			for index := range principal.Conversations {
				principal.Conversations[index] = Conversation{ID: fmt.Sprintf("hot-conversation-%02d", index), Version: 1, Scenario: "coach"}
			}
		}
		principals = append(principals, principal)
	}
	principals = append(principals, Principal{Label: "principal-cold", TenantBucket: "tenant-a", SessionTokenFile: "/run/secrets/session-cold", CSRFTokenFile: "/run/secrets/csrf-cold", Conversations: []Conversation{{ID: "cold-conversation", Version: 1, Scenario: "coach"}}})
	return Dataset{DatasetVersion: DatasetVersion, DatasetID: "reference-dataset", ProfileID: ProfileID, SourceCommit: sourceCommit, Principals: principals}
}
