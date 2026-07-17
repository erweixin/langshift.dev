// Package erasure coordinates durable, cross-surface account deletion.
package erasure

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	ErrInvalid   = errors.New("account erasure input is invalid")
	ErrNotDue    = errors.New("account erasure request is not due")
	ErrCancelled = errors.New("account erasure request is cancelled")
)

type Surface string

const (
	Payload           Surface = "payload"
	Memory            Surface = "memory"
	Indexes           Surface = "indexes"
	WorkspaceArtifact Surface = "workspace_artifact"
	Cache             Surface = "cache"
	Snapshot          Surface = "snapshot"
)

var RequiredSurfaces = []Surface{Payload, Memory, Indexes, WorkspaceArtifact, Cache, Snapshot}

type Request struct {
	ID, TenantID, UserID string
	Version              uint64
	Status               string
	ScheduledFor         time.Time
}

type Receipt struct {
	ID, RequestID, TenantID, UserID, RecoveryEpoch, Hash string
	Surface                                              Surface
	Details                                              json.RawMessage
	ErasedAt                                             time.Time
}

type Result struct {
	RequestID, RecoveryEpoch, ManifestHash string
	Receipts                               []Receipt
	Completed, Replayed, RestoreReplay     bool
}

type Store interface {
	Claim(context.Context, string, string, time.Time) (Request, bool, error)
	LoadReceipt(context.Context, string, string, Surface, string) (Receipt, bool, error)
	RecordReceipt(context.Context, Receipt) (Receipt, bool, error)
	Complete(context.Context, Request, string, []Receipt, time.Time) (string, error)
	PendingRestore(context.Context, string, int) ([]Request, error)
}

type SurfaceEraser interface {
	Erase(context.Context, Request, string) (json.RawMessage, error)
}

type Service struct {
	Store         Store
	Erasers       map[Surface]SurfaceEraser
	RecoveryEpoch string
	ReceiptKey    []byte
	Now           func() time.Time
}

func (service Service) Process(ctx context.Context, tenantID, requestID string) (Result, error) {
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	if err := service.valid(); err != nil || tenantID == "" || requestID == "" {
		return Result{}, ErrInvalid
	}
	request, completed, err := service.Store.Claim(ctx, tenantID, requestID, now)
	if err != nil {
		return Result{}, err
	}
	result, err := service.process(ctx, request, completed, now)
	if err == nil && completed {
		result.Replayed = true
	}
	return result, err
}

// ReconcileRestore re-applies every tombstone that does not yet have all six
// receipts for the current Store Epoch. Restoring a backup can therefore never
// resurrect readable content merely because its original deletion predated it.
func (service Service) ReconcileRestore(ctx context.Context, limit int) ([]Result, error) {
	if err := service.valid(); err != nil || limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	requests, err := service.Store.PendingRestore(ctx, service.RecoveryEpoch, limit)
	if err != nil {
		return nil, err
	}
	results := make([]Result, 0, len(requests))
	now := time.Now().UTC()
	if service.Now != nil {
		now = service.Now().UTC()
	}
	for _, request := range requests {
		result, processErr := service.process(ctx, request, true, now)
		if processErr != nil {
			return results, processErr
		}
		result.RestoreReplay = true
		results = append(results, result)
	}
	return results, nil
}

func (service Service) process(ctx context.Context, request Request, restore bool, now time.Time) (Result, error) {
	if request.ID == "" || request.TenantID == "" || request.UserID == "" || request.Version == 0 || (!restore && request.Status != "processing" && request.Status != "completed") || restore && request.Status != "completed" {
		return Result{}, ErrInvalid
	}
	receipts := make([]Receipt, 0, len(RequiredSurfaces))
	replayed := true
	for _, surface := range RequiredSurfaces {
		receipt, exists, err := service.Store.LoadReceipt(ctx, request.TenantID, request.ID, surface, service.RecoveryEpoch)
		if err != nil {
			return Result{}, err
		}
		if exists {
			receipts = append(receipts, receipt)
			continue
		}
		replayed = false
		details, eraseErr := service.Erasers[surface].Erase(ctx, request, service.RecoveryEpoch)
		if eraseErr != nil || !validDetails(details) {
			if eraseErr != nil {
				return Result{}, eraseErr
			}
			return Result{}, ErrInvalid
		}
		receipt = Receipt{RequestID: request.ID, TenantID: request.TenantID, UserID: request.UserID, RecoveryEpoch: service.RecoveryEpoch, Surface: surface, Details: details, ErasedAt: now}
		receipt.Hash = service.receiptHash(receipt)
		receipt, _, err = service.Store.RecordReceipt(ctx, receipt)
		if err != nil {
			return Result{}, err
		}
		receipts = append(receipts, receipt)
	}
	manifest := manifestHash(receipts)
	if !restore {
		completedHash, err := service.Store.Complete(ctx, request, service.RecoveryEpoch, receipts, now)
		if err != nil {
			return Result{}, err
		}
		if completedHash != manifest {
			return Result{}, ErrInvalid
		}
	}
	return Result{RequestID: request.ID, RecoveryEpoch: service.RecoveryEpoch, ManifestHash: manifest, Receipts: receipts, Completed: true, Replayed: replayed}, nil
}

func (service Service) valid() error {
	if service.Store == nil || len(service.ReceiptKey) < 32 || service.RecoveryEpoch == "" || len(service.Erasers) != len(RequiredSurfaces) {
		return ErrInvalid
	}
	for _, surface := range RequiredSurfaces {
		if service.Erasers[surface] == nil {
			return ErrInvalid
		}
	}
	return nil
}

func (service Service) receiptHash(receipt Receipt) string {
	digest := hmac.New(sha256.New, service.ReceiptKey)
	_, _ = digest.Write([]byte("lites-account-erasure-receipt-v1\x00"))
	_, _ = digest.Write([]byte(receipt.TenantID + "\x00" + receipt.UserID + "\x00" + receipt.RequestID + "\x00" + receipt.RecoveryEpoch + "\x00" + string(receipt.Surface) + "\x00"))
	_, _ = digest.Write(receipt.Details)
	return hex.EncodeToString(digest.Sum(nil))
}

func manifestHash(receipts []Receipt) string {
	ordered := append([]Receipt(nil), receipts...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Surface < ordered[j].Surface })
	digest := sha256.New()
	_, _ = digest.Write([]byte("lites-account-erasure-manifest-v1\x00"))
	for _, receipt := range ordered {
		_, _ = fmt.Fprintf(digest, "%s\x00%s\x00", receipt.Surface, receipt.Hash)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func validDetails(details json.RawMessage) bool {
	if len(details) < 2 || len(details) > 64<<10 || !json.Valid(details) {
		return false
	}
	var value map[string]any
	return json.Unmarshal(details, &value) == nil && value != nil
}
