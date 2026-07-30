package control

import (
	"fmt"
	"strings"
	"time"
)

const CurrentVersion = 4

type Role string

const (
	RoleRead       Role = "read"
	RoleDeveloper  Role = "developer"
	RoleMaintainer Role = "maintainer"
	RoleOwner      Role = "owner"
)

type Document struct {
	Version     int             `json:"version"`
	Group       string          `json:"group"`
	Description string          `json:"description"`
	Inherit     bool            `json:"inherit"`
	Visibility  string          `json:"visibility" enum:"private,internal,public"`
	LFS         LFSPolicy       `json:"lfs"`
	Members     map[string]Role `json:"members"`
	Tokens      []Token         `json:"tokens"`
}
type Token struct {
	Name      string     `json:"name"`
	Key       string     `json:"key"`
	Hash      string     `json:"hash"`
	Role      Role       `json:"role"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
	Disabled  bool       `json:"disabled,omitempty"`
}
type LFSPolicy struct {
	Enabled             bool  `json:"enabled"`
	MaximumObjectBytes  int64 `json:"maximumObjectBytes,omitempty" minimum:"0"`
	MaximumStorageBytes int64 `json:"maximumStorageBytes,omitempty" minimum:"0"`
}

func (r Role) Allows(need Role) bool {
	rank := map[Role]int{RoleRead: 1, RoleDeveloper: 2, RoleMaintainer: 3, RoleOwner: 4}
	return rank[r] >= rank[need]
}

// clone returns a deep copy whose Members map and Tokens slice do not share
// storage with the receiver. The control store caches documents; handing out
// references to the cached Members map would let a caller mutate the shared,
// process-wide authorization state for every later Authenticate call.
func (d Document) clone() Document {
	copied := d
	if d.Members != nil {
		members := make(map[string]Role, len(d.Members))
		for name, role := range d.Members {
			members[name] = role
		}
		copied.Members = members
	}
	if d.Tokens != nil {
		tokens := make([]Token, len(d.Tokens))
		for i, token := range d.Tokens {
			if token.ExpiresAt != nil {
				expiresAt := *token.ExpiresAt
				token.ExpiresAt = &expiresAt
			}
			tokens[i] = token
		}
		copied.Tokens = tokens
	}
	return copied
}

func ValidateSettings(d Document) error {
	for name, role := range d.Members {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("member name is required")
		}
		if !validRole(role) {
			return fmt.Errorf("invalid role for member %q", name)
		}
	}
	tokenNames := map[string]struct{}{}
	tokenKeys := map[string]struct{}{}
	for _, token := range d.Tokens {
		if strings.TrimSpace(token.Name) == "" {
			return fmt.Errorf("token name is required")
		}
		if strings.TrimSpace(token.Key) == "" {
			return fmt.Errorf("token key is required for %q", token.Name)
		}
		if strings.TrimSpace(token.Hash) == "" {
			return fmt.Errorf("token hash is required for %q", token.Name)
		}
		if !validRole(token.Role) {
			return fmt.Errorf("invalid role for token %q", token.Name)
		}
		if _, exists := tokenNames[token.Name]; exists {
			return fmt.Errorf("duplicate token name %q", token.Name)
		}
		if _, exists := tokenKeys[token.Key]; exists {
			return fmt.Errorf("duplicate token key %q", token.Key)
		}
		tokenNames[token.Name] = struct{}{}
		tokenKeys[token.Key] = struct{}{}
	}

	switch d.Visibility {
	case "private", "internal", "public":
	default:
		return fmt.Errorf("invalid group visibility %q", d.Visibility)
	}
	if d.LFS.MaximumObjectBytes < 0 || d.LFS.MaximumStorageBytes < 0 {
		return fmt.Errorf("group LFS limits cannot be negative")
	}
	return nil
}
