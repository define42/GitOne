package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/issue"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/go-git/go-git/v5/plumbing"
	"golang.org/x/text/unicode/norm"
)

type issuesInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	State      string `query:"state" enum:"open,closed,all" default:"open" doc:"Issue state to return"`
}

// IssueInput identifies one persisted issue.
type IssueInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	ID         uint64 `path:"id" minimum:"1" doc:"Issue number"`
}

type createIssueBody struct {
	Title       string   `json:"title" minLength:"1" maxLength:"500"`
	Description string   `json:"description,omitempty" maxLength:"65536"`
	Labels      []string `json:"labels,omitempty" maxItems:"32"`
	Assignees   []string `json:"assignees,omitempty" maxItems:"32"`
}

type createIssueInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Body       createIssueBody
}

type updateIssueBody struct {
	Title       *string   `json:"title,omitempty" maxLength:"500"`
	Description *string   `json:"description,omitempty" maxLength:"65536"`
	State       *string   `json:"state,omitempty" enum:"open,closed"`
	Labels      *[]string `json:"labels,omitempty" maxItems:"32"`
	Assignees   *[]string `json:"assignees,omitempty" maxItems:"32"`
}

type updateIssueInput struct {
	IssueInput
	Body updateIssueBody
}

type createIssueCommentBody struct {
	Body string `json:"body" minLength:"1" maxLength:"65536"`
}

type createIssueCommentInput struct {
	IssueInput
	Body createIssueCommentBody
}

type createIssueBranchBody struct {
	Name string `json:"name" minLength:"1" maxLength:"1024" doc:"Git-safe name for the issue branch"`
	From string `json:"from" minLength:"1" doc:"Existing branch from which the issue branch is created"`
}

type createIssueBranchInput struct {
	IssueInput
	Body createIssueBranchBody
}

// issueCommentView mirrors issue.Comment under a distinct OpenAPI schema name
// so that it does not collide with the merge request comment schema.
type issueCommentView struct {
	ID        uint64    `json:"id"`
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

type issueView struct {
	ID              uint64             `json:"id"`
	Repository      string             `json:"repository"`
	Title           string             `json:"title"`
	Description     string             `json:"description"`
	SuggestedBranch string             `json:"suggestedBranch" doc:"Git-safe branch name derived from the issue description"`
	Author          string             `json:"author"`
	State           issue.State        `json:"state"`
	CreatedAt       time.Time          `json:"createdAt"`
	UpdatedAt       time.Time          `json:"updatedAt"`
	Labels          []string           `json:"labels"`
	Assignees       []string           `json:"assignees"`
	Comments        []issueCommentView `json:"comments"`
	Branch          string             `json:"branch,omitempty"`
	BranchCreatedBy string             `json:"branchCreatedBy,omitempty"`
	BranchCreatedAt *time.Time         `json:"branchCreatedAt,omitempty"`
	ClosedBy        string             `json:"closedBy,omitempty"`
	ClosedAt        *time.Time         `json:"closedAt,omitempty"`
	CanComment      bool               `json:"canComment"`
	CanUpdate       bool               `json:"canUpdate"`
}

type issuesOutput struct {
	Body struct {
		Repository string      `json:"repository"`
		Issues     []issueView `json:"issues"`
	}
}

type issueOutput struct {
	Body issueView
}

type createIssueBranchOutput struct {
	Body struct {
		Repository string    `json:"repository"`
		IssueID    uint64    `json:"issueId"`
		Name       string    `json:"name"`
		From       string    `json:"from"`
		Commit     string    `json:"commit"`
		CreatedBy  string    `json:"createdBy"`
		CreatedAt  time.Time `json:"createdAt"`
	}
}

func registerIssueAPI(api huma.API, service API) {
	huma.Register(api, protected(huma.Operation{
		OperationID: "list-issues",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/issues",
		Summary:     "List persisted repository issues",
		Tags:        []string{"Issues"},
	}), service.listIssues)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-issue",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/issues",
		Summary:       "Create a repository issue",
		Tags:          []string{"Issues"},
		DefaultStatus: http.StatusCreated,
	}), service.createIssue)

	huma.Register(api, protected(huma.Operation{
		OperationID: "get-issue",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/issues/{id}",
		Summary:     "Get one repository issue with its discussion",
		Tags:        []string{"Issues"},
	}), service.getIssue)

	huma.Register(api, protected(huma.Operation{
		OperationID: "update-issue",
		Method:      http.MethodPatch,
		Path:        "/api/repositories/{repository}/issues/{id}",
		Summary:     "Edit an issue or close and reopen it",
		Tags:        []string{"Issues"},
	}), service.updateIssue)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-issue-comment",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/issues/{id}/comments",
		Summary:       "Comment on a repository issue",
		Tags:          []string{"Issues"},
		DefaultStatus: http.StatusCreated,
	}), service.createIssueComment)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-issue-branch",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/issues/{id}/branch",
		Summary:       "Create and record a branch for a repository issue",
		Tags:          []string{"Issues"},
		DefaultStatus: http.StatusCreated,
	}), service.createIssueBranch)
}

