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
	d := Document{
		Version: CurrentVersion, Group: "g", Visibility: "private",
		Members: map[string]Role{"a": RoleWrite},
	}
	if Validate("g", d) == nil {
		t.Fatal("expected owner error")
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
			name: "token without name",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Key: "deploy", Hash: "hash", Role: RoleWrite}}
			},
		},
		{
			name: "token without key",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Hash: "hash", Role: RoleWrite}}
			},
		},
		{
			name: "token without hash",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Key: "deploy", Role: RoleWrite}}
			},
		},
		{
			name: "invalid token role",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Key: "deploy", Hash: "hash", Role: Role("superuser")}}
			},
		},
		{
			name: "duplicate token name",
			mutate: func(document *Document) {
				document.Tokens = []Token{
					{Name: "ci", Key: "deploy", Hash: "sha256:a", Role: RoleWrite},
					{Name: "ci", Key: "release", Hash: "sha256:b", Role: RoleWrite},
				}
			},
		},
		{
			name: "duplicate token key",
			mutate: func(document *Document) {
				document.Tokens = []Token{
					{Name: "ci", Key: "deploy", Hash: "sha256:a", Role: RoleWrite},
					{Name: "release", Key: "deploy", Hash: "sha256:b", Role: RoleWrite},
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
