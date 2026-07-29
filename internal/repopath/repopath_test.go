package repopath

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNestedRepository(t *testing.T) {
	r, s, e := ParseGitRequestPath("/engineering/backend/api.git/git-upload-pack")
	if e != nil {
		t.Fatal(e)
	}
	if r.Group() != "engineering/backend" || r.Name != "api" || s != "/git-upload-pack" {
		t.Fatalf("unexpected: %#v %q", r, s)
	}
}

func TestRejectRootRepository(t *testing.T) {
	if _, _, e := ParseGitRequestPath("/api.git/info/refs"); e == nil {
		t.Fatal("expected error")
	}
}

func TestSafeJoinEscape(t *testing.T) {
	if _, e := SafeJoin("/tmp/root", "..", "outside"); e == nil {
		t.Fatal("expected escape rejection")
	}
}

func TestParseRepositoryRoutes(t *testing.T) {
	for _, test := range []struct {
		path   string
		suffix string
	}{
		{path: "/team/project.git", suffix: ""},
		{path: "/team/project.git/info/refs", suffix: "/info/refs"},
		{path: "/team/project.git/git-receive-pack", suffix: "/git-receive-pack"},
		{path: "/team/project.git/info/lfs/objects/batch", suffix: "/info/lfs/objects/batch"},
		{path: "/team/project.git/info/lfs/objects/abc", suffix: "/info/lfs/objects/abc"},
	} {
		t.Run(test.suffix, func(t *testing.T) {
			repository, suffix, err := ParseGitRequestPath(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if repository.Full() != "team/project" || suffix != test.suffix {
				t.Fatalf("ParseGitRequestPath(%q) = %#v, %q", test.path, repository, suffix)
			}
		})
	}
}

func TestParseRepositoryRoutesWithLFSGroupNames(t *testing.T) {
	for _, test := range []struct {
		path       string
		repository string
		suffix     string
	}{
		{
			path:       "/info/lfs/project.git/info/refs",
			repository: "info/lfs/project",
			suffix:     "/info/refs",
		},
		{
			path:       "/info/lfs/objects/team/project.git/git-upload-pack",
			repository: "info/lfs/objects/team/project",
			suffix:     "/git-upload-pack",
		},
		{
			path:       "/info/lfs/objects/team/project.git/info/lfs/objects/abc",
			repository: "info/lfs/objects/team/project",
			suffix:     "/info/lfs/objects/abc",
		},
		{
			path:       "/archive.git/info/lfs/objects/team/project.git/info/refs",
			repository: "archive.git/info/lfs/objects/team/project",
			suffix:     "/info/refs",
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			repository, suffix, err := ParseGitRequestPath(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if repository.Full() != test.repository || suffix != test.suffix {
				t.Fatalf(
					"ParseGitRequestPath(%q) = %#v, %q",
					test.path,
					repository,
					suffix,
				)
			}
		})
	}
}

func TestRejectInvalidRepositoryPaths(t *testing.T) {
	for _, path := range []string{
		"/team/project",
		"/team//project.git",
		"/team/project.git/",
		"/team/.project.git",
		"/team/../project.git",
		"/team/project!.git",
		"/team/prøject.git",
		"/team/" + strings.Repeat("x", 101) + ".git",
	} {
		t.Run(path, func(t *testing.T) {
			if _, _, err := ParseGitRequestPath(path); err == nil {
				t.Fatalf("ParseGitRequestPath(%q) unexpectedly succeeded", path)
			}
		})
	}
}

func TestParseGroup(t *testing.T) {
	parts, err := ParseGroup("/engineering/backend/")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(parts, "/") != "engineering/backend" {
		t.Fatalf("unexpected group parts: %q", parts)
	}

	for _, group := range []string{"", "/", "engineering//backend", ".hidden", "..", "bad name"} {
		if _, err = ParseGroup(group); err == nil {
			t.Fatalf("ParseGroup(%q) unexpectedly succeeded", group)
		}
	}
}

func TestSafeJoinWithinRoot(t *testing.T) {
	root := t.TempDir()
	got, err := SafeJoin(root, "group", "repository.git")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "group", "repository.git")
	if got != want {
		t.Fatalf("SafeJoin() = %q, want %q", got, want)
	}
}
