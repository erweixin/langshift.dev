package reference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/langshift/lites/internal/identity/session"
	"github.com/langshift/lites/internal/security/transport"
)

type LoadMetrics interface {
	SetHotTenantPercent(float64)
	AddHotUserEventAppends(context.Context, int64)
}

type WorkloadConfig struct {
	GatewayURL, PublicOrigin string
	Client                   *http.Client
	Profile                  Profile
	Dataset                  LoadedDataset
	RunID                    string
	WarmupDuration           time.Duration
	RequestTimeout           time.Duration
	MaximumInflight          int
	MaximumErrorRatio        float64
	Metrics                  LoadMetrics
}

type Workload struct {
	configuration    WorkloadConfig
	ready            chan struct{}
	errors           chan error
	stop             context.CancelFunc
	started          atomic.Bool
	readyOnce        sync.Once
	stopOnce         sync.Once
	connections      atomic.Int64
	attempts         atomic.Int64
	accepted         atomic.Int64
	apiFailures      atomic.Int64
	realtimeFailures atomic.Int64
	mu               sync.Mutex
	acceptedByTen    map[string]int64
}

type loadPrincipal struct {
	Principal
	sessionToken string
	csrfToken    string
}

type messageSlot struct {
	principal *loadPrincipal
	tenant    string
	id        string
	scenario  string
	mu        sync.Mutex
	version   uint64
}

func NewWorkload(configuration WorkloadConfig) (*Workload, error) {
	endpoint, err := url.Parse(configuration.GatewayURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") || configuration.PublicOrigin == "" || configuration.Client == nil || configuration.RunID == "" || configuration.Profile.Validate() != nil || configuration.Dataset.Validate(configuration.Dataset.SourceCommit, configuration.Profile) != nil || configuration.WarmupDuration < 30*time.Second || configuration.WarmupDuration > 15*time.Minute || configuration.RequestTimeout < time.Second || configuration.RequestTimeout > time.Minute || configuration.MaximumInflight < int(configuration.Profile.Vectors["apiRequestsPerSecond"]) || configuration.MaximumInflight > 100000 || configuration.MaximumErrorRatio < 0 || configuration.MaximumErrorRatio > .001 || configuration.Metrics == nil {
		return nil, ErrInvalid
	}
	origin, err := url.Parse(configuration.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return nil, ErrInvalid
	}
	return &Workload{configuration: configuration, ready: make(chan struct{}), errors: make(chan error, 1), acceptedByTen: map[string]int64{}}, nil
}

func (workload *Workload) Ready() <-chan struct{} { return workload.ready }
func (workload *Workload) Errors() <-chan error   { return workload.errors }

func (workload *Workload) Start(parent context.Context) error {
	if !workload.started.CompareAndSwap(false, true) {
		return ErrInvalid
	}
	principals, slotsByTenant, hotTenant, err := workload.loadPrincipals()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(parent)
	workload.stop = cancel
	for index := range principals {
		principal := principals[index]
		for connection := 0; connection < principal.ConnectionCount; connection++ {
			go workload.maintainRealtime(ctx, principal)
		}
	}
	go workload.runMessages(ctx, slotsByTenant, hotTenant)
	go workload.monitor(ctx)
	return nil
}

func (workload *Workload) Stop() {
	workload.stopOnce.Do(func() {
		if workload.stop != nil {
			workload.stop()
		}
	})
}

func (workload *Workload) loadPrincipals() ([]*loadPrincipal, map[string][]*messageSlot, string, error) {
	principals := make([]*loadPrincipal, 0, len(workload.configuration.Dataset.Principals))
	slotsByTenant := map[string][]*messageSlot{}
	hotTenant := ""
	for _, source := range workload.configuration.Dataset.Principals {
		sessionToken, err := readSecret(source.SessionTokenFile)
		if err != nil {
			return nil, nil, "", fmt.Errorf("load session credential for %s: %w", source.Label, err)
		}
		csrfToken, err := readSecret(source.CSRFTokenFile)
		if err != nil {
			return nil, nil, "", fmt.Errorf("load CSRF credential for %s: %w", source.Label, err)
		}
		principal := &loadPrincipal{Principal: source, sessionToken: sessionToken, csrfToken: csrfToken}
		principals = append(principals, principal)
		if source.HotUser {
			hotTenant = source.TenantBucket
		}
		for _, conversation := range source.Conversations {
			slotsByTenant[source.TenantBucket] = append(slotsByTenant[source.TenantBucket], &messageSlot{principal: principal, tenant: source.TenantBucket, id: conversation.ID, scenario: conversation.Scenario, version: conversation.Version})
		}
	}
	if hotTenant == "" || len(slotsByTenant) != 5 {
		return nil, nil, "", ErrInvalid
	}
	return principals, slotsByTenant, hotTenant, nil
}