func (a API) issueStore() *issue.Store {
	if a.Issues != nil {
		return a.Issues
	}
	return issue.NewStore(a.Storage.Root)
}

func (a API) listIssues(ctx context.Context, input *issuesInput) (*issuesOutput, error) {
	_, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
	if err != nil {
		return nil, err
	}
	records, err := a.issueStore().List(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not list issues", err)
	}
	state := input.State
	if state == "" {
		state = string(issue.StateOpen)
	}
	principal, role := a.issuePrincipal(ctx, input.AuthInput, parsed)
	output := &issuesOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Issues = make([]issueView, 0, len(records))
	for _, record := range records {
		if state != "all" && string(record.State) != state {
			continue
		}
		output.Body.Issues = append(
			output.Body.Issues,
			buildIssueView(record, principal, role),
		)
	}
	return output, nil
}

func (a API) createIssue(ctx context.Context, input *createIssueInput) (*issueOutput, error) {
	title := strings.TrimSpace(input.Body.Title)
	description := strings.TrimSpace(input.Body.Description)
	if title == "" {
		return nil, huma.Error400BadRequest("title is required")
	}
	if len(title) > issue.MaximumTitleBytes || len(description) > issue.MaximumBodyBytes {
		return nil, huma.Error400BadRequest("issue text is too long")
	}
	labels, err := normalizedIssueNames(input.Body.Labels, issue.MaximumLabels, issue.MaximumLabelBytes, "label")
	if err != nil {
		return nil, err
	}
	assignees, err := normalizedIssueNames(
		input.Body.Assignees,
		issue.MaximumAssignees,
		issue.MaximumAssigneeBytes,
		"assignee",
	)
	if err != nil {
		return nil, err
	}
	parsed, principal, release, err := a.openLockedIssueRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
	}
	defer release()

	now := time.Now().UTC()
	record := issue.Issue{
		Repository:  parsed.Full(),
		Title:       title,
		Description: description,
		Author:      principal.Name,
		State:       issue.StateOpen,
		CreatedAt:   now,
		UpdatedAt:   now,
		Labels:      labels,
		Assignees:   assignees,
		Comments:    []issue.Comment{},
	}
	if err = a.issueStore().Create(parsed, &record); err != nil {
		return nil, issueMutationError("could not create issue", err)
	}
	return &issueOutput{
		Body: buildIssueView(record, principal.Name, principal.Role),
	}, nil
}

func (a API) getIssue(ctx context.Context, input *IssueInput) (*issueOutput, error) {
	_, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
	if err != nil {
		return nil, err
	}
	record, err := a.issueStore().Get(parsed, input.ID)
	if err != nil {
		return nil, issueMutationError("could not read issue", err)
	}
	principal, role := a.issuePrincipal(ctx, input.AuthInput, parsed)
	return &issueOutput{Body: buildIssueView(record, principal, role)}, nil
}

