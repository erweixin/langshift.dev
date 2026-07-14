package authorization

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type permissionFixture struct {
	SchemaVersion string      `json:"schemaVersion"`
	Allowed       [][3]string `json:"allowed"`
}

type permissionReport struct {
	SchemaVersion string `json:"schemaVersion"`
	Stage         int    `json:"stage"`
	Gate          string `json:"gate"`
	Status        string `json:"status"`
	Source        struct {
		Commit        string `json:"commit"`
		FixtureSHA256 string `json:"fixtureSha256"`
	} `json:"source"`
	Results struct {
		Roles                   int `json:"roles"`
		Resources               int `json:"resources"`
		Actions                 int `json:"actions"`
		SameTenantCases         int `json:"sameTenantCases"`
		WrongOwnerCases         int `json:"wrongOwnerCases"`
		CrossTenantCases        int `json:"crossTenantCases"`
		UnexpectedAllows        int `json:"unexpectedAllows"`
		UnexpectedDenies        int `json:"unexpectedDenies"`
		CrossTenantAllows       int `json:"crossTenantAllows"`
		WrongOwnerPrivateAllows int `json:"wrongOwnerPrivateAllows"`
	} `json:"results"`
}

func TestPermissionMatrixMatchesReviewedContract(t *testing.T) {
	fixturePath := filepath.Join("..", "..", "test-fixtures", "authorization", "stage2-permission-matrix.json")
	body, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture permissionFixture
	if err = json.Unmarshal(body, &fixture); err != nil || fixture.SchemaVersion != "1.0.0" {
		t.Fatalf("permission fixture: %v", err)
	}
	expected := map[key]bool{}
	for _, value := range fixture.Allowed {
		candidate := key{role: Role(value[0]), resource: Resource(value[1]), action: Action(value[2])}
		if expected[candidate] {
			t.Fatalf("duplicate permission %#v", candidate)
		}
		expected[candidate] = true
	}

	report := permissionReport{SchemaVersion: "1.0.0", Stage: 2, Gate: "permission-matrix", Status: "passed"}
	report.Source.Commit = os.Getenv("LITES_SOURCE_COMMIT")
	digest := sha256.Sum256(body)
	report.Source.FixtureSHA256 = hex.EncodeToString(digest[:])
	report.Results.Roles, report.Results.Resources, report.Results.Actions = len(Roles()), len(Resources()), len(Actions())

	for _, role := range Roles() {
		for _, resource := range Resources() {
			for _, action := range Actions() {
				want := expected[key{role: role, resource: resource, action: action}]
				got := Allow(Request{SubjectTenantID: "tenant-a", SubjectUserID: "user-a", Role: role, ResourceTenantID: "tenant-a", ResourceOwnerID: "user-a", Resource: resource, Action: action})
				report.Results.SameTenantCases++
				if got && !want {
					report.Results.UnexpectedAllows++
				}
				if !got && want {
					report.Results.UnexpectedDenies++
				}

				if Allow(Request{SubjectTenantID: "tenant-a", SubjectUserID: "user-a", Role: role, ResourceTenantID: "tenant-b", ResourceOwnerID: "user-a", Resource: resource, Action: action}) {
					report.Results.CrossTenantAllows++
				}
				report.Results.CrossTenantCases++

				if resource == ResourceAccount || resource == ResourcePrivateWork {
					report.Results.WrongOwnerCases++
					if Allow(Request{SubjectTenantID: "tenant-a", SubjectUserID: "user-a", Role: role, ResourceTenantID: "tenant-a", ResourceOwnerID: "user-b", Resource: resource, Action: action}) {
						report.Results.WrongOwnerPrivateAllows++
					}
				}
			}
		}
	}
	if report.Results.UnexpectedAllows != 0 || report.Results.UnexpectedDenies != 0 || report.Results.CrossTenantAllows != 0 || report.Results.WrongOwnerPrivateAllows != 0 {
		report.Status = "failed"
		t.Fatalf("permission matrix mismatch: %#v", report.Results)
	}
	if output := os.Getenv("LITES_PERMISSION_REPORT"); output != "" {
		if len(report.Source.Commit) != 40 {
			t.Fatal("LITES_SOURCE_COMMIT must be a full SHA when writing evidence")
		}
		encoded, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		if mkdirErr := os.MkdirAll(filepath.Dir(output), 0o755); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
		if writeErr := os.WriteFile(output, append(encoded, '\n'), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
}