func (workload *Workload) maintainRealtime(ctx context.Context, principal *loadPrincipal) {
	for ctx.Err() == nil {
		ready, err := workload.realtimeOnce(ctx, principal)
		if ready {
			workload.connections.Add(-1)
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			workload.realtimeFailures.Add(1)
		}
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (workload *Workload) realtimeOnce(ctx context.Context, principal *loadPrincipal) (bool, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(workload.configuration.GatewayURL, "/")+"/v1/realtime", nil)
	if err != nil {
		return false, err
	}
	request.AddCookie(&http.Cookie{Name: session.CookieName, Value: principal.sessionToken})
	request.Header.Set("Accept", "text/event-stream")
	request.Header.Set("User-Agent", "lites-reference-capacity/1.0")
	response, err := workload.configuration.Client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return false, fmt.Errorf("realtime returned HTTP %d", response.StatusCode)
	}
	reader := bufio.NewReaderSize(response.Body, 16<<10)
	ready := false
	for {
		line, readErr := reader.ReadString('\n')
		if len(line) > 64<<10 {
			return ready, errors.New("realtime frame exceeds limit")
		}
		if strings.HasPrefix(line, "data:") && strings.Contains(line, `"kind":"ready"`) && !ready {
			ready = true
			workload.connections.Add(1)
		}
		if readErr != nil {
			if ctx.Err() != nil {
				return ready, nil
			}
			return ready, readErr
		}
	}
}

func (workload *Workload) runMessages(ctx context.Context, slotsByTenant map[string][]*messageSlot, hotTenant string) {
	tenantNames := workload.configuration.Dataset.SortedTenantBuckets()
	// A two-percent dispatch margin keeps every one-minute Prometheus rate
	// above the contractual minimum despite scheduler and scrape boundaries.
	targetRate := int64(math.Ceil(workload.configuration.Profile.Vectors["apiRequestsPerSecond"] * 1.02))
	semaphore := make(chan struct{}, workload.configuration.MaximumInflight)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	started := time.Now()
	var launched int64
	tenantIndexes := map[string]uint64{}
	hotTenantRequests := uint64(0)
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			due := int64(math.Floor(now.Sub(started).Seconds() * float64(targetRate)))
			if due-launched > targetRate {
				due = launched + targetRate
			}
			for launched < due {
				tenant := tenantNames[launched%int64(len(tenantNames))]
				candidates := slotsByTenant[tenant]
				if tenant == hotTenant {
					hotTenantRequests++
					if hotTenantRequests*34/100 > (hotTenantRequests-1)*34/100 {
						candidates = hotUserSlots(candidates)
					} else {
						candidates = nonHotUserSlots(candidates)
					}
				}
				if len(candidates) == 0 {
					workload.fail(errors.New("fixed tenant has no eligible conversation slot"))
					return
				}
				index := tenantIndexes[tenant] % uint64(len(candidates))
				tenantIndexes[tenant]++
				slot := candidates[index]
				sequence := launched + 1
				launched++
				select {
				case semaphore <- struct{}{}:
					go func() {
						defer func() { <-semaphore }()
						workload.sendMessage(ctx, slot, sequence)
					}()
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func hotUserSlots(slots []*messageSlot) []*messageSlot {
	result := make([]*messageSlot, 0, len(slots))
	for _, slot := range slots {
		if slot.principal.HotUser {
			result = append(result, slot)
		}
	}
	return result
}

func nonHotUserSlots(slots []*messageSlot) []*messageSlot {
	result := make([]*messageSlot, 0, len(slots))
	for _, slot := range slots {
		if !slot.principal.HotUser {
			result = append(result, slot)
		}
	}
	return result
}

func (workload *Workload) sendMessage(ctx context.Context, slot *messageSlot, sequence int64) {
	slot.mu.Lock()
	defer slot.mu.Unlock()
	workload.attempts.Add(1)
	requestID := fmt.Sprintf("capacity-%s-%012d", workload.configuration.RunID, sequence)
	body, _ := json.Marshal(map[string]any{
		"request_id": requestID, "conversation_id": slot.id,
		"content": "[lites-reference-capacity:" + slot.scenario + "] Execute the immutable reference scenario.",
		"mode":    "enqueue", "expected_conversation_version": slot.version,
	})
	for attempt := 0; attempt < 3; attempt++ {
		requestCtx, cancel := context.WithTimeout(ctx, workload.configuration.RequestTimeout)
		request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, strings.TrimRight(workload.configuration.GatewayURL, "/")+"/v1/messages", bytes.NewReader(body))
		if err == nil {
			request.AddCookie(&http.Cookie{Name: session.CookieName, Value: slot.principal.sessionToken})
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Accept", "application/json")
			request.Header.Set("Origin", workload.configuration.PublicOrigin)
			request.Header.Set(transport.CSRFHeader, slot.principal.csrfToken)
			request.Header.Set(transport.IdempotencyHeader, requestID)
			request.Header.Set("If-Match", `"`+strconv.FormatUint(slot.version, 10)+`"`)
			request.Header.Set("User-Agent", "lites-reference-capacity/1.0")
			var response *http.Response
			response, err = workload.configuration.Client.Do(request)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
				_ = response.Body.Close()
				if response.StatusCode == http.StatusAccepted {
					version, versionErr := parseETag(response.Header.Get("ETag"))
					if versionErr == nil && version == slot.version+1 {
						slot.version = version
						workload.recordAccepted(ctx, slot.tenant, slot.principal.HotUser)
						cancel()
						return
					}
					err = errors.New("message response ETag is invalid")
				} else if response.StatusCode >= 400 && response.StatusCode < 500 && response.StatusCode != http.StatusTooManyRequests {
					err = fmt.Errorf("message request rejected with HTTP %d", response.StatusCode)
					cancel()
					break
				} else {
					err = fmt.Errorf("message request failed with HTTP %d", response.StatusCode)
				}
			}
		}
		cancel()
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(attempt+1) * 50 * time.Millisecond):
			}
		}
	}
	workload.apiFailures.Add(1)
}

