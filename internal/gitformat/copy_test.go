package gitformat

import (
	"bytes"
	"compress/zlib"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
)

func TestCopySHA256RepositoryKeepsOnlyValidatedReachableData(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.git")
	source, err := Init(sourcePath, true)
	if err != nil {
		t.Fatal(err)
	}
	emptyTree := storeTestObject(t, source, plumbing.TreeObject, nil)
	tip := testCommit(t, source, emptyTree, "reachable")
	unreachable := storeTestObject(t, source, plumbing.BlobObject, []byte("unreachable"))
	main := plumbing.NewBranchReferenceName("main")
	setTestRef(t, source, plumbing.NewHashReference(main, tip))
	setTestRef(t, source, plumbing.NewSymbolicReference(plumbing.HEAD, main))

	configuration, err := source.Config()
	if err != nil {
		t.Fatal(err)
	}
	configuration.Remotes["origin"] = &config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://example.invalid/source.git"},
	}
	if err = source.Storer.SetConfig(configuration); err != nil {
		t.Fatal(err)
	}

	foreignLooseObject := filepath.Join(
		sourcePath,
		"objects",
		"ab",
		strings.Repeat("c", 38),
	)
	if err = os.MkdirAll(filepath.Dir(foreignLooseObject), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(foreignLooseObject, []byte("legacy object junk"), 0o640); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(sourcePath, "hooks", "post-receive")
	if err = os.MkdirAll(filepath.Dir(hook), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(hook, []byte("untrusted hook"), 0o750); err != nil {
		t.Fatal(err)
	}

	destinationPath := filepath.Join(t.TempDir(), "destination.git")
	destination, err := CopySHA256Repository(source, destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = destination.Close() }()

	if err = ValidateReachable(destination); err != nil {
		t.Fatalf("ValidateReachable: %v", err)
	}
	assertTestRef(t, destination, main, tip)
	if err = destination.Storer.HasEncodedObject(unreachable); !errors.Is(
		err,
		plumbing.ErrObjectNotFound,
	) {
		t.Fatalf("unreachable object lookup = %v, want object not found", err)
	}
	for _, path := range []string{
		filepath.Join(destinationPath, "objects", "ab", strings.Repeat("c", 38)),
		filepath.Join(destinationPath, "hooks", "post-receive"),
	} {
		if _, err = os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("untrusted source data was copied to %s: %v", path, err)
		}
	}
	copiedConfig, err := destination.Config()
	if err != nil {
		t.Fatal(err)
	}
	if len(copiedConfig.Remotes) != 0 {
		t.Fatalf("source remotes were copied: %#v", copiedConfig.Remotes)
	}
	if _, err = os.Stat(foreignLooseObject); err != nil {
		t.Fatalf("source was modified: %v", err)
	}
}

func TestCopySHA256RepositoryRejectsInvalidSourceBeforeCreatingDestination(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T) *git.Repository
		want  string
	}{
		{
			name: "legacy source",
			setup: func(t *testing.T) *git.Repository {
				path := filepath.Join(t.TempDir(), "legacy.git")
				repo, err := git.PlainInit(path, true)
				if err != nil {
					t.Fatal(err)
				}
				return repo
			},
			want: "repository is not SHA-256",
		},
		{
			name: "missing reachable object",
			setup: func(t *testing.T) *git.Repository {
				repo, err := Init(filepath.Join(t.TempDir(), "source.git"), true)
				if err != nil {
					t.Fatal(err)
				}
				missing, ok := plumbing.FromBytes(bytes.Repeat([]byte{0x42}, 32))
				if !ok {
					t.Fatal("construct missing object ID")
				}
				setTestRef(t, repo, plumbing.NewHashReference("refs/heads/main", missing))
				return repo
			},
			want: "load object",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := test.setup(t)
			defer func() { _ = source.Close() }()
			destinationPath := filepath.Join(t.TempDir(), "destination.git")
			_, err := CopySHA256Repository(source, destinationPath)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CopySHA256Repository error = %v, want containing %q", err, test.want)
			}
			if _, statErr := os.Lstat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination exists after rejected source: %v", statErr)
			}
		})
	}
}

func TestCopySHA256RepositoryRejectsExistingDestination(t *testing.T) {
	source, err := Init(filepath.Join(t.TempDir(), "source.git"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()
	blob := storeTestObject(t, source, plumbing.BlobObject, []byte("reachable"))
	setTestRef(t, source, plumbing.NewHashReference("refs/heads/data", blob))

	destinationPath := filepath.Join(t.TempDir(), "destination.git")
	marker := filepath.Join(destinationPath, "keep")
	if err := os.Mkdir(destinationPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = CopySHA256Repository(source, destinationPath)
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("CopySHA256Repository error = %v", err)
	}
	if contents, readErr := os.ReadFile(marker); readErr != nil || string(contents) != "unchanged" {
		t.Fatalf("existing destination changed: contents=%q error=%v", contents, readErr)
	}
}

func TestCopySHA256RepositoryRejectsCorruptReachableObject(t *testing.T) {
	sourcePath := filepath.Join(t.TempDir(), "source.git")
	source, err := Init(sourcePath, true)
	if err != nil {
		t.Fatal(err)
	}
	blob := storeTestObject(t, source, plumbing.BlobObject, []byte("good"))
	setTestRef(t, source, plumbing.NewHashReference("refs/heads/data", blob))
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}

	loosePath := filepath.Join(sourcePath, "objects", blob.String()[:2], blob.String()[2:])
	var compressed bytes.Buffer
	zw := zlib.NewWriter(&compressed)
	if _, err := zw.Write([]byte("blob 4\x00evil")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(loosePath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(loosePath, compressed.Bytes(), 0o400); err != nil {
		t.Fatal(err)
	}
	source, err = Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = source.Close() }()

	destinationPath := filepath.Join(t.TempDir(), "destination.git")
	_, err = CopySHA256Repository(source, destinationPath)
	if err == nil || !strings.Contains(err.Error(), "object ID does not match content") {
		t.Fatalf("CopySHA256Repository error = %v", err)
	}
	if _, statErr := os.Lstat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination exists after rejected corruption: %v", statErr)
	}
}
