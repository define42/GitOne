package control

import "testing"

func TestRoleAllows(t *testing.T) {
	if !RoleOwner.Allows(RoleWrite) {
		t.Fatal("owner should write")
	}
	if RoleRead.Allows(RoleWrite) {
		t.Fatal("read should not write")
	}
}
func TestValidateRequiresOwner(t *testing.T) {
	d := Document{Version: 1, Group: "g", Members: map[string]Role{"a": RoleWrite}, Repositories: map[string]RepositoryPolicy{}}
	if Validate("g", d) == nil {
		t.Fatal("expected owner error")
	}
}
func TestValidateGroupMatch(t *testing.T) {
	d := Document{Version: 1, Group: "other", Members: map[string]Role{"a": RoleOwner}, Repositories: map[string]RepositoryPolicy{}}
	if Validate("g", d) == nil {
		t.Fatal("expected mismatch")
	}
}
