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

func TestValidateRejectsInvalidSettings(t *testing.T) {
	base := Document{
		Version:      1,
		Group:        "g",
		Members:      map[string]Role{"alice": RoleOwner},
		Tokens:       []Token{},
		Repositories: map[string]RepositoryPolicy{},
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
			name: "token without hash",
			mutate: func(document *Document) {
				document.Tokens = []Token{{Name: "ci", Key: "deploy", Role: RoleWrite}}
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
			name: "negative LFS limit",
			mutate: func(document *Document) {
				document.Repositories["api"] = RepositoryPolicy{
					LFS: LFSPolicy{MaximumObjectBytes: -1},
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := base
			document.Members = map[string]Role{"alice": RoleOwner}
			document.Tokens = nil
			document.Repositories = map[string]RepositoryPolicy{}
			test.mutate(&document)
			if Validate("g", document) == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}
