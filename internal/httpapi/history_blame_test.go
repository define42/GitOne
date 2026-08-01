package httpapi

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestRepositoryCommitPaginationBeyondHundred(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	ctx := context.Background()
	repository, _, err := service.openBrowsableRepository(
		ctx,
		credentials,
		"engineering/api",
	)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := repository.CommitObject(plumbing.NewHash(head))
	if err != nil {
		t.Fatal(err)
	}
	treeHash := parent.TreeHash
	parentHash := parent.Hash
	baseTime := time.Date(2026, time.July, 1, 12, 0, 0, 0, time.UTC)
	for index := 1; index <= 105; index++ {
		signature := object.Signature{
			Name:  "alice",
			Email: "alice@localhost",
			When:  baseTime.Add(time.Duration(index) * time.Minute),
		}
		commit := &object.Commit{
			Author:       signature,
			Committer:    signature,
			Message:      fmt.Sprintf("pagination commit %d\n", index),
			TreeHash:     treeHash,
			ParentHashes: []plumbing.Hash{parentHash},
		}
		encoded := repository.Storer.NewEncodedObject()
		if err = commit.Encode(encoded); err != nil {
			t.Fatal(err)
		}
		parentHash, err = repository.Storer.SetEncodedObject(encoded)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		parentHash,
	)); err != nil {
		t.Fatal(err)
	}

	first, err := service.listRepositoryCommits(ctx, &repositoryCommitsInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Page: 1, PerPage: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.Body.Page != 1 ||
		first.Body.PerPage != 100 ||
		first.Body.Total != nil ||
		first.Body.TotalPages != nil ||
		first.Body.HasPrevious ||
		!first.Body.HasNext ||
		len(first.Body.Commits) != 100 ||
		first.Body.Commits[0].Message != "pagination commit 105\n" {
		t.Fatalf("first commit page = %#v", first.Body)
	}

	second, err := service.listRepositoryCommits(ctx, &repositoryCommitsInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Page: 2, PerPage: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.Body.Page != 2 ||
		second.Body.Total == nil ||
		*second.Body.Total != 106 ||
		second.Body.TotalPages == nil ||
		*second.Body.TotalPages != 2 ||
		!second.Body.HasPrevious ||
		second.Body.HasNext ||
		len(second.Body.Commits) != 6 ||
		second.Body.Commits[0].Message != "pagination commit 5\n" {
		t.Fatalf("second commit page = %#v", second.Body)
	}

	if _, err = service.listRepositoryCommits(ctx, &repositoryCommitsInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Page: 1_000_001, PerPage: 50,
	}); err == nil {
		t.Fatal("invalid commit page was accepted")
	}
	if _, err = service.listRepositoryCommits(ctx, &repositoryCommitsInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Page: maximumCommitHistoryWalk + 1, PerPage: 1,
	}); err == nil {
		t.Fatal("unbounded commit history page was accepted")
	}
}

func TestRepositoryCommitPageStopsAfterLookahead(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	repository, _, err := service.openBrowsableRepository(
		context.Background(),
		credentials,
		"engineering/api",
	)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := repository.CommitObject(plumbing.NewHash(head))
	if err != nil {
		t.Fatal(err)
	}
	parentHash := initial.Hash
	for index := 1; index <= 2; index++ {
		signature := object.Signature{
			Name:  "alice",
			Email: "alice@localhost",
			When:  initial.Author.When.Add(time.Duration(index) * time.Minute),
		}
		commit := &object.Commit{
			Author:       signature,
			Committer:    signature,
			Message:      fmt.Sprintf("bounded commit %d\n", index),
			TreeHash:     initial.TreeHash,
			ParentHashes: []plumbing.Hash{parentHash},
		}
		encoded := repository.Storer.NewEncodedObject()
		if err = commit.Encode(encoded); err != nil {
			t.Fatal(err)
		}
		parentHash, err = repository.Storer.SetEncodedObject(encoded)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		parentHash,
	)); err != nil {
		t.Fatal(err)
	}
	if err = repository.DeleteObject(initial.Hash); err != nil {
		t.Fatal(err)
	}

	page, err := service.listRepositoryCommits(
		context.Background(),
		&repositoryCommitsInput{
			AuthInput: credentials, Repository: "engineering/api", Ref: "main",
			Page: 1, PerPage: 1,
		},
	)
	if err != nil {
		t.Fatalf("first page traversed beyond its lookahead: %v", err)
	}
	if len(page.Body.Commits) != 1 ||
		!page.Body.HasNext ||
		page.Body.Total != nil ||
		page.Body.Commits[0].Message != "bounded commit 2\n" {
		t.Fatalf("bounded commit page = %#v", page.Body)
	}
}

func TestRepositoryCommitPageHonorsCanceledContext(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := service.listRepositoryCommits(ctx, &repositoryCommitsInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Page: 1, PerPage: 1,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled commit history request returned %v", err)
	}
}

func TestRepositoryBlameAttributesEachLine(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	ctx := context.Background()

	first, err := service.updateRepositoryFile(ctx, &updateRepositoryFileInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Path: "README.md",
		Body: updateRepositoryFileBody{
			Content: "original\nshared\n", ExpectedCommit: head,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.updateRepositoryFile(ctx, &updateRepositoryFileInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Path: "README.md",
		Body: updateRepositoryFileBody{
			Content: "changed\nshared\n", ExpectedCommit: first.Body.Commit,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	blame, err := service.readRepositoryBlame(ctx, &repositoryBrowserPathInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Path: "README.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	if blame.Body.Repository != "engineering/api" ||
		blame.Body.Ref != "main" ||
		blame.Body.Commit != second.Body.Commit ||
		blame.Body.Path != "README.md" ||
		len(blame.Body.Lines) != 2 {
		t.Fatalf("blame = %#v", blame.Body)
	}
	if blame.Body.Lines[0].Number != 1 ||
		blame.Body.Lines[0].Text != "changed" ||
		blame.Body.Lines[0].Commit != second.Body.Commit ||
		blame.Body.Lines[0].Author != "alice" ||
		blame.Body.Lines[0].Email != "alice@localhost" {
		t.Fatalf("first blame line = %#v", blame.Body.Lines[0])
	}
	if blame.Body.Lines[1].Number != 2 ||
		blame.Body.Lines[1].Text != "shared" ||
		blame.Body.Lines[1].Commit != first.Body.Commit {
		t.Fatalf("second blame line = %#v", blame.Body.Lines[1])
	}

	created, err := service.createRepositoryFile(ctx, &createRepositoryFileInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Path: "blank.txt",
		Body: createRepositoryFileBody{
			Content: "", ExpectedCommit: second.Body.Commit,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	blank, err := service.readRepositoryBlame(ctx, &repositoryBrowserPathInput{
		AuthInput: credentials, Repository: "engineering/api", Ref: "main",
		Path: "blank.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if blank.Body.Commit != created.Body.Commit || len(blank.Body.Lines) != 0 {
		t.Fatalf("blank file blame = %#v", blank.Body)
	}
}
