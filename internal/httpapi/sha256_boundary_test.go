package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestRepositoryCommitAndEditInputsRequireCanonicalSHA256(t *testing.T) {
	service, credentials, head := repositoryAPIFixture(t)
	ctx := context.Background()

	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "legacy SHA-1", value: strings.Repeat("a", 40)},
		{name: "uppercase SHA-256", value: strings.Repeat("A", 64)},
		{name: "non-hex SHA-256", value: strings.Repeat("g", 64)},
		{name: "abbreviated SHA-256", value: strings.Repeat("a", 63)},
	} {
		t.Run(test.name+" diff", func(t *testing.T) {
			_, err := service.readRepositoryCommitDiff(ctx, &repositoryCommitDiffInput{
				AuthInput:  credentials,
				Repository: "engineering/api",
				Commit:     test.value,
			})
			requireStatusError(t, err, http.StatusBadRequest)
		})
		t.Run(test.name+" edit", func(t *testing.T) {
			_, err := service.updateRepositoryFile(ctx, &updateRepositoryFileInput{
				AuthInput:  credentials,
				Repository: "engineering/api",
				Ref:        "main",
				Path:       "README.md",
				Body: updateRepositoryFileBody{
					Content:        "must not be committed\n",
					ExpectedCommit: test.value,
				},
			})
			requireStatusError(t, err, http.StatusBadRequest)
		})
	}

	rootDiff, err := service.readRepositoryCommitDiff(ctx, &repositoryCommitDiffInput{
		AuthInput:  credentials,
		Repository: "engineering/api",
		Commit:     head,
	})
	if err != nil {
		t.Fatalf("read canonical root commit diff: %v", err)
	}
	if rootDiff.Body.Commit != head || rootDiff.Body.Parent != "" ||
		len(rootDiff.Body.Files) != 1 || rootDiff.Body.Files[0].Path != "README.md" {
		t.Fatalf("unexpected root commit diff: %#v", rootDiff.Body)
	}

	created, err := service.createRepositoryFile(ctx, &createRepositoryFileInput{
		AuthInput:  credentials,
		Repository: "engineering/api",
		Ref:        "main",
		Path:       "canonical.txt",
		Body: createRepositoryFileBody{
			Content:        "canonical SHA-256 input\n",
			ExpectedCommit: head,
		},
	})
	if err != nil {
		t.Fatalf("create file with canonical SHA-256 expected commit: %v", err)
	}
	if len(created.Body.Commit) != 64 || created.Body.Commit != strings.ToLower(created.Body.Commit) {
		t.Fatalf("created commit is not canonical SHA-256: %q", created.Body.Commit)
	}

	childDiff, err := service.readRepositoryCommitDiff(ctx, &repositoryCommitDiffInput{
		AuthInput:  credentials,
		Repository: "engineering/api",
		Commit:     created.Body.Commit,
	})
	if err != nil {
		t.Fatalf("read canonical child commit diff: %v", err)
	}
	if childDiff.Body.Commit != created.Body.Commit || childDiff.Body.Parent != head ||
		len(childDiff.Body.Files) != 1 || childDiff.Body.Files[0].Path != "canonical.txt" {
		t.Fatalf("unexpected child commit diff: %#v", childDiff.Body)
	}
}

func TestReviewMutationInputsRequireCanonicalSHA256(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	ctx := context.Background()

	input := func(value string) *approveMergeRequestInput {
		return &approveMergeRequestInput{
			MergeRequestInput: mergeRequestInput{
				AuthInput:  fixture.bob,
				Repository: fixture.path.Full(),
				ID:         created.Body.ID,
			},
			Body: approveMergeRequestBody{ExpectedHeadCommit: value},
		}
	}
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "legacy SHA-1", value: strings.Repeat("a", 40)},
		{name: "uppercase SHA-256", value: strings.Repeat("A", 64)},
		{name: "non-hex SHA-256", value: strings.Repeat("g", 64)},
	} {
		t.Run(test.name+" approval", func(t *testing.T) {
			_, err := fixture.service.approveMergeRequest(ctx, input(test.value))
			requireReviewHTTPStatus(t, err, http.StatusBadRequest)
		})
		t.Run(test.name+" merge", func(t *testing.T) {
			_, err := fixture.service.mergeApprovedRequest(ctx, input(test.value))
			requireReviewHTTPStatus(t, err, http.StatusBadRequest)
		})
	}

	_, err := fixture.service.approveMergeRequest(ctx, input(strings.Repeat("0", 64)))
	requireReviewHTTPStatus(t, err, http.StatusConflict)
	_, err = fixture.service.mergeApprovedRequest(ctx, input(fixture.head.String()))
	requireReviewHTTPStatus(t, err, http.StatusConflict)
}
