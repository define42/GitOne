package control

import (
	"testing"
	"time"
)

func TestCloneIsolatesDocumentState(t *testing.T) {
	expiry := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	original := Document{
		Version:    CurrentVersion,
		Group:      "g",
		Visibility: "private",
		Members:    map[string]Role{"alice": RoleOwner},
		Tokens: []Token{{
			Name: "ci", Key: "deploy", Hash: "hash", Role: RoleDeveloper,
			ExpiresAt: &expiry,
		}},
	}
	cloned := original.clone()

	cloned.Members["mallory"] = RoleOwner
	if _, exists := original.Members["mallory"]; exists {
		t.Fatal("clone shares the Members map with the original")
	}

	cloned.Tokens[0].Role = RoleOwner
	if original.Tokens[0].Role != RoleDeveloper {
		t.Fatal("clone shares the Tokens slice with the original")
	}

	*cloned.Tokens[0].ExpiresAt = expiry.Add(24 * time.Hour)
	if !original.Tokens[0].ExpiresAt.Equal(expiry) {
		t.Fatalf(
			"clone shares the token ExpiresAt pointer: original = %s",
			original.Tokens[0].ExpiresAt,
		)
	}
}

func TestRoleAllows(t *testing.T) {
	if !RoleOwner.Allows(RoleDeveloper) {
		t.Fatal("owner should have developer access")
	}
	if !RoleMaintainer.Allows(RoleDeveloper) {
		t.Fatal("maintainer should have developer access")
	}
	if RoleMaintainer.Allows(RoleOwner) {
		t.Fatal("maintainer should not have owner access")
	}
	if RoleRead.Allows(RoleDeveloper) {
		t.Fatal("read should not have developer access")
	}
}

func TestValidateRequiresOwner(t *testing.T) {
	d := Document{
		Version: CurrentVersion, Group: "g", Visibility: "private",
		Members: map[string]Role{"a": RoleDeveloper},
	}
	if Validate("g", d) == nil {
		t.Fatal("expected owner error")
	}
}

func TestValidateAllowsOwnerlessInheritedSubgroup(t *testing.T) {
	d := Document{
		Version: CurrentVersion, Group: "parent/child", Inherit: true,
		Visibility: "private", Members: map[string]Role{},
	}
	if err := Validate("parent/child", d); err != nil {
		t.Fatalf("validate inherited subgroup: %v", err)
	}

	d.Inherit = false
	if Validate("parent/child", d) == nil {
		t.Fatal("ownerless subgroup was allowed to disable inheritance")
	}
}

func TestValidateGroupMatch(t *testing.T) {
	d := Document{
		Version: CurrentVersion, Group: "other", Visibility: "private",
		Members: map[string]Role{"a": RoleOwner},
	}
	if Validate("g", d) == nil {
		t.Fatal("expected mismatch")
	}
}

func TestValidateRejectsPreviousSchemaVersion(t *testing.T) {
	document := Document{
		Version:    CurrentVersion - 1,
		Group:      "g",
		Visibility: "private",
		Members:    map[string]Role{"alice": RoleOwner},
	}
	if err := Validate("g", document); err == nil {
		t.Fatal("previous control schema version was accepted")
	}
}

func TestValidateRejectsInvalidSettings(t *testing.T) {
	base := Document{
		Version:    CurrentVersion,
		Group:      "g",
		Visibility: "private",
		Members:    map[string]Role{"alice": RoleOwner},
		Tokens:     []Token{},
	}
	tests := []struct {
		name   string
		mutate func(*Document)
	}{
		{
			name: "empty member",
			mutate: func(document *Document) {
				document.Members[""] = RoleRead
			},
		},
		{
			name: "invalid member role",
			mutate: func(document *Document) {
				document.Members["bob"] = Role("superuser")
			},
		},
		{
			name: "legacy admin member role",
			mutate: func(document *Document) {
				document.Members["bob"] = Role("admin")
			},
		},
		{
			name: "legacy write member role",
			mutate: func(document *Document) {
				document.Members["bob"] = Role("write")
			},
		},
		{
			name: "token without name",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Key: "deploy", Hash: "hash", Role: RoleDeveloper}}
			},
		},
		{
			name: "token without key",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Hash: "hash", Role: RoleDeveloper}}
			},
		},
		{
			name: "token without hash",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Key: "deploy", Role: RoleDeveloper}}
			},
		},
		{
			name: "invalid token role",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Key: "deploy", Hash: "hash", Role: Role("superuser")}}
			},
		},
		{
			name: "legacy admin token role",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Key: "deploy", Hash: "hash", Role: Role("admin")}}
			},
		},
		{
			name: "legacy write token role",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Key: "deploy", Hash: "hash", Role: Role("write")}}
			},
		},
		{
			name: "duplicate token name",
			mutate: func(document *Document) {
				document.Tokens = []Token{
					{Name: "ci", Key: "deploy", Hash: "sha256:a", Role: RoleDeveloper},
					{Name: "ci", Key: "release", Hash: "sha256:b", Role: RoleDeveloper},
				}
			},
		},
		{
			name: "duplicate token key",
			mutate: func(document *Document) {
				document.Tokens = []Token{
					{Name: "ci", Key: "deploy", Hash: "sha256:a", Role: RoleDeveloper},
					{Name: "release", Key: "deploy", Hash: "sha256:b", Role: RoleDeveloper},
				}
			},
		},
		{
			name: "invalid group visibility",
			mutate: func(document *Document) {
				document.Visibility = "secret"
			},
		},
		{
			name: "negative LFS limit",
			mutate: func(document *Document) {
				document.LFS.MaximumObjectBytes = -1
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := base
			document.Members = map[string]Role{"alice": RoleOwner}
			document.Tokens = nil
			test.mutate(&document)
			if Validate("g", document) == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
