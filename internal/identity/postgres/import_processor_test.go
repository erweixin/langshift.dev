package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestParseInvitationCSVNormalizesAndIsolatesInvalidRows(t *testing.T) {
	contents := []byte("email,role\nALICE@Example.com,reviewer\nbad-email,member\nalice@example.com,admin\nbob@example.com,\ncarol@example.com,owner\ndave@example.com\n")
	rows, rejected, err := parseInvitationCSV(contents, "member")
	if err != nil {
		t.Fatal(err)
	}
	if rejected != 4 || len(rows) != 2 {
		t.Fatalf("rows=%#v rejected=%d", rows, rejected)
	}
	if rows[0].Number != 2 || rows[0].Email != "alice@example.com" || rows[0].Role != "reviewer" || rows[1].Number != 5 || rows[1].Email != "bob@example.com" || rows[1].Role != "member" {
		t.Fatalf("rows=%#v", rows)
	}
}

func TestParseInvitationCSVRejectsMalformedContractAndLimits(t *testing.T) {
	for _, contents := range [][]byte{
		[]byte("role,email\nmember,a@example.com\n"),
		[]byte("email,role,extra\na@example.com,member,x\n"),
		[]byte("email,role\n\"unterminated,member\n"),
		append([]byte("email\n"), 0xff),
	} {
		if _, _, err := parseInvitationCSV(contents, "member"); !errors.Is(err, ErrImportInvalid) {
			t.Fatalf("contents=%q err=%v", contents, err)
		}
	}
	tooMany := "email\n" + strings.Repeat("valid@example.com\n", maximumImportRows+1)
	if _, _, err := parseInvitationCSV([]byte(tooMany), "member"); !errors.Is(err, ErrImportInvalid) {
		t.Fatalf("row limit err=%v", err)
	}
}

func TestMatchesSHA256RequiresCanonicalFullDigest(t *testing.T) {
	contents := []byte("email\na@example.com\n")
	digest := sha256.Sum256(contents)
	expected := "sha256:" + hex.EncodeToString(digest[:])
	if !matchesSHA256(expected, contents) || matchesSHA256("sha256:abcdef", contents) || matchesSHA256(expected, append(contents, 'x')) {
		t.Fatal("content hash binding failed")
	}
}

func TestParseMembershipCSVNormalizesAndRejectsUnsafeRows(t *testing.T) {
	contents := []byte("email,role\nOWNER@example.com,owner\nmember@example.com,reviewer\nmember@example.com,admin\ninvalid,member\nother@example.com,unknown\nshort@example.com\n")
	rows, rejected, err := parseMembershipCSV(contents)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rejected != 4 || rows[0].Email != "owner@example.com" || rows[0].Role != "owner" || rows[1].Email != "member@example.com" || rows[1].Role != "reviewer" {
		t.Fatalf("rows=%#v rejected=%d", rows, rejected)
	}
}

func TestParseMembershipCSVRequiresExactHeader(t *testing.T) {
	for _, value := range []string{"email\na@example.com\n", "role,email\nmember,a@example.com\n", "email,role,extra\na@example.com,member,x\n"} {
		if _, _, err := parseMembershipCSV([]byte(value)); !errors.Is(err, ErrImportInvalid) {
			t.Fatalf("value=%q err=%v", value, err)
		}
	}
}

func TestDecodeImportWorkCommandIsStrictAndTypeBound(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	invitation := `{"import_id":"10000000-0000-0000-0000-000000000001","object_ref":"s3://imports/invitations.csv","content_hash":"` + hash + `","import_key":"invitation-import-0001","default_role":"member"}`
	decoded, err := decodeImportWorkCommand([]byte(invitation), "identity.invitation_import.process")
	if err != nil || decoded.DefaultRole != "member" || decoded.Mode != "" {
		t.Fatalf("invitation=%#v error=%v", decoded, err)
	}
	membership := `{"import_id":"10000000-0000-0000-0000-000000000002","object_ref":"s3://imports/members.csv","content_hash":"` + hash + `","import_key":"membership-import-0001","mode":"deactivate_missing"}`
	decoded, err = decodeImportWorkCommand([]byte(membership), "identity.membership_import.process")
	if err != nil || decoded.Mode != "deactivate_missing" || decoded.DefaultRole != "" {
		t.Fatalf("membership=%#v error=%v", decoded, err)
	}
	for _, invalid := range []string{
		strings.Replace(invitation, `"default_role":"member"`, `"default_role":"member","unknown":true`, 1),
		strings.Replace(invitation, `"default_role":"member"`, `"mode":"upsert"`, 1),
		invitation + `{}`,
	} {
		if _, err = decodeImportWorkCommand([]byte(invalid), "identity.invitation_import.process"); !errors.Is(err, ErrImportInvalid) {
			t.Fatalf("invalid=%s error=%v", invalid, err)
		}
	}
}
