package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrValidation           = errors.New("contract command validation failed")
	ErrPermissionDenied     = errors.New("contract permission denied")
	ErrReauthentication     = errors.New("recent reauthentication required")
	ErrResourceNotFound     = errors.New("contract resource not found")
	ErrStateConflict        = errors.New("contract state conflict")
	ErrIdempotencyConflict  = errors.New("contract idempotency conflict")
	ErrApprovalScopeChanged = errors.New("contract approval scope changed")
)

type CommandMetadata struct {
	RequestID, ClientRequestID, IdempotencyKey string
	TenantID, UserID, MembershipID, SessionID  string
}

type ContractProposalCommand struct {
	CommandMetadata
	Action                           string
	TargetContractID, ContractNumber string
	TargetVersion                    uint64
	StartsAt, EndsAt                 *time.Time
	SeatLimit                        int
	Region, LicenseKind, Reason      string
}

type ContractDecisionCommand struct {
	CommandMetadata
	ProposalID, Decision, ProposalHash string
	TargetVersion                      uint64
	ExpectedProposalVersion            uint64
}

type AdjustmentProposalCommand struct {
	CommandMetadata
	BucketID, Reason string
	TargetVersion    uint64
	Units            int64
}

type AdjustmentDecisionCommand struct {
	CommandMetadata
	ProposalID, Decision, ProposalHash string
	TargetVersion                      uint64
	ExpectedProposalVersion            uint64
}

type EntitlementProposalCommand struct {
	CommandMetadata
	ContractID               string
	TargetContractVersion    uint64
	EntitlementKey           string
	TargetEntitlementVersion uint64
	LimitValue               *int64
	Config                   json.RawMessage
	Reason                   string
}

type EntitlementDecisionCommand struct {
	CommandMetadata
	ProposalID, Decision, ProposalHash string
	TargetContractVersion              uint64
	TargetEntitlementVersion           uint64
	ExpectedProposalVersion            uint64
}

type UsageQuery struct {
	RequestID, TenantID, UserID, MembershipID, SessionID, Reason string
}

type AuditQuery struct {
	RequestID, TenantID, UserID, MembershipID, SessionID, Reason string
	Before                                                       string
	Limit                                                        int
}

type AuditRecord struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	Action        string    `json:"action"`
	ActorUserID   string    `json:"actor_user_id"`
	ResourceKind  string    `json:"resource_kind"`
	ResourceID    string    `json:"resource_id"`
	ReasonHash    string    `json:"reason_hash"`
	BeforeVersion *uint64   `json:"before_version"`
	AfterVersion  *uint64   `json:"after_version"`
	OccurredAt    time.Time `json:"occurred_at"`
}

type AuditPage struct {
	Items      []AuditRecord `json:"items"`
	NextBefore string        `json:"next_before,omitempty"`
}

type AuditExportCommand struct {
	CommandMetadata
	PeriodStart, PeriodEnd time.Time
	Kinds                  []string
	Format, Reason         string
}

type AuditExportQuery struct {
	RequestID, TenantID, UserID, MembershipID, SessionID, ExportID, Reason string
}

type AuditExport struct {
	ID          string    `json:"id"`
	Version     uint64    `json:"version"`
	Status      string    `json:"status"`
	Format      string    `json:"format"`
	RecordCount int       `json:"record_count"`
	ContentHash string    `json:"content_hash"`
	ByteSize    int64     `json:"byte_size"`
	PeriodStart time.Time `json:"period_start"`
	PeriodEnd   time.Time `json:"period_end"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Replayed    bool      `json:"replayed,omitempty"`
}

type AuditExportDownload struct {
	AuditExport
	Content []byte
}

type Resource struct {
	ID            string    `json:"id"`
	Version       uint64    `json:"version"`
	Status        string    `json:"status"`
	ProposalHash  string    `json:"proposal_hash"`
	TargetID      string    `json:"target_id"`
	TargetVersion uint64    `json:"target_version"`
	ApprovalCount int       `json:"approval_count"`
	UpdatedAt     time.Time `json:"updated_at"`
	Replayed      bool      `json:"replayed"`
}

type UsageSnapshot struct {
	TenantID       string    `json:"tenant_id"`
	GrantedUnits   int64     `json:"granted_units"`
	AvailableUnits int64     `json:"available_units"`
	ReservedUnits  int64     `json:"reserved_units"`
	SettledUnits   int64     `json:"settled_units"`
	ActiveSeats    int       `json:"active_seats"`
	SeatLimit      int       `json:"seat_limit"`
	AsOf           time.Time `json:"as_of"`
}

type Service interface {
	ProposeContract(context.Context, ContractProposalCommand) (Resource, error)
	DecideContract(context.Context, ContractDecisionCommand) (Resource, error)
	ProposeAdjustment(context.Context, AdjustmentProposalCommand) (Resource, error)
	DecideAdjustment(context.Context, AdjustmentDecisionCommand) (Resource, error)
	ProposeEntitlement(context.Context, EntitlementProposalCommand) (Resource, error)
	DecideEntitlement(context.Context, EntitlementDecisionCommand) (Resource, error)
	ReadUsage(context.Context, UsageQuery) (UsageSnapshot, error)
	ReadAudit(context.Context, AuditQuery) (AuditPage, error)
	RequestAuditExport(context.Context, AuditExportCommand) (AuditExport, error)
	ReadAuditExport(context.Context, AuditExportQuery) (AuditExportDownload, error)
}

type InternalUsageReserveCommand struct {
	RequestID, IdempotencyKey, WorkloadIdentity string
	TenantID, UserID, OperationKey              string
	SubjectKind, SubjectID                      string
	SubjectVersion, RequestedUnits              uint64
	BYOK                                        bool
}

type InternalUsageSettleCommand struct {
	RequestID, IdempotencyKey, WorkloadIdentity string
	TenantID, ReservationID, ProviderAttemptID  string
	ActualUnits, ProviderCostMicrounits         uint64
	ExpectedVersion                             uint64
}

type InternalUsageReleaseCommand struct {
	RequestID, IdempotencyKey, WorkloadIdentity string
	TenantID, ReservationID, Reason             string
	ExpectedVersion                             uint64
}

type InternalUsageResult struct {
	ReservationID string    `json:"reservation_id"`
	Status        string    `json:"status"`
	ReservedUnits uint64    `json:"reserved_units,omitempty"`
	SettledUnits  uint64    `json:"settled_units,omitempty"`
	ReleasedUnits uint64    `json:"released_units,omitempty"`
	LedgerEntryID string    `json:"ledger_entry_id,omitempty"`
	ExpiresAt     time.Time `json:"expires_at,omitempty"`
	Replayed      bool      `json:"replayed,omitempty"`
}

type InternalUsageService interface {
	Reserve(context.Context, InternalUsageReserveCommand) (InternalUsageResult, error)
	Settle(context.Context, InternalUsageSettleCommand) (InternalUsageResult, error)
	Release(context.Context, InternalUsageReleaseCommand) (InternalUsageResult, error)
}
