package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/issue"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type issueAPIFixture struct {
	service API
	path    repopath.Repository
	alice   AuthInput
	bob     AuthInput
	carol   AuthInput
}

func newIssueAPIFixture(t *testing.T) issueAPIFixture {
	t.Helper()
	service, alice, _ := repositoryAPIFixture(t)
	path := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}

	document, err := service.Resolver.Controls.Load(context.Background(), path.Group())
	if err != nil {
		t.Fatal(err)
	}
	document.Members["bob"] = control.RoleDeveloper
	document.Members["carol"] = control.RoleRead
	if err = service.Storage.UpdateGroupControl(path.Group(), document, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate(path.Group())
	service.Resolver.Directory = testIdentityProvider{
		"alice": "secret",
		"bob":   "bob-secret",
		"carol": "carol-secret",
	}
	service.Issues = issue.NewStore(service.Storage.Root)

	return issueAPIFixture{
		service: service,
		path:    path,
		alice:   alice,
		bob:     mergeRequestCredentials(t, "bob", "bob-secret"),
		carol:   mergeRequestCredentials(t, "carol", "carol-secret"),
	}
}

func (f issueAPIFixture) create(
	t *testing.T,
	credentials AuthInput,
	title string,
) issueView {
	t.Helper()
	output, err := f.service.createIssue(context.Background(), &createIssueInput{
		AuthInput:  credentials,
		Repository: f.path.Full(),
		Body:       createIssueBody{Title: title, Description: "Details"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return output.Body
}

func issueStatus(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var statusErr huma.StatusError
	if !asStatusError(err, &statusErr) {
		t.Fatalf("error is not a status error: %v", err)
	}
	return statusErr.GetStatus()
}

func asStatusError(err error, target *huma.StatusError) bool {
	statusErr, ok := err.(huma.StatusError) //nolint:errorlint // Huma returns concrete errors.
	if !ok {
		return false
	}
	*target = statusErr
	return true
}

func TestIssueLifecycle(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()

	created := fixture.create(t, fixture.bob, "Login fails")
	if created.ID != 1 || created.State != issue.StateOpen || created.Author != "bob" {
		t.Fatalf("unexpected created issue: %#v", created)
	}
	if !created.CanComment || !created.CanUpdate {
		t.Fatalf("author permissions were not reported: %#v", created)
	}
	if created.SuggestedBranch != "issue-1-details" {
		t.Fatalf("suggested branch = %q, want issue-1-details", created.SuggestedBranch)
	}
	if created.Labels == nil || created.Assignees == nil || created.Comments == nil {
		t.Fatalf("issue arrays were not normalized: %#v", created)
	}

	second := fixture.create(t, fixture.alice, "Docs are stale")
	if second.ID != 2 {
		t.Fatalf("second issue ID = %d, want 2", second.ID)
	}

	commented, err := fixture.service.createIssueComment(ctx, &createIssueCommentInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.carol,
			Repository: fixture.path.Full(),
			ID:         created.ID,
		},
		Body: createIssueCommentBody{Body: "I see this too"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commented.Body.Comments) != 1 ||
		commented.Body.Comments[0].Author != "carol" ||
		commented.Body.Comments[0].ID != 1 {
		t.Fatalf("unexpected comment: %#v", commented.Body.Comments)
	}
	if commented.Body.CanUpdate {
		t.Fatalf("a reader must not be able to update an issue: %#v", commented.Body)
	}

	closedState := string(issue.StateClosed)
	label := "bug"
	closed, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.ID,
		},
		Body: updateIssueBody{
			State:  &closedState,
			Labels: &[]string{label, label},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Body.State != issue.StateClosed || closed.Body.ClosedBy != "bob" ||
		closed.Body.ClosedAt == nil {
		t.Fatalf("unexpected closed issue: %#v", closed.Body)
	}
	if len(closed.Body.Labels) != 1 || closed.Body.Labels[0] != label {
		t.Fatalf("labels were not deduplicated: %#v", closed.Body.Labels)
	}

	open, err := fixture.service.listIssues(ctx, &issuesInput{
		AuthInput:  fixture.carol,
		Repository: fixture.path.Full(),
		State:      "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(open.Body.Issues) != 1 || open.Body.Issues[0].ID != second.ID {
		t.Fatalf("unexpected open issues: %#v", open.Body.Issues)
	}

	all, err := fixture.service.listIssues(ctx, &issuesInput{
		AuthInput:  fixture.carol,
		Repository: fixture.path.Full(),
		State:      "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Body.Issues) != 2 || all.Body.Issues[0].ID != 2 {
		t.Fatalf("unexpected issue list: %#v", all.Body.Issues)
	}

	fetched, err := fixture.service.getIssue(ctx, &IssueInput{
		AuthInput:  fixture.bob,
		Repository: fixture.path.Full(),
		ID:         created.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Body.State != issue.StateClosed || len(fetched.Body.Comments) != 1 {
		t.Fatalf("unexpected fetched issue: %#v", fetched.Body)
	}

	openState := string(issue.StateOpen)
	reopened, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.ID,
		},
		Body: updateIssueBody{State: &openState},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Body.State != issue.StateOpen || reopened.Body.ClosedBy != "" ||
		reopened.Body.ClosedAt != nil {
		t.Fatalf("closure metadata was not cleared: %#v", reopened.Body)
	}
}

func TestSuggestedIssueBranch(t *testing.T) {
	longDescription := strings.Repeat("word ", 40)
	exactBoundary := strings.Repeat("a", maximumIssueBranchSlugBytes)
	overBoundary := exactBoundary + "a"
	separatorFits := strings.Repeat("a", maximumIssueBranchSlugBytes-2) + " b"
	separatorDoesNotFit := strings.Repeat("a", maximumIssueBranchSlugBytes-1) + " b"
	twoByteBoundary := strings.Repeat("é", maximumIssueBranchSlugBytes/2)
	threeByteBoundary := strings.Repeat("界", maximumIssueBranchSlugBytes/3)
	fourByteBoundary := strings.Repeat("𐍈", maximumIssueBranchSlugBytes/4)
	tests := []struct {
		name        string
		id          uint64
		description string
		title       string
		want        string
	}{
		{name: "description", id: 12, description: "Fix Login Redirect", title: "ignored", want: "issue-12-fix-login-redirect"},
		{name: "markdown and forbidden ref characters", id: 3, description: "**Crash** on `foo.lock` / @{main} ~^:?*[\\", want: "issue-3-crash-on-foo-lock-main"},
		{name: "collapsed separators", id: 4, description: "  one ... two --- three  ", want: "issue-4-one-two-three"},
		{name: "title fallback", id: 5, description: " \n ", title: "Missing description", want: "issue-5-missing-description"},
		{name: "unicode description", id: 6, description: "ÆØÅ 🚀", title: "unused", want: "issue-6-æøå"},
		{name: "non-latin description", id: 16, description: "修正 ログイン", want: "issue-16-修正-ログイン"},
		{name: "composed unicode", id: 17, description: "Åland", want: "issue-17-åland"},
		{name: "decomposed unicode", id: 18, description: "A\u030aland", want: "issue-18-åland"},
		{name: "attached unicode marks", id: 19, description: "सुधार लॉगिन", want: "issue-19-सुधार-लॉगिन"},
		{name: "leading forbidden punctuation", id: 7, description: "../@{~^:?*[\\.lock", want: "issue-7-lock"},
		{name: "punctuation only", id: 9, description: "../@{~^:?*[\\", want: "issue-9"},
		{name: "shell and filesystem punctuation", id: 10, description: "safe$;|\"'<>name\x00\x1f\x7fend", want: "issue-10-safe-name-end"},
		{name: "bounded", id: 8, description: longDescription, want: "issue-8-" + strings.TrimSuffix(strings.Repeat("word-", 16), "-")},
		{name: "exact boundary", id: 11, description: exactBoundary, want: "issue-11-" + exactBoundary},
		{name: "over boundary", id: 13, description: overBoundary, want: "issue-13-" + exactBoundary},
		{name: "separator fits boundary", id: 14, description: separatorFits, want: "issue-14-" + strings.Repeat("a", maximumIssueBranchSlugBytes-2) + "-b"},
		{name: "separator cannot fit boundary", id: 15, description: separatorDoesNotFit, want: "issue-15-" + strings.Repeat("a", maximumIssueBranchSlugBytes-1)},
		{name: "two byte boundary", id: 20, description: twoByteBoundary + "é", want: "issue-20-" + twoByteBoundary},
		{name: "three byte boundary", id: 21, description: threeByteBoundary + "界", want: "issue-21-" + threeByteBoundary},
		{name: "four byte boundary", id: 22, description: fourByteBoundary + "𐍈", want: "issue-22-" + fourByteBoundary},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := suggestedIssueBranch(issue.Issue{
				ID:          test.id,
				Description: test.description,
				Title:       test.title,
			})
			if got != test.want {
				t.Fatalf("suggested branch = %q, want %q", got, test.want)
			}
			if _, err := validatedBranchReference(got); err != nil {
				t.Fatalf("suggested branch is not a valid Git ref: %v", err)
			}
			prefix := "issue-" + strconv.FormatUint(test.id, 10)
			slug := strings.TrimPrefix(got, prefix)
			if slug != "" && !strings.HasPrefix(slug, "-") {
				t.Fatalf("suggested branch does not use the issue prefix: %q", got)
			}
			if len(strings.TrimPrefix(slug, "-")) > maximumIssueBranchSlugBytes {
				t.Fatalf("suggested branch slug is too long: %q", got)
			}
			if !utf8.ValidString(got) {
				t.Fatalf("suggested branch is not valid UTF-8: %q", got)
			}
			for _, character := range got {
				if !unicode.IsLetter(character) && !unicode.IsNumber(character) &&
					!unicode.IsMark(character) && character != '-' {
					t.Fatalf("suggested branch contains a Git-unsafe character %q: %q", character, got)
				}
			}
		})
	}

	first := suggestedIssueBranch(issue.Issue{ID: 1, Description: "Same work"})
	second := suggestedIssueBranch(issue.Issue{ID: 2, Description: "Same work"})
	if first == second {
		t.Fatalf("different issues received the same suggested branch: %q", first)
	}
}

func TestIssueBranchLifecycle(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()
	record := fixture.create(t, fixture.alice, "Branch this issue")
	branchName := issueBranchPrefix(record.ID) + "-custom-work"

	created, err := fixture.service.createIssueBranch(ctx, &createIssueBranchInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: createIssueBranchBody{Name: branchName, From: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Body.IssueID != record.ID || created.Body.Name != branchName ||
		created.Body.From != "main" || created.Body.CreatedBy != "bob" ||
		created.Body.CreatedAt.IsZero() {
		t.Fatalf("unexpected issue branch response: %#v", created.Body)
	}

	stored, err := fixture.service.issueStore().Get(fixture.path, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Branch != branchName || stored.BranchCreatedBy != "bob" ||
		stored.BranchCreatedAt == nil ||
		!stored.BranchCreatedAt.Equal(created.Body.CreatedAt) {
		t.Fatalf("issue branch metadata was not persisted: %#v", stored)
	}
	fetched, err := fixture.service.getIssue(ctx, &IssueInput{
		AuthInput: fixture.carol, Repository: fixture.path.Full(), ID: record.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Body.Branch != stored.Branch ||
		fetched.Body.BranchCreatedBy != stored.BranchCreatedBy ||
		fetched.Body.BranchCreatedAt == nil {
		t.Fatalf("issue branch metadata was not returned: %#v", fetched.Body)
	}

	repositoryPath, err := fixture.service.Storage.GitPath(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		t.Fatal(err)
	}
	branch, err := repository.Reference(
		plumbing.NewBranchReferenceName(branchName),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if branch.Hash().String() != created.Body.Commit {
		t.Fatalf("created branch commit = %s, want %s", branch.Hash(), created.Body.Commit)
	}

	if _, err = fixture.service.createIssueBranch(ctx, &createIssueBranchInput{
		IssueInput: IssueInput{
			AuthInput: fixture.bob, Repository: fixture.path.Full(), ID: record.ID,
		},
		Body: createIssueBranchBody{Name: branchName, From: "main"},
	}); issueStatus(t, err) != 409 {
		t.Fatal("an issue received more than one persisted branch")
	}
}

func TestIssueBranchValidationAndAuthorization(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()

	readerIssue := fixture.create(t, fixture.bob, "Reader cannot branch")
	if _, err := fixture.service.createIssueBranch(ctx, &createIssueBranchInput{
		IssueInput: IssueInput{
			AuthInput: fixture.carol, Repository: fixture.path.Full(), ID: readerIssue.ID,
		},
		Body: createIssueBranchBody{Name: readerIssue.SuggestedBranch, From: "main"},
	}); issueStatus(t, err) != 403 {
		t.Fatal("a reader created an issue branch")
	}

	invalidPrefix := fixture.create(t, fixture.bob, "Prefix required")
	if _, err := fixture.service.createIssueBranch(ctx, &createIssueBranchInput{
		IssueInput: IssueInput{
			AuthInput: fixture.bob, Repository: fixture.path.Full(), ID: invalidPrefix.ID,
		},
		Body: createIssueBranchBody{Name: "unrelated-branch", From: "main"},
	}); issueStatus(t, err) != 400 {
		t.Fatal("an issue branch without the issue prefix was accepted")
	}

	missingSource := fixture.create(t, fixture.bob, "Missing source")
	if _, err := fixture.service.createIssueBranch(ctx, &createIssueBranchInput{
		IssueInput: IssueInput{
			AuthInput: fixture.bob, Repository: fixture.path.Full(), ID: missingSource.ID,
		},
		Body: createIssueBranchBody{Name: missingSource.SuggestedBranch, From: "missing"},
	}); issueStatus(t, err) != 404 {
		t.Fatal("an issue branch was created from a missing source")
	}
	stored, err := fixture.service.issueStore().Get(fixture.path, missingSource.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Branch != "" || stored.BranchCreatedBy != "" || stored.BranchCreatedAt != nil {
		t.Fatalf("failed branch creation changed the issue: %#v", stored)
	}
}

func TestIssueBranchHTTPRoute(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	record := fixture.create(t, fixture.bob, "HTTP branch")
	mux := http.NewServeMux()
	Register(mux, fixture.service)

	request := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/issues/1/branch",
		strings.NewReader(`{"name":"`+record.SuggestedBranch+`","from":"main"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	request.SetBasicAuth("alice", "secret")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf(
			"issue branch HTTP status = %d, want %d: %s",
			response.Code,
			http.StatusCreated,
			response.Body.String(),
		)
	}
	var created createIssueBranchOutput
	if err := json.Unmarshal(response.Body.Bytes(), &created.Body); err != nil {
		t.Fatal(err)
	}
	if created.Body.IssueID != record.ID || created.Body.Name != record.SuggestedBranch ||
		created.Body.CreatedBy != "alice" || created.Body.CreatedAt.IsZero() {
		t.Fatalf("unexpected HTTP issue branch response: %#v", created.Body)
	}
}

func TestIssueAuthorization(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()
	record := fixture.create(t, fixture.bob, "Reported by bob")

	if _, err := fixture.service.createIssue(ctx, &createIssueInput{
		AuthInput:  fixture.carol,
		Repository: fixture.path.Full(),
		Body:       createIssueBody{Title: "Not allowed"},
	}); issueStatus(t, err) != 403 {
		t.Fatal("a reader must not create issues")
	}

	if _, err := fixture.service.createIssueComment(ctx, &createIssueCommentInput{
		IssueInput: IssueInput{
			AuthInput:  AuthInput{},
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: createIssueCommentBody{Body: "anonymous"},
	}); err == nil {
		t.Fatal("an anonymous user must not comment on a private repository issue")
	}

	title := "Renamed by another developer"
	other := fixture.create(t, fixture.alice, "Reported by alice")
	if _, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         other.ID,
		},
		Body: updateIssueBody{Title: &title},
	}); issueStatus(t, err) != 403 {
		t.Fatal("a developer must not edit another author's issue")
	}

	maintained, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.alice,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: updateIssueBody{Title: &title},
	})
	if err != nil {
		t.Fatal(err)
	}
	if maintained.Body.Title != title {
		t.Fatalf("an owner could not edit another author's issue: %#v", maintained.Body)
	}
}

func TestIssueVisibilityAllowsPublicReads(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()
	record := fixture.create(t, fixture.bob, "Public issue")

	document, err := fixture.service.Resolver.Controls.Load(ctx, fixture.path.Group())
	if err != nil {
		t.Fatal(err)
	}
	document.Visibility = "public"
	if err = fixture.service.Storage.UpdateGroupControl(
		fixture.path.Group(),
		document,
		"alice",
	); err != nil {
		t.Fatal(err)
	}
	fixture.service.Resolver.Controls.Invalidate(fixture.path.Group())

	listed, err := fixture.service.listIssues(ctx, &issuesInput{
		Repository: fixture.path.Full(),
		State:      "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Body.Issues) != 1 {
		t.Fatalf("anonymous list = %#v", listed.Body.Issues)
	}
	if listed.Body.Issues[0].CanComment || listed.Body.Issues[0].CanUpdate {
		t.Fatalf("anonymous readers must not receive write permissions: %#v", listed.Body.Issues[0])
	}

	fetched, err := fixture.service.getIssue(ctx, &IssueInput{
		Repository: fixture.path.Full(),
		ID:         record.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if fetched.Body.CanComment {
		t.Fatalf("anonymous readers must not be able to comment: %#v", fetched.Body)
	}
}

func TestIssueValidation(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()
	record := fixture.create(t, fixture.bob, "Valid")

	longLabels := make([]string, 0, issue.MaximumLabels+1)
	for index := range issue.MaximumLabels + 1 {
		longLabels = append(longLabels, strings.Repeat("l", index+1))
	}
	for name, body := range map[string]createIssueBody{
		"empty title": {Title: "   "},
		"long title":  {Title: strings.Repeat("t", issue.MaximumTitleBytes+1)},
		"long description": {
			Title:       "Valid",
			Description: strings.Repeat("d", issue.MaximumBodyBytes+1),
		},
		"empty label":     {Title: "Valid", Labels: []string{" "}},
		"long label":      {Title: "Valid", Labels: []string{strings.Repeat("l", 101)}},
		"too many labels": {Title: "Valid", Labels: longLabels},
		"empty assignee":  {Title: "Valid", Assignees: []string{""}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := fixture.service.createIssue(ctx, &createIssueInput{
				AuthInput:  fixture.bob,
				Repository: fixture.path.Full(),
				Body:       body,
			}); issueStatus(t, err) != 400 {
				t.Fatalf("expected a validation error for %s", name)
			}
		})
	}

	if _, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: updateIssueBody{},
	}); issueStatus(t, err) != 400 {
		t.Fatal("expected an error for an empty update")
	}

	invalidState := "merged"
	if _, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: updateIssueBody{State: &invalidState},
	}); issueStatus(t, err) != 400 {
		t.Fatal("expected an error for an invalid state")
	}

	emptyTitle := "  "
	if _, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: updateIssueBody{Title: &emptyTitle},
	}); issueStatus(t, err) != 400 {
		t.Fatal("expected an error for an empty title")
	}

	blank := "   "
	if _, err := fixture.service.createIssueComment(ctx, &createIssueCommentInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         record.ID,
		},
		Body: createIssueCommentBody{Body: blank},
	}); issueStatus(t, err) != 400 {
		t.Fatal("expected an error for an empty comment")
	}
}

func TestIssueNotFoundAndInvalidRepository(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()

	if _, err := fixture.service.getIssue(ctx, &IssueInput{
		AuthInput:  fixture.bob,
		Repository: fixture.path.Full(),
		ID:         42,
	}); issueStatus(t, err) != 404 {
		t.Fatal("expected a 404 for an unknown issue")
	}
	if _, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         42,
		},
		Body: updateIssueBody{Title: stringPointer("New title")},
	}); issueStatus(t, err) != 404 {
		t.Fatal("expected a 404 for an unknown issue")
	}
	if _, err := fixture.service.createIssueComment(ctx, &createIssueCommentInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         42,
		},
		Body: createIssueCommentBody{Body: "hello"},
	}); issueStatus(t, err) != 404 {
		t.Fatal("expected a 404 for an unknown issue")
	}
	if _, err := fixture.service.listIssues(ctx, &issuesInput{
		AuthInput:  fixture.bob,
		Repository: "invalid",
	}); issueStatus(t, err) != 400 {
		t.Fatal("expected a 400 for an invalid repository path")
	}
	if _, err := fixture.service.createIssue(ctx, &createIssueInput{
		AuthInput:  fixture.bob,
		Repository: "engineering/absent",
		Body:       createIssueBody{Title: "Missing repository"},
	}); issueStatus(t, err) != 404 {
		t.Fatal("expected a 404 for a missing repository")
	}
}

func stringPointer(value string) *string {
	return &value
}
