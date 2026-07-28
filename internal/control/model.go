package control

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type Role string

const (
	RoleRead  Role = "read"
	RoleWrite Role = "write"
	RoleAdmin Role = "admin"
	RoleOwner Role = "owner"
)

type Document struct {
	Version      int                         `json:"version"`
	Group        string                      `json:"group"`
	Description  string                      `json:"description"`
	Inherit      bool                        `json:"inherit"`
	Members      map[string]Role             `json:"members"`
	Tokens       []Token                     `json:"tokens"`
	Repositories map[string]RepositoryPolicy `json:"repositories"`
}
type Token struct {
	Name         string     `json:"name"`
	Key          string     `json:"key"`
	Hash         string     `json:"hash"`
	Role         Role       `json:"role"`
	Repositories []string   `json:"repositories,omitempty"`
	ExpiresAt    *time.Time `json:"expiresAt,omitempty"`
	Disabled     bool       `json:"disabled,omitempty"`
}
type RepositoryPolicy struct {
	Visibility string    `json:"visibility,omitempty"`
	LFS        LFSPolicy `json:"lfs"`
}
type LFSPolicy struct {
	Enabled             bool  `json:"enabled"`
	MaximumObjectBytes  int64 `json:"maximumObjectBytes,omitempty"`
	MaximumStorageBytes int64 `json:"maximumStorageBytes,omitempty"`
}

func (r Role) Allows(need Role) bool {
	rank := map[Role]int{RoleRead: 1, RoleWrite: 2, RoleAdmin: 3, RoleOwner: 4}
	return rank[r] >= rank[need]
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
		for _, repository := range token.Repositories {
			if strings.TrimSpace(repository) == "" || filepath.Base(repository) != repository || repository == "control" {
				return fmt.Errorf("invalid repository scope %q for token %q", repository, token.Name)
			}
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

	for name, policy := range d.Repositories {
		switch policy.Visibility {
		case "", "private", "internal", "public":
		default:
			return fmt.Errorf("invalid visibility for repository %q", name)
		}
		if policy.LFS.MaximumObjectBytes < 0 || policy.LFS.MaximumStorageBytes < 0 {
			return fmt.Errorf("LFS limits cannot be negative for repository %q", name)
		}
	}
	return nil
}