func (workload *Workload) recordAccepted(ctx context.Context, tenant string, hotUser bool) {
	workload.accepted.Add(1)
	workload.mu.Lock()
	workload.acceptedByTen[tenant]++
	workload.mu.Unlock()
	if hotUser {
		// MessageAppended, RunAccepted, and RunQueued are committed atomically.
		workload.configuration.Metrics.AddHotUserEventAppends(ctx, 3)
	}
}

func (workload *Workload) monitor(ctx context.Context) {
	interval := time.Duration(workload.configuration.Profile.SampleIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	warmupStarted := time.Time{}
	var previousAttempts, previousAccepted, previousFailed int64
	unhealthyIntervals := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			workload.publishTenantShare()
			connections := workload.connections.Load()
			attempts, accepted, failed := workload.attempts.Load(), workload.accepted.Load(), workload.apiFailures.Load()
			if connections == int64(workload.configuration.Profile.Vectors["concurrentConnections"]) && workload.accepted.Load() >= int64(workload.configuration.Profile.Vectors["apiRequestsPerSecond"])*10 {
				if warmupStarted.IsZero() {
					warmupStarted = time.Now()
				}
				if time.Since(warmupStarted) >= workload.configuration.WarmupDuration {
					workload.readyOnce.Do(func() { close(workload.ready) })
				}
			} else {
				warmupStarted = time.Time{}
			}
			if workload.readyClosed() {
				deltaAttempts, deltaAccepted, deltaFailed := attempts-previousAttempts, accepted-previousAccepted, failed-previousFailed
				minimumAccepted := int64(workload.configuration.Profile.Vectors["apiRequestsPerSecond"]) * int64(workload.configuration.Profile.SampleIntervalSeconds)
				unhealthy := connections != int64(workload.configuration.Profile.Vectors["concurrentConnections"]) || deltaAttempts < minimumAccepted || deltaAccepted < minimumAccepted || deltaAttempts > 0 && float64(deltaFailed)/float64(deltaAttempts) > workload.configuration.MaximumErrorRatio
				if unhealthy {
					unhealthyIntervals++
				} else {
					unhealthyIntervals = 0
				}
				if unhealthyIntervals >= 1 {
					workload.fail(errors.New("reference load left the fixed connection or API error envelope"))
					return
				}
			}
			previousAttempts, previousAccepted, previousFailed = attempts, accepted, failed
		}
	}
}

func (workload *Workload) publishTenantShare() {
	workload.mu.Lock()
	defer workload.mu.Unlock()
	var total, maximum int64
	for tenant, count := range workload.acceptedByTen {
		total += count
		if count > maximum {
			maximum = count
		}
		workload.acceptedByTen[tenant] = 0
	}
	if total > 0 {
		workload.configuration.Metrics.SetHotTenantPercent(float64(maximum) * 100 / float64(total))
	}
}

func (workload *Workload) readyClosed() bool {
	select {
	case <-workload.ready:
		return true
	default:
		return false
	}
}

func (workload *Workload) fail(err error) {
	select {
	case workload.errors <- err:
	default:
	}
	workload.Stop()
}

func readSecret(path string) (string, error) {
	raw, err := readStrictFile(path, 8192)
	value := strings.TrimSpace(string(raw))
	if err != nil || value == "" || strings.ContainsAny(value, "\x00\r\n") {
		return "", ErrInvalid
	}
	return value, nil
}

func parseETag(value string) (uint64, error) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value[1:len(value)-1], "\"") {
		return 0, ErrInvalid
	}
	return strconv.ParseUint(value[1:len(value)-1], 10, 64)
}
