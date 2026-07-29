package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

func TestBuildStoreListsNewestFirstAndReportsCorruption(t *testing.T) {
	root := t.TempDir()
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	store := NewStore(root)
	older := Job{
		ID: "older", Repository: repository.Full(), Status: StatusSucceeded,
		CreatedAt: time.Now().UTC().Add(-time.Hour),
	}
	newer := Job{
		ID: "newer", Repository: repository.Full(), Status: StatusQueued,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.save(repository, older); err != nil {
		t.Fatal(err)
	}
	if err := store.save(repository, newer); err != nil {
		t.Fatal(err)
	}
	directory, err := store.repositoryDirectory(repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "ignored.txt"), []byte("ignored"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(directory, "ignored.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	jobs, err := store.List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 2 || jobs[0].ID != newer.ID || jobs[1].ID != older.ID {
		t.Fatalf("jobs = %#v", jobs)
	}

	if err = os.WriteFile(filepath.Join(directory, "broken.json"), []byte("{"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = store.List(repository); err == nil ||
		!strings.Contains(err.Error(), "read build") {
		t.Fatalf("corrupt build error = %v", err)
	}
}

func TestBuildStoreHandlesMissingDataAndValidatesIdentifiers(t *testing.T) {
	store := NewStore(t.TempDir())
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	jobs, err := store.List(repository)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("missing build list = %#v, %v", jobs, err)
	}
	logContents, err := store.Log(repository, "missing")
	if err != nil || logContents != "" {
		t.Fatalf("missing log = %q, %v", logContents, err)
	}
	if size, sizeErr := store.logSize(repository, "missing"); sizeErr != nil || size != 0 {
		t.Fatalf("missing log size = %d, %v", size, sizeErr)
	}

	invalidIDs := []string{"", "has/slash", "has space", strings.Repeat("x", 101)}
	for _, id := range invalidIDs {
		t.Run("invalid-"+id, func(t *testing.T) {
			if validJobID(id) {
				t.Fatalf("validJobID(%q) = true", id)
			}
			if _, err = store.Get(repository, id); err == nil {
				t.Fatalf("Get accepted ID %q", id)
			}
			if _, err = store.Log(repository, id); err == nil {
				t.Fatalf("Log accepted ID %q", id)
			}
			if _, err = store.createLog(repository, id); err == nil {
				t.Fatalf("createLog accepted ID %q", id)
			}
			if _, err = store.logSize(repository, id); err == nil {
				t.Fatalf("logSize accepted ID %q", id)
			}
			if _, err = store.appendLog(repository, id, 0, nil); err == nil {
				t.Fatalf("appendLog accepted ID %q", id)
			}
		})
	}
	if !validJobID("Build_123-ok") {
		t.Fatal("valid build ID was rejected")
	}

	unsafeRepository := repopath.Repository{Groups: []string{".."}, Name: "api"}
	if _, err = store.List(unsafeRepository); err == nil {
		t.Fatal("List accepted unsafe repository")
	}
	if err = store.save(unsafeRepository, Job{ID: "build"}); err == nil {
		t.Fatal("save accepted unsafe repository")
	}
}

func TestBuildStoreLogOffsetsAndLimits(t *testing.T) {
	root := t.TempDir()
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	store := NewStore(root)

	offset, err := store.appendLog(repository, "build", 0, []byte("first"))
	if err != nil || offset != 5 {
		t.Fatalf("first append = %d, %v", offset, err)
	}
	if size, sizeErr := store.logSize(repository, "build"); sizeErr != nil || size != 5 {
		t.Fatalf("log size = %d, %v", size, sizeErr)
	}
	if actual, appendErr := store.appendLog(
		repository,
		"build",
		0,
		[]byte("duplicate"),
	); appendErr == nil || actual != 5 {
		t.Fatalf("offset conflict = %d, %v", actual, appendErr)
	}

	path, err := store.jobPath(repository, "build", ".log")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Truncate(path, MaximumStoredLogBytes-2); err != nil {
		t.Fatal(err)
	}
	offset, err = store.appendLog(
		repository,
		"build",
		MaximumStoredLogBytes-2,
		[]byte("four"),
	)
	if err != nil || offset != MaximumStoredLogBytes {
		t.Fatalf("truncated append = %d, %v", offset, err)
	}
	offset, err = store.appendLog(
		repository,
		"build",
		MaximumStoredLogBytes,
		[]byte("discarded"),
	)
	if err != nil || offset != MaximumStoredLogBytes {
		t.Fatalf("capped append = %d, %v", offset, err)
	}
	logContents, err := store.Log(repository, "build")
	if err != nil {
		t.Fatal(err)
	}
	if len(logContents) <= maximumLogBytes ||
		!strings.HasSuffix(logContents, "[log truncated by GitOne]\n") {
		t.Fatalf("unexpected limited log length=%d suffix=%q", len(logContents), logContents[len(logContents)-32:])
	}
}

func TestBuildStoreReportsFilesystemErrors(t *testing.T) {
	rootFile := filepath.Join(t.TempDir(), "root")
	if err := os.WriteFile(rootFile, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	store := NewStore(rootFile)
	repository := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	job := Job{ID: "build", CreatedAt: time.Now().UTC()}

	if _, err := store.List(repository); err == nil {
		t.Fatal("List ignored filesystem error")
	}
	if err := store.save(repository, job); err == nil {
		t.Fatal("save ignored filesystem error")
	}
	if _, err := store.createLog(repository, job.ID); err == nil {
		t.Fatal("createLog ignored filesystem error")
	}
	if _, err := store.logSize(repository, job.ID); err == nil {
		t.Fatal("logSize ignored filesystem error")
	}
	if _, err := store.appendLog(repository, job.ID, 0, []byte("log")); err == nil {
		t.Fatal("appendLog ignored filesystem error")
	}

	closedPath := filepath.Join(t.TempDir(), "closed.log")
	closed, err := os.Create(closedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = readLog(closed); err == nil {
		t.Fatal("readLog ignored a closed file")
	}
}
