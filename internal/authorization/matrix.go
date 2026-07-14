// Package authorization implements the tenant-scoped RBAC decision used by
// the gateway and services. An absent rule is a deny, never an implicit allow.
package authorization

type Role string
type Resource string
type Action string

const (
	RoleOwner          Role = "owner"
	RoleAdmin          Role = "admin"
	RoleContractAdmin  Role = "contract_admin"
	RoleProgramManager Role = "program_manager"
	RoleReviewer       Role = "reviewer"
	RoleMember         Role = "member"

	ResourceAccount     Resource = "account"
	ResourceMembership  Resource = "membership"
	ResourceInvitation  Resource = "invitation"
	ResourceProgram     Resource = "program"
	ResourceContract    Resource = "contract"
	ResourceAudit       Resource = "audit"
	ResourcePrivateWork Resource = "private_work"

	ActionRead    Action = "read"
	ActionCreate  Action = "create"
	ActionUpdate  Action = "update"
	ActionDelete  Action = "delete"
	ActionExport  Action = "export"
	ActionApprove Action = "approve"
)

type Request struct {
	SubjectTenantID  string
	SubjectUserID    string
	Role             Role
	ResourceTenantID string
	ResourceOwnerID  string
	Resource         Resource
	Action           Action
}

type key struct {
	role     Role
	resource Resource
	action   Action
}

var rules = map[key]bool{
	{RoleOwner, ResourceMembership, ActionRead}: true, {RoleOwner, ResourceMembership, ActionCreate}: true, {RoleOwner, ResourceMembership, ActionDelete}: true,
	{RoleOwner, ResourceInvitation, ActionCreate}: true, {RoleOwner, ResourceAudit, ActionRead}: true, {RoleOwner, ResourceAudit, ActionExport}: true,
	{RoleOwner, ResourceContract, ActionRead}: true, {RoleOwner, ResourceContract, ActionCreate}: true,
	{RoleAdmin, ResourceMembership, ActionRead}: true, {RoleAdmin, ResourceMembership, ActionCreate}: true, {RoleAdmin, ResourceMembership, ActionDelete}: true,
	{RoleAdmin, ResourceInvitation, ActionCreate}: true, {RoleAdmin, ResourceProgram, ActionRead}: true, {RoleAdmin, ResourceProgram, ActionCreate}: true, {RoleAdmin, ResourceProgram, ActionUpdate}: true,
	{RoleContractAdmin, ResourceContract, ActionRead}: true, {RoleContractAdmin, ResourceContract, ActionCreate}: true, {RoleContractAdmin, ResourceAudit, ActionRead}: true,
	{RoleProgramManager, ResourceProgram, ActionRead}: true, {RoleProgramManager, ResourceProgram, ActionCreate}: true, {RoleProgramManager, ResourceProgram, ActionUpdate}: true,
	{RoleReviewer, ResourceProgram, ActionRead}: true,
	{RoleMember, ResourceAccount, ActionRead}:   true, {RoleMember, ResourceAccount, ActionUpdate}: true, {RoleMember, ResourceAccount, ActionExport}: true, {RoleMember, ResourceAccount, ActionDelete}: true,
	{RoleMember, ResourcePrivateWork, ActionRead}: true, {RoleMember, ResourcePrivateWork, ActionCreate}: true, {RoleMember, ResourcePrivateWork, ActionUpdate}: true, {RoleMember, ResourcePrivateWork, ActionDelete}: true, {RoleMember, ResourcePrivateWork, ActionExport}: true,
}

func Allow(request Request) bool {
	if request.SubjectTenantID == "" || request.SubjectUserID == "" || request.ResourceTenantID == "" || request.SubjectTenantID != request.ResourceTenantID {
		return false
	}
	if (request.Resource == ResourceAccount || request.Resource == ResourcePrivateWork) && request.ResourceOwnerID != request.SubjectUserID {
		return false
	}
	return rules[key{request.Role, request.Resource, request.Action}]
}

func Roles() []Role {
	return []Role{RoleOwner, RoleAdmin, RoleContractAdmin, RoleProgramManager, RoleReviewer, RoleMember}
}
func Resources() []Resource {
	return []Resource{ResourceAccount, ResourceMembership, ResourceInvitation, ResourceProgram, ResourceContract, ResourceAudit, ResourcePrivateWork}
}
func Actions() []Action {
	return []Action{ActionRead, ActionCreate, ActionUpdate, ActionDelete, ActionExport, ActionApprove}
}
