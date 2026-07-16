package reference

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

type StartCommand struct {
	ProfileID       string `json:"profileId"`
	ProfileSHA256   string `json:"profileSha256"`
	SourceCommit    string `json:"sourceCommit"`
	DurationSeconds int    `json:"durationSeconds"`
}

type Status struct {
	RunID           string     `json:"runId"`
	ProfileID       string     `json:"profileId"`
	SourceCommit    string     `json:"sourceCommit"`
	Status          string     `json:"status"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	CompletedAt     *time.Time `json:"completedAt,omitempty"`
	LastHeartbeatAt time.Time  `json:"lastHeartbeatAt"`
	DatasetSHA256   string     `json:"datasetSha256"`
}

type ManagedWorkload interface {
	Start(context.Context) error
	Ready() <-chan struct{}
	Errors() <-chan error
	Stop()
}

type WorkloadFactory func(string) (ManagedWorkload, error)

type Manager struct {
	Profile       Profile
	ProfileSHA256 string
	SourceCommit  string
	DatasetSHA256 string
	Factory       WorkloadFactory
	Now           func() time.Time
	Context       context.Context

	mu     sync.Mutex
	active *managedRun
}

type managedRun struct {
	command  StartCommand
	status   Status
	workload ManagedWorkload
	ctx      context.Context
	cancel   context.CancelFunc
}

var (
	ErrConflict = errors.New("reference capacity run conflicts with existing state")
	ErrNotFound = errors.New("reference capacity run was not found")
	ErrTooEarly = errors.New("reference capacity run has not reached its duration")
)

func (manager *Manager) Start(ctx context.Context, command StartCommand) (Status, error) {
	if manager.Profile.Validate() != nil || manager.ProfileSHA256 == "" || manager.DatasetSHA256 == "" || !commitPattern.MatchString(manager.SourceCommit) || manager.Factory == nil || command.ProfileID != manager.Profile.ProfileID || command.ProfileSHA256 != manager.ProfileSHA256 || command.SourceCommit != manager.SourceCommit || command.DurationSeconds != manager.Profile.DurationSeconds {
		return Status{}, ErrInvalid
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active != nil {
		if manager.active.command == command && (manager.active.status.Status == "warming" || manager.active.status.Status == "running") {
			return manager.snapshotLocked(manager.active), nil
		}
		return Status{}, ErrConflict
	}
	runID, err := randomRunID()
	if err != nil {
		return Status{}, err
	}
	workload, err := manager.Factory(runID)
	if err != nil {
		return Status{}, err
	}
	baseContext := manager.Context
	if baseContext == nil {
		baseContext = context.Background()
	}
	runCtx, cancel := context.WithCancel(baseContext)
	run := &managedRun{command: command, workload: workload, ctx: runCtx, cancel: cancel, status: Status{RunID: runID, ProfileID: command.ProfileID, SourceCommit: command.SourceCommit, Status: "warming", LastHeartbeatAt: manager.now(), DatasetSHA256: manager.DatasetSHA256}}
	if err = workload.Start(runCtx); err != nil {
		cancel()
		return Status{}, err
	}
	manager.active = run
	go manager.supervise(run)
	return manager.snapshotLocked(run), nil
}

func (manager *Manager) Get(runID string) (Status, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active == nil || manager.active.status.RunID != runID {
		return Status{}, ErrNotFound
	}
	manager.active.status.LastHeartbeatAt = manager.now()
	return manager.snapshotLocked(manager.active), nil
}

func (manager *Manager) Complete(runID string) (Status, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active == nil || manager.active.status.RunID != runID {
		return Status{}, ErrNotFound
	}
	run := manager.active
	if run.status.Status == "completed" {
		return manager.snapshotLocked(run), nil
	}
	if run.status.Status != "running" || run.status.StartedAt == nil {
		return Status{}, ErrConflict
	}
	now := manager.now()
	if now.Sub(*run.status.StartedAt) < time.Duration(run.command.DurationSeconds)*time.Second {
		return Status{}, ErrTooEarly
	}
	run.workload.Stop()
	run.cancel()
	run.status.Status = "completed"
	run.status.CompletedAt = &now
	run.status.LastHeartbeatAt = now
	return manager.snapshotLocked(run), nil
}

func (manager *Manager) Abort(runID string) (Status, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active == nil || manager.active.status.RunID != runID {
		return Status{}, ErrNotFound
	}
	run := manager.active
	if run.status.Status != "completed" && run.status.Status != "aborted" {
		run.workload.Stop()
		run.cancel()
		now := manager.now()
		run.status.Status = "aborted"
		run.status.CompletedAt = &now
		run.status.LastHeartbeatAt = now
	}
	return manager.snapshotLocked(run), nil
}

func (manager *Manager) supervise(run *managedRun) {
	select {
	case <-run.ctx.Done():
		return
	case <-run.workload.Ready():
		manager.mu.Lock()
		if manager.active == run && run.status.Status == "warming" {
			now := manager.now()
			run.status.Status = "running"
			run.status.StartedAt = &now
			run.status.LastHeartbeatAt = now
		}
		manager.mu.Unlock()
	case <-run.workload.Errors():
		manager.failRun(run)
		return
	}
	select {
	case <-run.ctx.Done():
		return
	case <-run.workload.Errors():
		manager.failRun(run)
	}
}

func (manager *Manager) failRun(run *managedRun) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.active != run || run.status.Status == "completed" || run.status.Status == "aborted" {
		return
	}
	run.workload.Stop()
	run.cancel()
	now := manager.now()
	run.status.Status = "failed"
	run.status.CompletedAt = &now
	run.status.LastHeartbeatAt = now
}

func (manager *Manager) snapshotLocked(run *managedRun) Status {
	result := run.status
	if result.StartedAt != nil {
		value := *result.StartedAt
		result.StartedAt = &value
	}
	if result.CompletedAt != nil {
		value := *result.CompletedAt
		result.CompletedAt = &value
	}
	return result
}

func (manager *Manager) now() time.Time {
	if manager.Now != nil {
		return manager.Now().UTC()
	}
	return time.Now().UTC()
}

func randomRunID() (string, error) {
	raw := make([]byte, 18)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "capacity-" + base64.RawURLEncoding.EncodeToString(raw), nil
}

type Handler struct {
	Manager     *Manager
	BearerToken string
}

func (handler Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Cache-Control", "no-store")
	if handler.Manager == nil || handler.BearerToken == "" {
		handler.problem(writer, http.StatusServiceUnavailable, "controller_unavailable")
		return
	}
	provided := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	if len(provided) != len(handler.BearerToken) || subtle.ConstantTimeCompare([]byte(provided), []byte(handler.BearerToken)) != 1 {
		writer.Header().Set("WWW-Authenticate", `Bearer realm="reference-capacity"`)
		handler.problem(writer, http.StatusUnauthorized, "authentication_required")
		return
	}
	if request.URL.Path == "/v1/reference-capacity-runs" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			handler.problem(writer, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		var command StartCommand
		if !decodeControlRequest(writer, request, &command) {
			return
		}
		status, err := handler.Manager.Start(request.Context(), command)
		handler.writeResult(writer, status, err)
		return
	}
	runID, action, ok := parseRunRoute(request.URL.Path)
	if !ok {
		handler.problem(writer, http.StatusNotFound, "not_found")
		return
	}
	var status Status
	var err error
	switch action {
	case "get":
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			handler.problem(writer, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		status, err = handler.Manager.Get(runID)
	case "complete", "abort":
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			handler.problem(writer, http.StatusMethodNotAllowed, "method_not_allowed")
			return
		}
		if !emptyRequestBody(request) {
			handler.problem(writer, http.StatusBadRequest, "validation_failed")
			return
		}
		if action == "complete" {
			status, err = handler.Manager.Complete(runID)
		} else {
			status, err = handler.Manager.Abort(runID)
		}
	}
	handler.writeResult(writer, status, err)
}

func emptyRequestBody(request *http.Request) bool {
	if request.Body == nil || request.Body == http.NoBody {
		return true
	}
	defer request.Body.Close()
	var byteBuffer [1]byte
	count, err := request.Body.Read(byteBuffer[:])
	return count == 0 && errors.Is(err, io.EOF)
}

func (handler Handler) writeResult(writer http.ResponseWriter, status Status, err error) {
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalid):
			handler.problem(writer, http.StatusBadRequest, "validation_failed")
		case errors.Is(err, ErrNotFound):
			handler.problem(writer, http.StatusNotFound, "not_found")
		case errors.Is(err, ErrConflict), errors.Is(err, ErrTooEarly):
			handler.problem(writer, http.StatusConflict, "state_conflict")
		default:
			handler.problem(writer, http.StatusServiceUnavailable, "controller_unavailable")
		}
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(status)
}

func (handler Handler) problem(writer http.ResponseWriter, status int, code string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(map[string]any{"status": status, "code": code})
}

func decodeControlRequest(writer http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		Handler{}.problem(writer, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, 4096)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(target); err != nil {
		Handler{}.problem(writer, http.StatusBadRequest, "validation_failed")
		return false
	}
	if err = decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		Handler{}.problem(writer, http.StatusBadRequest, "validation_failed")
		return false
	}
	return true
}

func parseRunRoute(path string) (string, string, bool) {
	const prefix = "/v1/reference-capacity-runs/"
	if !strings.HasPrefix(path, prefix) {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(path, prefix), "/")
	if len(parts) == 1 && idPattern.MatchString(parts[0]) {
		return parts[0], "get", true
	}
	if len(parts) == 2 && idPattern.MatchString(parts[0]) && (parts[1] == "complete" || parts[1] == "abort") {
		return parts[0], parts[1], true
	}
	return "", "", false
}
