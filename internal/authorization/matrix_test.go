package authorization

import "testing"

func TestCrossTenantMatrixAlwaysDenies(t *testing.T) {
	for _, role := range Roles() {
		for _, resource := range Resources() {
			for _, action := range Actions() {
				request := Request{SubjectTenantID: "tenant-a", SubjectUserID: "user-a", Role: role, ResourceTenantID: "tenant-b", ResourceOwnerID: "user-a", Resource: resource, Action: action}
				if Allow(request) {
					t.Fatalf("cross-tenant allow: role=%s resource=%s action=%s", role, resource, action)
				}
			}
		}
	}
}

func TestPrivateResourcesRequireOwnerAndExplicitRule(t *testing.T) {
	if !Allow(Request{SubjectTenantID: "t", SubjectUserID: "u", Role: RoleMember, ResourceTenantID: "t", ResourceOwnerID: "u", Resource: ResourcePrivateWork, Action: ActionRead}) {
		t.Fatal("owner read denied")
	}
	if Allow(Request{SubjectTenantID: "t", SubjectUserID: "u", Role: RoleOwner, ResourceTenantID: "t", ResourceOwnerID: "other", Resource: ResourcePrivateWork, Action: ActionRead}) {
		t.Fatal("tenant owner read another user's private work")
	}
	if Allow(Request{SubjectTenantID: "t", SubjectUserID: "u", Role: RoleReviewer, ResourceTenantID: "t", ResourceOwnerID: "u", Resource: ResourcePrivateWork, Action: ActionRead}) {
		t.Fatal("unlisted reviewer permission allowed")
	}
}

func TestAdministrativeRulesAreNarrow(t *testing.T) {
	if !Allow(Request{SubjectTenantID: "t", SubjectUserID: "owner", Role: RoleOwner, ResourceTenantID: "t", Resource: ResourceAudit, Action: ActionExport}) {
		t.Fatal("owner audit export denied")
	}
	if Allow(Request{SubjectTenantID: "t", SubjectUserID: "admin", Role: RoleAdmin, ResourceTenantID: "t", Resource: ResourceContract, Action: ActionApprove}) {
		t.Fatal("admin approved contract without contract-approver rule")
	}
	if Allow(Request{SubjectTenantID: "t", SubjectUserID: "owner", Role: RoleOwner, ResourceTenantID: "t", Resource: ResourceContract, Action: ActionApprove}) {
		t.Fatal("tenant owner bypassed independent contract-approver qualification")
	}
}
