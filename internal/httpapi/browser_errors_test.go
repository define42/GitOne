package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
	git "github.com/go-git/go-git/v5"
)

type testIdentityProvider map[string]string

func (d testIdentityProvider) Authenticate(
	_ context.Context,
	username string,
	password string,
) (string, error) {
	if d[username] != password {
		return "", errors.New("invalid credentials")
	}
	return username, nil
}

func repositoryAPIFixture(t *testing.T) (API, AuthInput, string) {
	t.Helper()
	root := t.TempDir()
	store := storage.Store{Root: root}
	if err := store.CreateGroup("engineering", "alice", ""); err != nil {
		t.Fatal(err)
	}
	repositoryPath := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	if err := store.CreateRepository(repositoryPath, storage.CreateRepositoryOptions{
		InitializeReadme: true,
		Author:           "alice",
	}); err != nil {
		t.Fatal(err)
	}
	gitPath, err := store.GitPath(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	head, err := repository.Head()
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("alice", "secret")
	return API{
		Storage: store,
		Resolver: &auth.Resolver{
			Controls:  control.NewStore(root),
			Directory: testIdentityProvider{"alice": "secret"},
		},
	}, AuthInput{Authorization: request.Header.Get("Authorization")}, head.Hash().String()
}

func TestRepositoryBrowserRejectsInvalidReferencesAndPaths(t *testing.T) {
	service, credentials, _ := repositoryAPIFixture(t)
	ctx := context.Background()

	for _, test := range []struct {
		name string
		call func() error
	}{
		{
			name: "invalid repository",
			call: func() error {
				_, err := service.listRepositoryBranches(ctx, &repositoryBranchesInput{
					AuthInput:  credentials,
					Repository: "invalid",
				})
				return err
			},
		},
		{
			name: "missing repository",
			call: func() error {
				_, err := service.listRepositoryBranches(ctx, &repositoryBranchesInput{
					AuthInput:  credentials,
					Repository: "engineering/missing",
				})
				return err
			},
		},
		{
			name: "invalid new branch",
			call: func() error {
				_, err := service.createRepositoryBranch(ctx, &createRepositoryBranchInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Branch:     "bad branch",
					From:       "main",
				})
				return err
			},
		},
		{
			name: "invalid source branch",
			call: func() error {
				_, err := service.createRepositoryBranch(ctx, &createRepositoryBranchInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Branch:     "feature",
					From:       "bad branch",
				})
				return err
			},
		},
		{
			name: "missing tree reference",
			call: func() error {
				_, err := service.listRepositoryTree(
					ctx,
					credentials,
					"engineering/api",
					"missing",
					"",
				)
				return err
			},
		},
		{
			name: "invalid tree path",
			call: func() error {
				_, err := service.listRepositoryTree(
					ctx,
					credentials,
					"engineering/api",
					"main",
					"../outside",
				)
				return err
			},
		},
		{
			name: "missing directory",
			call: func() error {
				_, err := service.listRepositoryTree(
					ctx,
					credentials,
					"engineering/api",
					"main",
					"missing",
				)
				return err
			},
		},
		{
			name: "missing blob reference",
			call: func() error {
				_, err := service.readRepositoryBlob(ctx, &repositoryBrowserPathInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Ref:        "missing",
					Path:       "README.md",
				})
				return err
			},
		},
		{
			name: "invalid blob path",
			call: func() error {
				_, err := service.readRepositoryBlob(ctx, &repositoryBrowserPathInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Ref:        "main",
					Path:       "../README.md",
				})
				return err
			},
		},
		{
			name: "missing blob",
			call: func() error {
				_, err := service.readRepositoryBlob(ctx, &repositoryBrowserPathInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Ref:        "main",
					Path:       "missing.txt",
				})
				return err
			},
		},
		{
			name: "missing commit history reference",
			call: func() error {
				_, err := service.listRepositoryCommits(ctx, &repositoryCommitsInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Ref:        "missing",
					Limit:      20,
				})
				return err
			},
		},
		{
			name: "invalid commit diff hash",
			call: func() error {
				_, err := service.readRepositoryCommitDiff(ctx, &repositoryCommitDiffInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Commit:     "not-a-hash",
				})
				return err
			},
		},
		{
			name: "missing commit diff",
			call: func() error {
				_, err := service.readRepositoryCommitDiff(ctx, &repositoryCommitDiffInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
					Commit:     strings.Repeat("0", 40),
				})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); err == nil {
				t.Fatal("invalid repository request succeeded")
			}
		})
	}
}

func TestUpdateRepositoryFileRejectsInvalidContentAndState(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	ctx := context.Background()
	input := func() *updateRepositoryFileInput {
		return &updateRepositoryFileInput{
			AuthInput:  credentials,
			Repository: "engineering/api",
			Ref:        "main",
			Path:       "README.md",
			Body: updateRepositoryFileBody{
				Content:        "updated\n",
				ExpectedCommit: head,
			},
		}
	}

	for _, test := range []struct {
		name   string
		mutate func(*updateRepositoryFileInput)
	}{
		{
			name: "missing branch",
			mutate: func(input *updateRepositoryFileInput) {
				input.Ref = "missing"
			},
		},
		{
			name: "incomplete expected commit",
			mutate: func(input *updateRepositoryFileInput) {
				input.Body.ExpectedCommit = "1234"
			},
		},
		{
			name: "stale expected commit",
			mutate: func(input *updateRepositoryFileInput) {
				input.Body.ExpectedCommit = strings.Repeat("0", 40)
			},
		},
		{
			name: "invalid file path",
			mutate: func(input *updateRepositoryFileInput) {
				input.Path = "../README.md"
			},
		},
		{
			name: "oversized content",
			mutate: func(input *updateRepositoryFileInput) {
				input.Body.Content = strings.Repeat("x", maxEditableBlobSize+1)
			},
		},
		{
			name: "binary content",
			mutate: func(input *updateRepositoryFileInput) {
				input.Body.Content = "text\x00binary"
			},
		},
		{
			name: "missing file",
			mutate: func(input *updateRepositoryFileInput) {
				input.Path = "missing.txt"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := input()
			test.mutate(request)
			if _, err := service.updateRepositoryFile(ctx, request); err == nil {
				t.Fatal("invalid file update succeeded")
			}
		})
	}
}
