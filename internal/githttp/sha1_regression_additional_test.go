package githttp

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/storage/memory"
)

func TestUploadPackMissingRepository(t *testing.T) {
	response := httptest.NewRecorder()
	(Handler{Storage: storage.Store{Root: t.TempDir()}}).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/engineering/missing.git/git-upload-pack",
			http.NoBody,
		),
	)

	if response.Code != http.StatusNotFound {
		t.Fatalf("upload-pack returned %d, want %d: %s", response.Code, http.StatusNotFound, response.Body.String())
	}
}

func TestReferenceUpdateMayDiscardObjectsIsConservative(t *testing.T) {
	repository, err := git.Init(memory.NewStorage(), nil)
	if err != nil {
		t.Fatal(err)
	}

	sequence := int64(0)
	storeCommit := func(message string, parents ...plumbing.Hash) plumbing.Hash {
		t.Helper()
		sequence++
		signature := object.Signature{
			Name:  "alice",
			Email: "alice@example.com",
			When:  time.Unix(sequence, 0).UTC(),
		}
		commit := &object.Commit{
			Author:       signature,
			Committer:    signature,
			Message:      message,
			TreeHash:     plumbing.ZeroHash,
			ParentHashes: parents,
		}
		encoded := &plumbing.MemoryObject{}
		if encodeErr := commit.Encode(encoded); encodeErr != nil {
			t.Fatal(encodeErr)
		}
		hash, storeErr := repository.Storer.SetEncodedObject(encoded)
		if storeErr != nil {
			t.Fatal(storeErr)
		}
		return hash
	}

	base := storeCommit("base")
	descendant := storeCommit("descendant", base)
	sibling := storeCommit("sibling", base)
	missing := plumbing.NewHash("1111111111111111111111111111111111111111")
	brokenHistory := storeCommit("broken history", missing)
	branch := plumbing.NewBranchReferenceName("main")

	for _, test := range []struct {
		name    string
		command *packp.Command
		want    bool
	}{
		{
			name: "delete",
			command: &packp.Command{
				Name: branch,
				Old:  base,
				New:  plumbing.ZeroHash,
			},
			want: true,
		},
		{
			name: "non-branch update",
			command: &packp.Command{
				Name: plumbing.NewTagReferenceName("v1"),
				Old:  base,
				New:  descendant,
			},
			want: true,
		},
		{
			name: "missing old commit",
			command: &packp.Command{
				Name: branch,
				Old:  missing,
				New:  descendant,
			},
			want: true,
		},
		{
			name: "missing new commit",
			command: &packp.Command{
				Name: branch,
				Old:  base,
				New:  missing,
			},
			want: true,
		},
		{
			name: "unreadable new history",
			command: &packp.Command{
				Name: branch,
				Old:  base,
				New:  brokenHistory,
			},
			want: true,
		},
		{
			name: "non-fast-forward",
			command: &packp.Command{
				Name: branch,
				Old:  descendant,
				New:  sibling,
			},
			want: true,
		},
		{
			name: "fast-forward",
			command: &packp.Command{
				Name: branch,
				Old:  base,
				New:  descendant,
			},
		},
		{
			name: "create",
			command: &packp.Command{
				Name: branch,
				Old:  plumbing.ZeroHash,
				New:  descendant,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := referenceUpdateMayDiscardObjects(repository, test.command); got != test.want {
				t.Fatalf("referenceUpdateMayDiscardObjects() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMalformedSHA1ReceivePackDoesNotPublishObjectsOrReference(t *testing.T) {
	store := storage.Store{Root: t.TempDir()}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "docs"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	path, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(path)
	if err != nil {
		t.Fatal(err)
	}
	mainName := plumbing.NewBranchReferenceName("main")
	main, err := repository.Reference(mainName, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(main.Hash().String()) != 40 {
		t.Fatalf("repository uses a non-SHA-1 object ID: %s", main.Hash())
	}

	featureName := plumbing.NewBranchReferenceName("feature")
	referenceRequest := packp.NewReferenceUpdateRequest()
	if err = referenceRequest.Capabilities.Set(capability.ReportStatus); err != nil {
		t.Fatal(err)
	}
	referenceRequest.Commands = []*packp.Command{{
		Name: featureName,
		Old:  plumbing.ZeroHash,
		New:  main.Hash(),
	}}
	var body bytes.Buffer
	if err = referenceRequest.Encode(&body); err != nil {
		t.Fatal(err)
	}
	// This is a valid pack signature, version, and object count followed by a
	// truncated object. The reference update preceding it remains well formed.
	body.Write([]byte("PACK\x00\x00\x00\x02\x00\x00\x00\x01"))

	beforePacks, err := filepath.Glob(filepath.Join(path, "objects", "pack", "*"))
	if err != nil {
		t.Fatal(err)
	}
	notifications := 0
	response := httptest.NewRecorder()
	(Handler{
		Storage: store,
		RepositoryUpdated: func(repopath.Repository, []ReferenceUpdate) {
			notifications++
		},
	}).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodPost,
			"/engineering/docs.git/git-receive-pack",
			&body,
		),
	)

	if response.Code != http.StatusOK {
		t.Fatalf("receive-pack returned %d: %s", response.Code, response.Body.String())
	}
	status := packp.NewReportStatus()
	if err = status.Decode(response.Body); err != nil {
		t.Fatalf("decode receive status: %v", err)
	}
	if status.UnpackStatus == "ok" || len(status.CommandStatuses) != 1 || status.CommandStatuses[0].Status == "ok" {
		t.Fatalf("malformed pack was not rejected: %#v", status)
	}
	if _, err = repository.Reference(featureName, false); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("feature reference was published or returned an unexpected error: %v", err)
	}
	mainAfter, err := repository.Reference(mainName, true)
	if err != nil {
		t.Fatal(err)
	}
	if mainAfter.Hash() != main.Hash() {
		t.Fatalf("main changed from %s to %s", main.Hash(), mainAfter.Hash())
	}
	afterPacks, err := filepath.Glob(filepath.Join(path, "objects", "pack", "*"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterPacks, beforePacks) {
		t.Fatalf("live packs changed after malformed receive: before %v, after %v", beforePacks, afterPacks)
	}
	if notifications != 0 {
		t.Fatalf("malformed receive emitted %d repository update notifications", notifications)
	}
}
