package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"example.com/puregit-server/internal/control"
	"strings"
	"time"
)

type Resolver struct {
	Controls                      *control.Store
	BootstrapUser, BootstrapToken string
}
type Principal struct {
	Name      string
	Role      control.Role
	Group     string
	Bootstrap bool
}

func (r *Resolver) Authenticate(ctx context.Context, group, user, secret string) (Principal, error) {
	if user == r.BootstrapUser && constant(secret, r.BootstrapToken) {
		return Principal{Name: user, Role: control.RoleOwner, Bootstrap: true}, nil
	}
	paths := parents(group)
	for i := len(paths) - 1; i >= 0; i-- {
		d, e := r.Controls.Load(ctx, paths[i])
		if e != nil {
			continue
		}
		if role, ok := d.Members[user]; ok && constant(secret, user) {
			return Principal{Name: user, Role: role, Group: paths[i]}, nil
		}
		for _, t := range d.Tokens {
			if t.Disabled || t.Name != user || (t.ExpiresAt != nil && time.Now().After(*t.ExpiresAt)) {
				continue
			}
			if verifyToken(t.Hash, secret) {
				return Principal{Name: user, Role: t.Role, Group: paths[i]}, nil
			}
		}
		if !d.Inherit {
			break
		}
	}
	return Principal{}, errors.New("invalid credentials")
}
func parents(g string) []string {
	p := strings.Split(g, "/")
	o := make([]string, len(p))
	for i := range p {
		o[i] = strings.Join(p[:i+1], "/")
	}
	return o
}
func constant(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
func verifyToken(encoded, secret string) bool {
	if strings.HasPrefix(encoded, "sha256:") {
		sum := sha256.Sum256([]byte(secret))
		return constant(encoded, "sha256:"+fmtHex(sum[:]))
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	return false
}
func fmtHex(b []byte) string {
	const h = "0123456789abcdef"
	o := make([]byte, len(b)*2)
	for i, v := range b {
		o[i*2] = h[v>>4]
		o[i*2+1] = h[v&15]
	}
	return string(o)
}
