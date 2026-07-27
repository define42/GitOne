package repopath

import (
	"errors"
	"path/filepath"
	"strings"
)

type Repository struct {
	Groups []string
	Name   string
}

func (r Repository) Group() string { return strings.Join(r.Groups, "/") }
func (r Repository) Full() string  { return r.Group() + "/" + r.Name }

func ParseGitRequestPath(p string) (Repository, string, error) {
	p = strings.TrimPrefix(p, "/")
	var suffix string
	for _, s := range []string{"/info/lfs/objects/batch", "/info/lfs/objects/verify", "/git-upload-pack", "/git-receive-pack", "/info/refs"} {
		if strings.HasSuffix(p, s) {
			p = strings.TrimSuffix(p, s)
			suffix = s
			break
		}
	}
	if i := strings.Index(p, "/info/lfs/objects/"); i >= 0 {
		suffix = p[i:]
		p = p[:i]
	}
	if !strings.HasSuffix(p, ".git") {
		return Repository{}, "", errors.New("repository path must end in .git")
	}
	p = strings.TrimSuffix(p, ".git")
	if strings.Contains(p, "//") || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return Repository{}, "", errors.New("invalid repository path")
	}
	parts := strings.Split(p, "/")
	if len(parts) < 2 {
		return Repository{}, "", errors.New("repository must belong to a group")
	}
	for _, v := range parts {
		if !valid(v) {
			return Repository{}, "", errors.New("invalid path segment")
		}
	}
	return Repository{Groups: append([]string(nil), parts[:len(parts)-1]...), Name: parts[len(parts)-1]}, suffix, nil
}
func ParseGroup(p string) ([]string, error) {
	p = strings.Trim(p, "/")
	if p == "" || strings.Contains(p, "//") {
		return nil, errors.New("invalid group")
	}
	parts := strings.Split(p, "/")
	for _, v := range parts {
		if !valid(v) {
			return nil, errors.New("invalid group segment")
		}
	}
	return parts, nil
}
func valid(s string) bool {
	if s == "" || len(s) > 100 || s == "." || s == ".." || strings.HasPrefix(s, ".") {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}
func SafeJoin(root string, parts ...string) (string, error) {
	target := filepath.Join(append([]string{root}, parts...)...)
	ar, e := filepath.Abs(root)
	if e != nil {
		return "", e
	}
	at, e := filepath.Abs(target)
	if e != nil {
		return "", e
	}
	rel, e := filepath.Rel(ar, at)
	if e != nil {
		return "", e
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes root")
	}
	return at, nil
}
