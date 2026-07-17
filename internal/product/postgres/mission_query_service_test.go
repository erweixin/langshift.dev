package postgres

import (
	"bytes"
	"testing"
	"time"
)

func TestMissionCursorIsAuthenticatedAndClosed(t *testing.T) {
	service := MissionQueryService{CursorKey: bytes.Repeat([]byte{0x51}, 32)}
	want := missionCursor{TenantID: "tenant", UserID: "user", Created: time.Date(2026, time.July, 16, 15, 0, 0, 123, time.UTC), ID: "c6000000-0000-4000-8000-000000000001"}
	encoded, err := service.encodeCursor(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := service.decodeCursor(encoded)
	if err != nil || got.TenantID != want.TenantID || got.UserID != want.UserID || got.ID != want.ID || !got.Created.Equal(want.Created) {
		t.Fatalf("cursor=%#v err=%v", got, err)
	}
	tampered := []byte(encoded)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}
	if _, err = service.decodeCursor(string(tampered)); err == nil {
		t.Fatal("tampered cursor accepted")
	}
}
