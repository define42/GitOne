package httpapi

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/issue"
)

func TestIssueStoreFallsBackToTheStorageRoot(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	fixture.create(t, fixture.bob, "Stored")
	fixture.service.Issues = nil

	listed, err := fixture.service.listIssues(context.Background(), &issuesInput{
		AuthInput:  fixture.bob,
		Repository: fixture.path.Full(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Body.Issues) != 1 {
		t.Fatalf("default issue store list = %#v", listed.Body.Issues)
	}
}

func TestIssueStoreFailuresReportServerErrors(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()
	blocked := filepath.Join(fixture.service.Storage.Root, "engineering", "api.issues")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := fixture.service.listIssues(ctx, &issuesInput{
		AuthInput:  fixture.bob,
		Repository: fixture.path.Full(),
	}); issueStatus(t, err) != 500 {
		t.Fatal("expected a 500 when the issue store cannot be listed")
	}
	if _, err := fixture.service.createIssue(ctx, &createIssueInput{
		AuthInput:  fixture.bob,
		Repository: fixture.path.Full(),
		Body:       createIssueBody{Title: "Blocked"},
	}); issueStatus(t, err) != 500 {
		t.Fatal("expected a 500 when the issue store cannot be written")
	}
}

func TestGetIssueRejectsInvalidRepositories(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	if _, err := fixture.service.getIssue(context.Background(), &IssueInput{
		AuthInput:  fixture.bob,
		Repository: "invalid",
		ID:         1,
	}); issueStatus(t, err) != 400 {
		t.Fatal("expected a 400 for an invalid repository path")
	}
}

func TestUpdateIssueValidatesEveryField(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()
	record := fixture.create(t, fixture.bob, "Editable")

	longTitle := strings.Repeat("t", issue.MaximumTitleBytes+1)
	longDescription := strings.Repeat("d", issue.MaximumBodyBytes+1)
	emptyName := []string{" "}
	for name, body := range map[string]updateIssueBody{
		"long title":       {Title: &longTitle},
		"long description": {Description: &longDescription},
		"empty label":      {Labels: &emptyName},
		"empty assignee":   {Assignees: &emptyName},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.service.updateIssue(ctx, &updateIssueInput{
				IssueInput: IssueInput{
					AuthInput:  fixture.bob,
					Repository: fixture.path.Full(),
					ID:         record.ID,
				},
				Body: body,
			}); issueStatus(t, err) != 400 {
				t.Fatalf("expected a validation error for %s", name)
			}
		})
	}

	description := "  Updated details  "
	assignees := []string{"bob", "carol"}
	updated, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: updateIssueBody{Description: &description, Assignees: &assignees},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Body.Description != "Updated details" ||
		len(updated.Body.Assignees) != 2 {
		t.Fatalf("unexpected updated issue: %#v", updated.Body)
	}

	if _, err = fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.carol,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: updateIssueBody{Description: &description},
	}); issueStatus(t, err) != 403 {
		t.Fatal("a reader must not update an issue")
	}
}

func TestIssueCommentsNumberSequentially(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()
	record := fixture.create(t, fixture.bob, "Discussion")

	for index := range 2 {
		commented, err := fixture.service.createIssueComment(ctx, &createIssueCommentInput{
			IssueInput: IssueInput{
				AuthInput:  fixture.carol,
				Repository: fixture.path.Full(),
				ID:         record.ID,
			},
			Body: createIssueCommentBody{Body: "Comment"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(commented.Body.Comments) != index+1 ||
			commented.Body.Comments[index].ID != uint64(index+1) {
			t.Fatalf("unexpected comments: %#v", commented.Body.Comments)
		}
	}

	if _, err := fixture.service.createIssueComment(ctx, &createIssueCommentInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.carol,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: createIssueCommentBody{
			Body: strings.Repeat("b", issue.MaximumBodyBytes+1),
		},
	}); issueStatus(t, err) != 400 {
		t.Fatal("expected an error for an oversized comment")
	}
}

func TestBuildIssueViewNormalizesEmptyCollections(t *testing.T) {
	view := buildIssueView(issue.Issue{ID: 1, Author: "bob"}, "bob", control.RoleDeveloper)
	if view.Labels == nil || view.Assignees == nil || view.Comments == nil {
		t.Fatalf("issue arrays were not normalized: %#v", view)
	}
	if !view.CanUpdate || !view.CanComment {
		t.Fatalf("author permissions were not reported: %#v", view)
	}
}
