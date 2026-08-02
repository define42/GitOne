package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestRepositoryBranchDivergenceAndDeletion(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	repository := fixtureRepository(t, service)
	root, err := repository.CommitObject(plumbing.NewHash(head))
	if err != nil {
		t.Fatal(err)
	}
	tree, err := root.Tree()
	if err != nil {
		t.Fatal(err)
	}
	mainTip := storeTestCommit(t, repository, tree, root.Hash)
	aheadTip := storeTestCommit(t, repository, tree, mainTip.Hash)
	divergedTree := storeTestTree(t, repository, object.TreeEntry{
		Name: "diverged.txt",
		Mode: filemode.Regular,
		Hash: storeTestBlob(t, repository, []byte("diverged\n")),
	})
	divergedTip := storeTestCommit(t, repository, divergedTree, root.Hash)
	setFixtureBranch(t, repository, "main", mainTip.Hash)
	setFixtureBranch(t, repository, "ahead", aheadTip.Hash)
	setFixtureBranch(t, repository, "behind", root.Hash)
	setFixtureBranch(t, repository, "diverged", divergedTip.Hash)

	listed, err := service.listRepositoryBranches(
		context.Background(),
		&repositoryBranchesInput{
			AuthInput:  credentials,
			Repository: "engineering/api",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int{
		"ahead":    {1, 0},
		"behind":   {0, 1},
		"diverged": {1, 1},
		"main":     {0, 0},
	}
	if len(listed.Body.Branches) != len(want) {
		t.Fatalf("listed %d branches, want %d", len(listed.Body.Branches), len(want))
	}
	for _, branch := range listed.Body.Branches {
		expected, ok := want[branch.Name]
		if !ok {
			t.Fatalf("unexpected branch: %#v", branch)
		}
		if !branch.Compared || branch.Ahead != expected[0] || branch.Behind != expected[1] {
			t.Fatalf(
				"branch %s divergence = compared:%t ahead:%d behind:%d, want true/%d/%d",
				branch.Name,
				branch.Compared,
				branch.Ahead,
				branch.Behind,
				expected[0],
				expected[1],
			)
		}
	}

	deleted, err := service.deleteRepositoryBranch(
		context.Background(),
		&deleteRepositoryBranchInput{
			AuthInput:  credentials,
			Repository: "engineering/api",
			Branch:     "diverged",
			Body: deleteRepositoryBranchBody{
				ExpectedCommit: divergedTip.Hash.String(),
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Body.Repository != "engineering/api" ||
		deleted.Body.Name != "diverged" ||
		deleted.Body.Commit != divergedTip.Hash.String() {
		t.Fatalf("unexpected branch deletion response: %#v", deleted.Body)
	}
	if _, err = repository.Reference(
		plumbing.NewBranchReferenceName("diverged"),
		false,
	); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("deleted branch remains: %v", err)
	}
}

func TestRepositoryBranchDeletionRejectsDefaultStaleAndMissingBranches(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	repository := fixtureRepository(t, service)
	setFixtureBranch(t, repository, "feature", plumbing.NewHash(head))

	for _, test := range []struct {
		name     string
		branch   string
		expected string
		status   int
	}{
		{name: "default", branch: "main", expected: head, status: http.StatusConflict},
		{name: "stale", branch: "feature", expected: strings.Repeat("0", 40), status: http.StatusConflict},
		{name: "missing", branch: "missing", expected: head, status: http.StatusNotFound},
		{name: "invalid", branch: "bad branch", expected: head, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := service.deleteRepositoryBranch(
				context.Background(),
				&deleteRepositoryBranchInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Branch:     test.branch,
					Body: deleteRepositoryBranchBody{
						ExpectedCommit: test.expected,
					},
				},
			)
			requireDefaultBranchStatus(t, err, test.status)
		})
	}
}

func TestRepositoryBranchDeletionHTTPRoute(t *testing.T) {
	service, _, head := repositoryAPIFixture(t)
	if len(head) != 40 || !plumbing.IsHash(head) {
		t.Fatalf("fixture HEAD = %q, want a complete SHA-1 commit ID", head)
	}
	repository := fixtureRepository(t, service)
	setFixtureBranch(t, repository, "feature/docs", plumbing.NewHash(head))
	mux := http.NewServeMux()
	Register(mux, service)

	for _, expectedCommit := range []string{
		strings.Repeat("a", 39),
		strings.Repeat("a", 64),
	} {
		request := httptest.NewRequest(
			http.MethodDelete,
			"/api/repositories/engineering%2Fapi/branches/feature%2Fdocs",
			strings.NewReader(`{"expectedCommit":"`+expectedCommit+`"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		request.SetBasicAuth("alice", "secret")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		if response.Code != http.StatusUnprocessableEntity {
			t.Fatalf(
				"delete with %d-character commit status = %d, want %d: %s",
				len(expectedCommit),
				response.Code,
				http.StatusUnprocessableEntity,
				response.Body.String(),
			)
		}
	}

	request := httptest.NewRequest(
		http.MethodDelete,
		"/api/repositories/engineering%2Fapi/branches/feature%2Fdocs",
		strings.NewReader(`{"expectedCommit":"`+head+`"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("alice", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var deleted struct {
		Repository string `json:"repository"`
		Name       string `json:"name"`
		Commit     string `json:"commit"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &deleted); err != nil {
		t.Fatal(err)
	}
	if deleted.Repository != "engineering/api" ||
		deleted.Name != "feature/docs" ||
		deleted.Commit != head {
		t.Fatalf("unexpected HTTP branch deletion response: %#v", deleted)
	}
}
