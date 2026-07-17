package postgres

import (
	"bytes"
	"testing"
	"time"
)

func TestAuditCursorIsTenantBoundTamperEvidentAndExpires(t *testing.T) {
	service := ControlService{IDKey: bytes.Repeat([]byte{0xa1}, 32)}
	now := time.Date(2026, time.July, 17, 12, 0, 0, 0, time.UTC)
	tenantID := "78000000-0000-4000-8000-000000002001"
	recordID := "78000000-0000-4000-8000-000000002002"
	token, err := service.encodeAuditCursor(tenantID, now.Add(-time.Minute), recordID, now.Add(15*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	beforeAt, beforeID, err := service.decodeAuditCursor(token, tenantID, now)
	if err != nil || beforeID != recordID || !beforeAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("beforeAt=%s beforeID=%s err=%v", beforeAt, beforeID, err)
	}
	if _, _, err = service.decodeAuditCursor(token, "78000000-0000-4000-8000-000000002003", now); err == nil {
		t.Fatal("cross-tenant cursor accepted")
	}
	tampered := token[:len(token)-1] + "A"
	if _, _, err = service.decodeAuditCursor(tampered, tenantID, now); err == nil {
		t.Fatal("tampered cursor accepted")
	}
	if _, _, err = service.decodeAuditCursor(token, tenantID, now.Add(16*time.Minute)); err == nil {
		t.Fatal("expired cursor accepted")
	}
}
