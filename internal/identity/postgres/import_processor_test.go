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
