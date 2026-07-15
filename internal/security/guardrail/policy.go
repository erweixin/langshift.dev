// Package guardrail contains the deterministic enforcement boundary applied
// after untrusted-input classification and before model output or tool effects.
package guardrail

import "errors"

var ErrInvalidSignal = errors.New("guardrail signal is invalid")

type ThreatClass string
type SourceTrust string
type Decision string

const (
	PromptInjection    ThreatClass = "prompt_injection"
	UnauthorizedTool   ThreatClass = "unauthorized_tool"
	SecretExfiltration ThreatClass = "secret_exfiltration"
	MaliciousRetrieval ThreatClass = "malicious_retrieval"
	MemoryPoisoning    ThreatClass = "memory_poisoning"
	DangerousArtifact  ThreatClass = "dangerous_artifact"
	ApprovalBypass     ThreatClass = "approval_bypass"
	PathEscape         ThreatClass = "path_escape"
	NetworkEgress      ThreatClass = "network_egress"
	CrossTenantAccess  ThreatClass = "cross_tenant_access"

	UserInput          SourceTrust = "user_input"
	ToolOutput         SourceTrust = "tool_output"
	UntrustedRetrieval SourceTrust = "untrusted_retrieval"

	IgnoreUntrustedInstruction Decision = "ignore_untrusted_instruction"
	Block                      Decision = "block"
	BlockAndRedact             Decision = "block_and_redact"
	TreatAsData                Decision = "treat_as_data"
	RejectMemoryWrite          Decision = "reject_memory_write"
	Quarantine                 Decision = "quarantine"
)

type Signal struct {
	Class       ThreatClass
	SourceTrust SourceTrust
	Input       string
}

type Outcome struct {
	Decision            Decision
	AllowSideEffects    bool
	AllowSecretOutput   bool
	AuditDecision       bool
	PreserveTenantScope bool
}

var decisions = map[ThreatClass]Decision{
	PromptInjection: IgnoreUntrustedInstruction, UnauthorizedTool: Block,
	SecretExfiltration: BlockAndRedact, MaliciousRetrieval: TreatAsData,
	MemoryPoisoning: RejectMemoryWrite, DangerousArtifact: Quarantine,
	ApprovalBypass: Block, PathEscape: Block, NetworkEgress: Block, CrossTenantAccess: Block,
}

// Evaluate never treats content as authority. Threat classification may be
// produced by a deterministic validator or a governed model, but enforcement
// is local, fail-closed and cannot grant a tool, secret or tenant capability.
func Evaluate(signal Signal) (Outcome, error) {
	decision, known := decisions[signal.Class]
	if !known || signal.Input == "" || signal.SourceTrust != UserInput && signal.SourceTrust != ToolOutput && signal.SourceTrust != UntrustedRetrieval {
		return Outcome{}, ErrInvalidSignal
	}
	return Outcome{
		Decision: decision, AllowSideEffects: false, AllowSecretOutput: false,
		AuditDecision: true, PreserveTenantScope: true,
	}, nil
}