func (a API) updateIssue(ctx context.Context, input *updateIssueInput) (*issueOutput, error) {
	if input.Body.Title == nil && input.Body.Description == nil &&
		input.Body.State == nil && input.Body.Labels == nil && input.Body.Assignees == nil {
		return nil, huma.Error400BadRequest("no issue changes were requested")
	}
	var title string
	if input.Body.Title != nil {
		title = strings.TrimSpace(*input.Body.Title)
		if title == "" {
			return nil, huma.Error400BadRequest("title is required")
		}
		if len(title) > issue.MaximumTitleBytes {
			return nil, huma.Error400BadRequest("issue text is too long")
		}
	}
	var description string
	if input.Body.Description != nil {
		description = strings.TrimSpace(*input.Body.Description)
		if len(description) > issue.MaximumBodyBytes {
			return nil, huma.Error400BadRequest("issue text is too long")
		}
	}
	var state issue.State
	if input.Body.State != nil {
		state = issue.State(*input.Body.State)
		if state != issue.StateOpen && state != issue.StateClosed {
			return nil, huma.Error400BadRequest("state must be open or closed")
		}
	}
	var labels []string
	if input.Body.Labels != nil {
		normalized, err := normalizedIssueNames(
			*input.Body.Labels,
			issue.MaximumLabels,
			issue.MaximumLabelBytes,
			"label",
		)
		if err != nil {
			return nil, err
		}
		labels = normalized
	}
	var assignees []string
	if input.Body.Assignees != nil {
		normalized, err := normalizedIssueNames(
			*input.Body.Assignees,
			issue.MaximumAssignees,
			issue.MaximumAssigneeBytes,
			"assignee",
		)
		if err != nil {
			return nil, err
		}
		assignees = normalized
	}

	parsed, principal, release, err := a.openLockedIssueRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
	}
	defer release()
	role := principal.Role

	updated, err := a.issueStore().Update(parsed, input.ID, func(record *issue.Issue) error {
		if record.Author != principal.Name && !role.Allows(control.RoleMaintainer) {
			return huma.Error403Forbidden(
				"only the issue author or a maintainer can change this issue",
			)
		}
		now := time.Now().UTC()
		if input.Body.Title != nil {
			record.Title = title
		}
		if input.Body.Description != nil {
			record.Description = description
		}
		if input.Body.Labels != nil {
			record.Labels = labels
		}
		if input.Body.Assignees != nil {
			record.Assignees = assignees
		}
		if input.Body.State != nil && record.State != state {
			record.State = state
			if state == issue.StateClosed {
				record.ClosedBy = principal.Name
				record.ClosedAt = &now
			} else {
				record.ClosedBy = ""
				record.ClosedAt = nil
			}
		}
		return nil
	})
	if err != nil {
		return nil, issueMutationError("could not update issue", err)
	}
	return &issueOutput{Body: buildIssueView(updated, principal.Name, role)}, nil
}

func (a API) createIssueComment(
	ctx context.Context,
	input *createIssueCommentInput,
) (*issueOutput, error) {
	body := strings.TrimSpace(input.Body.Body)
	if body == "" {
		return nil, huma.Error400BadRequest("comment body is required")
	}
	if len(body) > issue.MaximumBodyBytes {
		return nil, huma.Error400BadRequest("comment body is too long")
	}
	parsed, principal, release, err := a.openLockedIssueRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleRead,
	)
	if err != nil {
		return nil, err
	}
	defer release()

	updated, err := a.issueStore().Update(parsed, input.ID, func(record *issue.Issue) error {
		var nextID uint64
		for _, comment := range record.Comments {
			if comment.ID > nextID {
				nextID = comment.ID
			}
		}
		record.Comments = append(record.Comments, issue.Comment{
			ID:        nextID + 1,
			Author:    principal.Name,
			Body:      body,
			CreatedAt: time.Now().UTC(),
		})
		return nil
	})
	if err != nil {
		return nil, issueMutationError("could not add the issue comment", err)
	}
	return &issueOutput{
		Body: buildIssueView(updated, principal.Name, principal.Role),
	}, nil
}

