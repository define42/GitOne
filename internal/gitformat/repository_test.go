package gitformat

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
)

func TestInitOpenAndObjectFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.git")
	repo, err := Init(path, true)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	format, err := ObjectFormat(repo)
	if err != nil {
		t.Fatalf("ObjectFormat: %v", err)
	}
	if format != formatcfg.SHA256 {
		t.Fatalf("ObjectFormat = %q, want sha256", format)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.Core.RepositoryFormatVersion != formatcfg.Version1 {
		t.Fatalf("repository format = %q, want 1", cfg.Core.RepositoryFormatVersion)
	}
	if err := repo.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if err := Validate(reopened); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestOpenRejectsSHA1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "repo.git")
	writeRawRepositoryConfig(t, path, "[core]\n\tbare = true\n")
	_, err := Open(path)
	if !errors.Is(err, ErrNotSHA256Repository) {
		t.Fatalf("Open error = %v, want ErrNotSHA256Repository", err)
	}
}

func TestRepositoryBoundaryRejectsInvalidConfiguration(t *testing.T) {
	if _, err := ObjectFormat(nil); err == nil {
		t.Fatal("ObjectFormat(nil) succeeded")
	}
	if err := Validate(nil); err == nil {
		t.Fatal("Validate(nil) succeeded")
	}

	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *git.Repository)
		want   string
	}{
		{
			name: "repository format version zero",
			mutate: func(t *testing.T, repo *git.Repository) {
				cfg, err := repo.Config()
				if err != nil {
					t.Fatal(err)
				}
				cfg.Core.RepositoryFormatVersion = formatcfg.Version0
				if err := repo.Storer.SetConfig(cfg); err != nil {
					t.Fatal(err)
				}
			},
			want: "repository is not SHA-256",
		},
		{
			name: "compatibility object format",
			mutate: func(t *testing.T, repo *git.Repository) {
				cfg, err := repo.Config()
				if err != nil {
					t.Fatal(err)
				}
				cfg.Raw.Section("extensions").SetOption("compatObjectFormat", "sha1")
				if err := repo.Storer.SetConfig(cfg); err != nil {
					t.Fatal(err)
				}
			},
			want: "compatObjectFormat is forbidden",
		},
		{
			name: "invalid symbolic target",
			mutate: func(t *testing.T, repo *git.Repository) {
				setTestRef(t, repo, plumbing.NewSymbolicReference("refs/aliases/current", "invalid target"))
			},
			want: "invalid symbolic target",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo, err := Init(filepath.Join(t.TempDir(), "repo.git"), true)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = repo.Close() }()
			test.mutate(t, repo)
			err = Validate(repo)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestObjectFormatRecognizesLegacyAndRejectsUnknown(t *testing.T) {
	legacyPath := filepath.Join(t.TempDir(), "legacy.git")
	legacy, err := git.PlainInit(legacyPath, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = legacy.Close() }()
	format, err := ObjectFormat(legacy)
	if err != nil {
		t.Fatalf("ObjectFormat legacy: %v", err)
	}
	if format != formatcfg.SHA1 {
		t.Fatalf("ObjectFormat legacy = %s, want sha1", format)
	}

	sha256Repo, err := Init(filepath.Join(t.TempDir(), "sha256.git"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sha256Repo.Close() }()
	cfg, err := sha256Repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Extensions.ObjectFormat = formatcfg.ObjectFormat("sha512")
	if err := sha256Repo.Storer.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := ObjectFormat(sha256Repo); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("ObjectFormat unknown error = %v", err)
	}
}

func TestInitAndOpenPropagateFilesystemErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Init(file, true); err == nil {
		t.Fatal("Init on a file succeeded")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing.git")); err == nil {
		t.Fatal("Open missing repository succeeded")
	}
}

func TestDetectObjectFormat(t *testing.T) {
	for _, test := range []struct {
		name    string
		config  string
		want    formatcfg.ObjectFormat
		wantErr bool
	}{
		{
			name:   "implicit SHA-1",
			config: "[core]\n\tbare = true\n",
			want:   formatcfg.SHA1,
		},
		{
			name:   "explicit SHA-1",
			config: "[extensions]\n\tobjectFormat = sha1\n",
			want:   formatcfg.SHA1,
		},
		{
			name:   "SHA-256 case-insensitive names",
			config: "[Extensions]\n\tObjectFormat = sha256\n",
			want:   formatcfg.SHA256,
		},
		{
			name:    "unsupported",
			config:  "[extensions]\n\tobjectFormat = sha512\n",
			wantErr: true,
		},
		{
			name:    "malformed",
			config:  "[extensions\n\tobjectFormat = sha256\n",
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "repo.git")
			writeRawRepositoryConfig(t, path, test.config)
			got, err := DetectObjectFormat(path)
			if (err != nil) != test.wantErr {
				t.Fatalf("DetectObjectFormat error = %v, wantErr %v", err, test.wantErr)
			}
			if err == nil && got != test.want {
				t.Fatalf("DetectObjectFormat = %s, want %s", got, test.want)
			}
		})
	}
}

func TestIsSHA256OID(t *testing.T) {
	valid := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, test := range []struct {
		value string
		want  bool
	}{
		{valid, true},
		{valid[:63], false},
		{valid + "0", false},
		{"0123456789ABCDEF" + valid[16:], false},
		{"g" + valid[1:], false},
	} {
		if got := IsSHA256OID(test.value); got != test.want {
			t.Errorf("IsSHA256OID(%q) = %v, want %v", test.value, got, test.want)
		}
	}
}

func writeRawRepositoryConfig(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatalf("MkdirAll repository: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "config"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write repository config: %v", err)
	}
}
