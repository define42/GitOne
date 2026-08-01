package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

func TestBuildStoreCommitIDsRequireCanonicalSHA256(t *testing.T) {
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	store := NewStore(t.TempDir())
	valid := Job{
		ID:         "canonical",
		Repository: repository.Full(),
		Commit:     strings.Repeat("a", 64),
		Status:     StatusQueued,
		CreatedAt:  time.Now().UTC(),
	}
	if err := store.save(repository, valid); err != nil {
		t.Fatalf("save canonical SHA-256 build: %v", err)
	}
	loaded, err := store.Get(repository, valid.ID)
	if err != nil {
		t.Fatalf("load canonical SHA-256 build: %v", err)
	}
	if loaded.Commit != valid.Commit {
		t.Fatalf("loaded commit = %q, want %q", loaded.Commit, valid.Commit)
	}

	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "legacy-sha1", value: strings.Repeat("a", 40)},
		{name: "uppercase-sha256", value: strings.Repeat("A", 64)},
		{name: "non-hex-sha256", value: strings.Repeat("g", 64)},
		{name: "abbreviated-sha256", value: strings.Repeat("a", 63)},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := valid
			job.ID = test.name
			job.Commit = test.value
			if err := store.save(repository, job); err == nil ||
				!strings.Contains(err.Error(), "lowercase SHA-256") {
				t.Fatalf("save commit %q error = %v", test.value, err)
			}
		})
	}
}

func TestBuildStoreRejectsNonCanonicalPersistedCommitIDs(t *testing.T) {
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	store := NewStore(t.TempDir())
	directory, err := store.repositoryDirectory(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "legacy-sha1", value: strings.Repeat("b", 40)},
		{name: "uppercase-sha256", value: strings.Repeat("B", 64)},
		{name: "malformed-sha256", value: strings.Repeat("z", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			job := Job{
				ID:         test.name,
				Repository: repository.Full(),
				Commit:     test.value,
				Status:     StatusQueued,
				CreatedAt:  time.Now().UTC(),
			}
			contents, marshalErr := json.Marshal(job)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			path := filepath.Join(directory, test.name+".json")
			if writeErr := os.WriteFile(path, contents, 0o640); writeErr != nil {
				t.Fatal(writeErr)
			}

			if _, getErr := store.Get(repository, test.name); getErr == nil ||
				!strings.Contains(getErr.Error(), "lowercase SHA-256") {
				t.Fatalf("Get persisted commit %q error = %v", test.value, getErr)
			}
		})
	}
}