func (a API) createIssueBranch(
	ctx context.Context,
	input *createIssueBranchInput,
) (*createIssueBranchOutput, error) {
	if input.ID == 0 {
		return nil, huma.Error400BadRequest("issue ID must be greater than zero")
	}
	if len(input.Body.Name) > issue.MaximumBranchBytes {
		return nil, huma.Error400BadRequest("issue branch name is too long")
	}
	branchName, err := validatedBranchReference(input.Body.Name)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid issue branch name", err)
	}
	sourceName, err := validatedBranchReference(input.Body.From)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid source branch name", err)
	}
	prefix := issueBranchPrefix(input.ID)
	if input.Body.Name != prefix && !strings.HasPrefix(input.Body.Name, prefix+"-") {
		return nil, huma.Error400BadRequest("issue branch name must use the issue number prefix")
	}

	repository, parsed, principal, release, err := a.openLockedReviewRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
	}
	defer release()

	record, err := a.issueStore().Get(parsed, input.ID)
	if err != nil {
		return nil, issueMutationError("could not read issue", err)
	}
	if record.Branch != "" {
		return nil, huma.Error409Conflict("issue already has a branch")
	}

	commit, err := createRepositoryBranchReference(repository, branchName, sourceName)
	if err != nil {
		return nil, err
	}
	createdAt := time.Now().UTC()
	updated, err := a.issueStore().Update(parsed, input.ID, func(stored *issue.Issue) error {
		if stored.Branch != "" {
			return huma.Error409Conflict("issue already has a branch")
		}
		stored.Branch = input.Body.Name
		stored.BranchCreatedBy = principal.Name
		stored.BranchCreatedAt = &createdAt
		return nil
	})
	if err != nil {
		rollbackErr := repository.Storer.RemoveReference(branchName)
		if rollbackErr != nil && !errors.Is(rollbackErr, plumbing.ErrReferenceNotFound) {
			return nil, huma.Error500InternalServerError(
				"could not save issue branch or roll back its Git reference",
				errors.Join(err, rollbackErr),
			)
		}
		return nil, issueMutationError("could not save issue branch", err)
	}
	a.scheduleBuild(parsed, input.Body.Name, commit)

	output := &createIssueBranchOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.IssueID = updated.ID
	output.Body.Name = updated.Branch
	output.Body.From = input.Body.From
	output.Body.Commit = commit.String()
	output.Body.CreatedBy = updated.BranchCreatedBy
	output.Body.CreatedAt = *updated.BranchCreatedAt
	return output, nil
}

// openLockedIssueRepository authorizes the principal, confirms the repository
// exists, and holds the repository operation lock for the caller.
func (a API) openLockedIssueRepository(
	ctx context.Context,
	credentials AuthInput,
	value string,
	role control.Role,
) (repopath.Repository, auth.Principal, func(), error) {
	_, parsed, principal, release, err := a.openLockedReviewRepository(
		ctx,
		credentials,
		value,
		role,
	)
	if err != nil {
		return repopath.Repository{}, auth.Principal{}, nil, err
	}
	return parsed, principal, release, nil
}

// issuePrincipal reports the viewer's name and effective role, both of which
// are empty for anonymous readers of a public repository.
func (a API) issuePrincipal(
	ctx context.Context,
	credentials AuthInput,
	repository repopath.Repository,
) (string, control.Role) {
	principal, err := a.authorizeRepository(ctx, credentials, repository, control.RoleRead)
	if err != nil {
		return "", ""
	}
	return principal.Name, principal.Role
}

