package control

import "time"

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