func buildIssueView(record issue.Issue, viewer string, role control.Role) issueView {
	view := issueView{
		ID:              record.ID,
		Repository:      record.Repository,
		Title:           record.Title,
		Description:     record.Description,
		SuggestedBranch: suggestedIssueBranch(record),
		Author:          record.Author,
		State:           record.State,
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
		Labels:          record.Labels,
		Assignees:       record.Assignees,
		Comments:        issueCommentViews(record.Comments),
		Branch:          record.Branch,
		BranchCreatedBy: record.BranchCreatedBy,
		BranchCreatedAt: record.BranchCreatedAt,
		ClosedBy:        record.ClosedBy,
		ClosedAt:        record.ClosedAt,
	}
	if view.Labels == nil {
		view.Labels = []string{}
	}
	if view.Assignees == nil {
		view.Assignees = []string{}
	}
	view.CanComment = role.Allows(control.RoleRead)
	view.CanUpdate = role.Allows(control.RoleMaintainer) ||
		(role.Allows(control.RoleDeveloper) && viewer != "" && viewer == record.Author)
	return view
}

const maximumIssueBranchSlugBytes = 80

// suggestedIssueBranch turns the issue description into a Git-safe branch
// name. The issue number keeps otherwise identical descriptions distinct, and
// the title supplies a useful fallback for issues without a description.
func suggestedIssueBranch(record issue.Issue) string {
	prefix := issueBranchPrefix(record.ID)
	source := strings.TrimSpace(record.Description)
	if source == "" {
		source = strings.TrimSpace(record.Title)
	}

	var slug strings.Builder
	separator := false
	for _, character := range norm.NFC.String(strings.ToLower(source)) {
		isLetter := unicode.IsLetter(character)
		isNumber := unicode.IsNumber(character)
		isAttachedMark := unicode.IsMark(character) && slug.Len() > 0 && !separator
		if !isLetter && !isNumber && !isAttachedMark {
			separator = slug.Len() > 0
			continue
		}
		characterBytes := utf8.RuneLen(character)
		if separator {
			if slug.Len()+1+characterBytes > maximumIssueBranchSlugBytes {
				break
			}
			slug.WriteByte('-')
			separator = false
		}
		if slug.Len()+characterBytes > maximumIssueBranchSlugBytes {
			break
		}
		slug.WriteRune(character)
	}
	if slug.Len() == 0 {
		return prefix
	}
	return prefix + "-" + slug.String()
}

func issueBranchPrefix(id uint64) string {
	return "issue-" + strconv.FormatUint(id, 10)
}

func issueCommentViews(comments []issue.Comment) []issueCommentView {
	views := make([]issueCommentView, 0, len(comments))
	for _, comment := range comments {
		views = append(views, issueCommentView{
			ID:        comment.ID,
			Author:    comment.Author,
			Body:      comment.Body,
			CreatedAt: comment.CreatedAt,
		})
	}
	return views
}

func normalizedIssueNames(
	values []string,
	maximumCount int,
	maximumBytes int,
	kind string,
) ([]string, error) {
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			return nil, huma.Error400BadRequest("an issue " + kind + " cannot be empty")
		}
		if len(trimmed) > maximumBytes {
			return nil, huma.Error400BadRequest("an issue " + kind + " is too long")
		}
		if _, duplicate := seen[trimmed]; duplicate {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	if len(normalized) > maximumCount {
		return nil, huma.Error400BadRequest("too many issue " + kind + "s")
	}
	return normalized, nil
}

func issueMutationError(message string, err error) error {
	if errors.Is(err, issue.ErrNotFound) {
		return huma.Error404NotFound("issue not found")
	}
	var statusErr huma.StatusError
	if errors.As(err, &statusErr) {
		return err
	}
	return huma.Error500InternalServerError(message, err)
}
