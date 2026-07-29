package httpapi

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	maxMergeRequestTitleBytes   = 500
	maxMergeRequestMessageBytes = 64 * 1024
)

var (
	activeMergeClaims sync.Map      //nolint:gochecknoglobals // Tracks claims owned by this process.
	mergeProcessID    = rand.Text() //nolint:gochecknoglobals // Identifies persisted claims after a restart.
)

type mergeRequestsInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	State      string `query:"state" enum:"open,closed,merged,all" default:"open" doc:"Request state to return"`
}

type MergeRequestInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	ID         uint64 `path:"id" minimum:"1" doc:"Merge request number"`
}

type mergeRequestInput = MergeRequestInput

type createMergeRequestBody struct {
	Title       string `json:"title" minLength:"1" maxLength:"500"`
	Description string `json:"description,omitempty" maxLength:"65536"`
	Target      string `json:"target" minLength:"1" doc:"Branch receiving the merge"`
	Source      string `json:"source" minLength:"1" doc:"Branch proposed for merging"`
}

type createMergeRequestInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Body       createMergeRequestBody
}

type updateMergeRequestBody struct {
	State review.State `json:"state" enum:"open,closed"`
}

type updateMergeRequestInput struct {
	MergeRequestInput
	Body updateMergeRequestBody
}

type createReviewThreadBody struct {
	Body string `json:"body" minLength:"1" maxLength:"65536"`
}

type createReviewThreadInput struct {
	MergeRequestInput
	Body createReviewThreadBody
}

type ReviewThreadInput struct {
	MergeRequestInput
	ThreadID uint64 `path:"threadId" minimum:"1" doc:"Discussion thread number"`
}

type reviewThreadInput = ReviewThreadInput

type addReviewCommentInput struct {
	ReviewThreadInput
	Body createReviewThreadBody
}

type updateReviewThreadBody struct {
	Resolved bool `json:"resolved"`
}

type updateReviewThreadInput struct {
	ReviewThreadInput
	Body updateReviewThreadBody
}

type approveMergeRequestBody struct {
	ExpectedHeadCommit string `json:"expectedHeadCommit" minLength:"40" maxLength:"40" doc:"Reviewed source branch commit"`
}

type approveMergeRequestInput struct {
	MergeRequestInput
	Body approveMergeRequestBody
}

type mergeRequestApprovalView struct {
	Author     string    `json:"author"`
	HeadCommit string    `json:"headCommit"`
	CreatedAt  time.Time `json:"createdAt"`
	Current    bool      `json:"current"`
}

type mergeRequestView struct {
	ID                uint64                     `json:"id"`
	Repository        string                     `json:"repository"`
	Title             string                     `json:"title"`
	Description       string                     `json:"description"`
	Target            string                     `json:"target"`
	Source            string                     `json:"source"`
	Author            string                     `json:"author"`
	State             review.State               `json:"state"`
	CreatedAt         time.Time                  `json:"createdAt"`
	UpdatedAt         time.Time                  `json:"updatedAt"`
	RequiredApprovals int                        `json:"requiredApprovals"`
	CurrentApprovals  int                        `json:"currentApprovals"`
	StaleApprovals    int                        `json:"staleApprovals"`
	UnresolvedThreads int                        `json:"unresolvedThreads"`
	HeadCommit        string                     `json:"headCommit"`
	TargetCommit      string                     `json:"targetCommit"`
	Ahead             int                        `json:"ahead"`
	Behind            int                        `json:"behind"`
	Mergeable         bool                       `json:"mergeable"`
	Conflicts         []string                   `json:"conflicts"`
	Files             []repositoryComparisonFile `json:"files"`
	Approvals         []mergeRequestApprovalView `json:"approvals"`
	Threads           []review.Thread            `json:"threads"`
	CanApprove        bool                       `json:"canApprove"`
	ViewerApproved    bool                       `json:"viewerApproved"`
	CanMerge          bool                       `json:"canMerge"`
	CanUpdate         bool                       `json:"canUpdate"`
	MergeInProgress   bool                       `json:"mergeInProgress"`
	MergedCommit      string                     `json:"mergedCommit,omitempty"`
	MergedStrategy    string                     `json:"mergedStrategy,omitempty"`
	MergedBy          string                     `json:"mergedBy,omitempty"`
	MergedAt          *time.Time                 `json:"mergedAt,omitempty"`
	ClosedBy          string                     `json:"closedBy,omitempty"`
	ClosedAt          *time.Time                 `json:"closedAt,omitempty"`
}

type mergeRequestsOutput struct {
	Body struct {
		Repository    string             `json:"repository"`
		MergeRequests []mergeRequestView `json:"mergeRequests"`
	}
}

type mergeRequestOutput struct {
	Body mergeRequestView
}

type mergeRequestComparison struct {
	TargetCommit string
	HeadCommit   string
	Ahead        int
	Behind       int
	Mergeable    bool
	Conflicts    []string
	Files        []repositoryComparisonFile
}

func registerReviewAPI(api huma.API, service API) {
	huma.Register(api, protected(huma.Operation{
		OperationID: "list-merge-requests",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/merge-requests",
		Summary:     "List persisted merge requests",
		Tags:        []string{"Merge requests"},
	}), service.listMergeRequests)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-merge-request",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/merge-requests",
		Summary:       "Create a merge request",
		Tags:          []string{"Merge requests"},
		DefaultStatus: http.StatusCreated,
	}), service.createMergeRequest)

	huma.Register(api, protected(huma.Operation{
		OperationID: "get-merge-request",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/merge-requests/{id}",
		Summary:     "Get a merge request with its current comparison and review",
		Tags:        []string{"Merge requests"},
	}), service.getMergeRequest)

	huma.Register(api, protected(huma.Operation{
		OperationID: "update-merge-request",
		Method:      http.MethodPatch,
		Path:        "/api/repositories/{repository}/merge-requests/{id}",
		Summary:     "Close or reopen a merge request",
		Tags:        []string{"Merge requests"},
	}), service.updateMergeRequest)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-merge-request-thread",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/merge-requests/{id}/threads",
		Summary:       "Start a merge request discussion thread",
		Tags:          []string{"Merge requests"},
		DefaultStatus: http.StatusCreated,
	}), service.createReviewThread)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "add-merge-request-comment",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/merge-requests/{id}/threads/{threadId}/comments",
		Summary:       "Reply to a merge request discussion thread",
		Tags:          []string{"Merge requests"},
		DefaultStatus: http.StatusCreated,
	}), service.addReviewComment)

	huma.Register(api, protected(huma.Operation{
		OperationID: "update-merge-request-thread",
		Method:      http.MethodPatch,
		Path:        "/api/repositories/{repository}/merge-requests/{id}/threads/{threadId}",
		Summary:     "Resolve or reopen a merge request discussion thread",
		Tags:        []string{"Merge requests"},
	}), service.updateReviewThread)

	huma.Register(api, protected(huma.Operation{
		OperationID: "approve-merge-request",
		Method:      http.MethodPost,
		Path:        "/api/repositories/{repository}/merge-requests/{id}/approvals",
		Summary:     "Approve the current source commit and merge when ready",
		Tags:        []string{"Merge requests"},
	}), service.approveMergeRequest)

	huma.Register(api, protected(huma.Operation{
		OperationID: "merge-approved-request",
		Method:      http.MethodPost,
		Path:        "/api/repositories/{repository}/merge-requests/{id}/merge",
		Summary:     "Retry merging an approved request",
		Tags:        []string{"Merge requests"},
	}), service.mergeApprovedRequest)
}

func (a API) reviewStore() *review.Store {
	if a.Reviews != nil {
		return a.Reviews
	}
	return review.NewStore(a.Storage.Root)
}

func (a API) listMergeRequests(
	ctx context.Context,
	input *mergeRequestsInput,
) (*mergeRequestsOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
	if err != nil {
		return nil, err
	}
	requests, err := a.reviewStore().List(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not list merge requests", err)
	}
	state := input.State
	if state == "" {
		state = string(review.StateOpen)
	}
	output := &mergeRequestsOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.MergeRequests = make([]mergeRequestView, 0, len(requests))
	for _, request := range requests {
		request, err = a.recoverInterruptedMergeClaim(parsed, request)
		if err != nil {
			return nil, err
		}
		if state != "all" && string(request.State) != state {
			continue
		}
		view, viewErr := a.buildMergeRequestView(
			ctx,
			input.AuthInput,
			repository,
			parsed,
			request,
			false,
		)
		if viewErr != nil {
			return nil, viewErr
		}
		output.Body.MergeRequests = append(output.Body.MergeRequests, view)
	}
	return output, nil
}

func (a API) createMergeRequest(
	ctx context.Context,
	input *createMergeRequestInput,
) (*mergeRequestOutput, error) {
	title := strings.TrimSpace(input.Body.Title)
	description := strings.TrimSpace(input.Body.Description)
	if title == "" {
		return nil, huma.Error400BadRequest("title is required")
	}
	if len(title) > maxMergeRequestTitleBytes ||
		len(description) > maxMergeRequestMessageBytes {
		return nil, huma.Error400BadRequest("merge request text is too long")
	}
	repository, parsed, principal, releaseOperationLock, err := a.openLockedReviewRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
	}
	defer releaseOperationLock()
	targetName, targetRef, targetCommit, err := resolveBranch(repository, input.Body.Target)
	if err != nil {
		return nil, huma.Error404NotFound("target branch not found", err)
	}
	sourceName, sourceRef, sourceCommit, err := resolveBranch(repository, input.Body.Source)
	if err != nil {
		return nil, huma.Error404NotFound("source branch not found", err)
	}
	if targetName == sourceName {
		return nil, huma.Error400BadRequest("source and target branches must be different")
	}
	ahead, _, err := commitDifference(repository, targetCommit, sourceCommit)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not compare commit history", err)
	}
	if ahead == 0 {
		return nil, huma.Error409Conflict("source branch has no commits to merge")
	}
	existing, err := a.reviewStore().List(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not inspect merge requests", err)
	}
	for _, candidate := range existing {
		if candidate.State == review.StateOpen &&
			candidate.Target == targetName &&
			candidate.Source == sourceName {
			return nil, huma.Error409Conflict(fmt.Sprintf(
				"merge request !%d already reviews %s into %s",
				candidate.ID,
				sourceName,
				targetName,
			))
		}
	}
	now := time.Now().UTC()
	request := review.MergeRequest{
		Repository:        parsed.Full(),
		Title:             title,
		Description:       description,
		Target:            targetName,
		Source:            sourceName,
		Author:            principal.Name,
		State:             review.StateOpen,
		CreatedAt:         now,
		UpdatedAt:         now,
		BaseCommit:        targetRef.Hash().String(),
		HeadCommit:        sourceRef.Hash().String(),
		RequiredApprovals: 1,
		Approvals:         []review.Approval{},
		Threads:           []review.Thread{},
	}
	if err = a.reviewStore().Create(parsed, &request); err != nil {
		if errors.Is(err, review.ErrDuplicate) {
			return nil, huma.Error409Conflict(err.Error())
		}
		return nil, huma.Error500InternalServerError("could not create merge request", err)
	}
	return a.mergeRequestResponse(
		ctx,
		input.AuthInput,
		repository,
		parsed,
		request,
		true,
	)
}

func (a API) getMergeRequest(
	ctx context.Context,
	input *mergeRequestInput,
) (*mergeRequestOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
	if err != nil {
		return nil, err
	}
	request, err := a.getStoredMergeRequest(parsed, input.ID)
	if err != nil {
		return nil, err
	}
	request, err = a.recoverInterruptedMergeClaim(parsed, request)
	if err != nil {
		return nil, err
	}
	return a.mergeRequestResponse(
		ctx,
		input.AuthInput,
		repository,
		parsed,
		request,
		true,
	)
}

func (a API) updateMergeRequest(
	ctx context.Context,
	input *updateMergeRequestInput,
) (*mergeRequestOutput, error) {
	if input.Body.State != review.StateOpen && input.Body.State != review.StateClosed {
		return nil, huma.Error400BadRequest("state must be open or closed")
	}
	repository, parsed, principal, releaseOperationLock, err := a.openLockedReviewRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
	}
	defer releaseOperationLock()
	if _, err = a.prepareReviewMutationWithOperationLock(parsed, input.ID); err != nil {
		return nil, err
	}
	var updated review.MergeRequest
	updated, err = a.reviewStore().Update(parsed, input.ID, func(request *review.MergeRequest) error {
		if request.MergeInProgress {
			return huma.Error409Conflict("merge request is currently being merged")
		}
		if request.State == review.StateMerged {
			return huma.Error409Conflict("a merged request cannot be reopened or closed")
		}
		now := time.Now().UTC()
		request.State = input.Body.State
		request.UpdatedAt = now
		if request.State == review.StateClosed {
			request.ClosedBy = principal.Name
			request.ClosedAt = &now
		} else {
			request.ClosedBy = ""
			request.ClosedAt = nil
		}
		return nil
	})
	if err != nil {
		return nil, reviewMutationError("could not update merge request", err)
	}
	return a.mergeRequestResponse(
		ctx,
		input.AuthInput,
		repository,
		parsed,
		updated,
		true,
	)
}

func (a API) createReviewThread(
	ctx context.Context,
	input *createReviewThreadInput,
) (*mergeRequestOutput, error) {
	body, err := validatedReviewBody(input.Body.Body)
	if err != nil {
		return nil, err
	}
	repository, parsed, principal, releaseOperationLock, err := a.openLockedReviewRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleRead,
	)
	if err != nil {
		return nil, err
	}
	defer releaseOperationLock()
	if _, err = a.prepareReviewMutationWithOperationLock(parsed, input.ID); err != nil {
		return nil, err
	}
	var updated review.MergeRequest
	updated, err = a.reviewStore().Update(parsed, input.ID, func(request *review.MergeRequest) error {
		if request.MergeInProgress {
			return huma.Error409Conflict("merge request is currently being merged")
		}
		if request.State != review.StateOpen {
			return huma.Error409Conflict("only open merge requests can be discussed")
		}
		var nextID uint64 = 1
		for _, thread := range request.Threads {
			if thread.ID >= nextID {
				nextID = thread.ID + 1
			}
		}
		now := time.Now().UTC()
		request.Threads = append(request.Threads, review.Thread{
			ID:        nextID,
			CreatedAt: now,
			Comments: []review.Comment{{
				ID:        1,
				Author:    principal.Name,
				Body:      body,
				CreatedAt: now,
			}},
		})
		request.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, reviewMutationError("could not create discussion thread", err)
	}
	return a.mergeRequestResponse(
		ctx,
		input.AuthInput,
		repository,
		parsed,
		updated,
		true,
	)
}

func (a API) addReviewComment(
	ctx context.Context,
	input *addReviewCommentInput,
) (*mergeRequestOutput, error) {
	body, err := validatedReviewBody(input.Body.Body)
	if err != nil {
		return nil, err
	}
	repository, parsed, principal, releaseOperationLock, err := a.openLockedReviewRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleRead,
	)
	if err != nil {
		return nil, err
	}
	defer releaseOperationLock()
	if _, err = a.prepareReviewMutationWithOperationLock(parsed, input.ID); err != nil {
		return nil, err
	}
	var updated review.MergeRequest
	updated, err = a.reviewStore().Update(parsed, input.ID, func(request *review.MergeRequest) error {
		if request.MergeInProgress {
			return huma.Error409Conflict("merge request is currently being merged")
		}
		if request.State != review.StateOpen {
			return huma.Error409Conflict("only open merge requests can be discussed")
		}
		for index := range request.Threads {
			if request.Threads[index].ID != input.ThreadID {
				continue
			}
			var nextID uint64 = 1
			for _, comment := range request.Threads[index].Comments {
				if comment.ID >= nextID {
					nextID = comment.ID + 1
				}
			}
			now := time.Now().UTC()
			request.Threads[index].Comments = append(
				request.Threads[index].Comments,
				review.Comment{
					ID:        nextID,
					Author:    principal.Name,
					Body:      body,
					CreatedAt: now,
				},
			)
			request.UpdatedAt = now
			return nil
		}
		return huma.Error404NotFound("discussion thread not found")
	})
	if err != nil {
		return nil, reviewMutationError("could not add discussion comment", err)
	}
	return a.mergeRequestResponse(
		ctx,
		input.AuthInput,
		repository,
		parsed,
		updated,
		true,
	)
}

func (a API) updateReviewThread(
	ctx context.Context,
	input *updateReviewThreadInput,
) (*mergeRequestOutput, error) {
	repository, parsed, principal, releaseOperationLock, err := a.openLockedReviewRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleRead,
	)
	if err != nil {
		return nil, err
	}
	defer releaseOperationLock()
	if _, err = a.prepareReviewMutationWithOperationLock(parsed, input.ID); err != nil {
		return nil, err
	}
	var updated review.MergeRequest
	updated, err = a.reviewStore().Update(parsed, input.ID, func(request *review.MergeRequest) error {
		if request.MergeInProgress {
			return huma.Error409Conflict("merge request is currently being merged")
		}
		if request.State != review.StateOpen {
			return huma.Error409Conflict("only open merge request threads can be updated")
		}
		for index := range request.Threads {
			thread := &request.Threads[index]
			if thread.ID != input.ThreadID {
				continue
			}
			threadAuthor := ""
			if len(thread.Comments) > 0 {
				threadAuthor = thread.Comments[0].Author
			}
			if principal.Name != threadAuthor &&
				principal.Name != request.Author &&
				!principal.Role.Allows(control.RoleDeveloper) {
				return huma.Error403Forbidden("only the thread author or a developer can update this thread")
			}
			now := time.Now().UTC()
			thread.Resolved = input.Body.Resolved
			if thread.Resolved {
				thread.ResolvedBy = principal.Name
				thread.ResolvedAt = &now
			} else {
				thread.ResolvedBy = ""
				thread.ResolvedAt = nil
			}
			request.UpdatedAt = now
			return nil
		}
		return huma.Error404NotFound("discussion thread not found")
	})
	if err != nil {
		return nil, reviewMutationError("could not update discussion thread", err)
	}
	return a.mergeRequestResponse(
		ctx,
		input.AuthInput,
		repository,
		parsed,
		updated,
		true,
	)
}

func (a API) approveMergeRequest(
	ctx context.Context,
	input *approveMergeRequestInput,
) (*mergeRequestOutput, error) {
	if !plumbing.IsHash(input.Body.ExpectedHeadCommit) {
		return nil, huma.Error400BadRequest("expectedHeadCommit must be a complete commit hash")
	}
	expectedHeadCommit := plumbing.NewHash(input.Body.ExpectedHeadCommit).String()
	repository, parsed, principal, releaseOperationLock, err := a.openLockedReviewRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
	}
	defer releaseOperationLock()
	request, err := a.prepareReviewMutationWithOperationLock(parsed, input.ID)
	if err != nil {
		return nil, err
	}
	if request.State != review.StateOpen {
		return nil, huma.Error409Conflict("only open merge requests can be approved")
	}
	if request.MergeInProgress {
		return nil, huma.Error409Conflict("merge request is currently being merged")
	}
	selfApproval := principal.Name == request.Author &&
		principal.Role.Allows(control.RoleMaintainer)
	if principal.Name == request.Author && !selfApproval {
		return nil, huma.Error403Forbidden("merge request authors cannot approve their own changes")
	}
	_, currentSource, _, err := resolveBranch(repository, request.Source)
	if err != nil {
		return nil, huma.Error409Conflict("source branch no longer exists", err)
	}
	if currentSource.Hash().String() != expectedHeadCommit {
		return nil, huma.Error409Conflict("source branch changed since it was reviewed")
	}
	_, currentTarget, _, err := resolveBranch(repository, request.Target)
	if err != nil {
		return nil, huma.Error409Conflict("target branch no longer exists", err)
	}
	request, err = a.reviewStore().Update(parsed, input.ID, func(stored *review.MergeRequest) error {
		if stored.MergeInProgress {
			return huma.Error409Conflict("merge request is currently being merged")
		}
		if stored.State != review.StateOpen {
			return huma.Error409Conflict("only open merge requests can be approved")
		}
		now := time.Now().UTC()
		replaced := false
		for index := range stored.Approvals {
			if stored.Approvals[index].Author == principal.Name {
				stored.Approvals[index] = review.Approval{
					Author:       principal.Name,
					HeadCommit:   expectedHeadCommit,
					CreatedAt:    now,
					SelfApproval: selfApproval,
				}
				replaced = true
				break
			}
		}
		if !replaced {
			stored.Approvals = append(stored.Approvals, review.Approval{
				Author:       principal.Name,
				HeadCommit:   expectedHeadCommit,
				CreatedAt:    now,
				SelfApproval: selfApproval,
			})
		}
		stored.BaseCommit = currentTarget.Hash().String()
		stored.HeadCommit = expectedHeadCommit
		stored.UpdatedAt = now
		return nil
	})
	if err != nil {
		return nil, reviewMutationError("could not approve merge request", err)
	}

	view, err := a.buildMergeRequestView(
		ctx,
		input.AuthInput,
		repository,
		parsed,
		request,
		true,
	)
	if err != nil {
		return nil, err
	}
	if !view.CanMerge {
		return &mergeRequestOutput{Body: view}, nil
	}
	releaseOperationLock()
	merged, err := a.mergeStoredRequest(
		ctx,
		input.AuthInput,
		parsed,
		request.ID,
		request.CreatedAt,
		expectedHeadCommit,
	)
	if err != nil {
		return nil, err
	}
	return &mergeRequestOutput{Body: merged}, nil
}

func (a API) mergeApprovedRequest(
	ctx context.Context,
	input *approveMergeRequestInput,
) (*mergeRequestOutput, error) {
	if !plumbing.IsHash(input.Body.ExpectedHeadCommit) {
		return nil, huma.Error400BadRequest("expectedHeadCommit must be a complete commit hash")
	}
	expectedHeadCommit := plumbing.NewHash(input.Body.ExpectedHeadCommit).String()
	parsed, err := parseRepositoryPath(input.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	merged, err := a.mergeStoredRequest(
		ctx,
		input.AuthInput,
		parsed,
		input.ID,
		time.Time{},
		expectedHeadCommit,
	)
	if err != nil {
		return nil, err
	}
	return &mergeRequestOutput{Body: merged}, nil
}

func (a API) mergeStoredRequest(
	ctx context.Context,
	credentials AuthInput,
	parsed repopath.Repository,
	id uint64,
	expectedCreatedAt time.Time,
	expectedHeadCommit string,
) (mergeRequestView, error) {
	releaseOperationLock, err := a.acquireRepositoryOperationLocks(parsed)
	if err != nil {
		return mergeRequestView{}, huma.Error500InternalServerError(
			"could not lock repository operations",
			err,
		)
	}
	defer func() {
		_ = releaseOperationLock()
	}()
	releaseRepositoryLock, err := a.reviewStore().AcquireMergeLock(parsed)
	if err != nil {
		return mergeRequestView{}, huma.Error500InternalServerError(
			"could not lock merge request repository",
			err,
		)
	}
	defer func() {
		_ = releaseRepositoryLock()
	}()
	principal, err := a.authorizeRepository(
		ctx,
		credentials,
		parsed,
		control.RoleDeveloper,
	)
	if err != nil {
		return mergeRequestView{}, err
	}
	repositoryPath, err := a.Storage.GitPath(parsed)
	if err != nil {
		return mergeRequestView{}, huma.Error500InternalServerError(
			"could not resolve merge request repository",
			err,
		)
	}
	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		return mergeRequestView{}, huma.Error404NotFound("repository not found", err)
	}
	request, err := a.getStoredMergeRequest(parsed, id)
	if err != nil {
		return mergeRequestView{}, err
	}
	if !expectedCreatedAt.IsZero() && !request.CreatedAt.Equal(expectedCreatedAt) {
		return mergeRequestView{}, huma.Error409Conflict("merge request changed before it could be merged")
	}
	if request.MergeInProgress {
		request, err = a.reconcileMergeClaim(repository, parsed, request, false)
		if err != nil {
			return mergeRequestView{}, err
		}
	}
	if request.State != review.StateOpen {
		return mergeRequestView{}, huma.Error409Conflict("only open merge requests can be merged")
	}
	view, err := a.buildMergeRequestView(
		ctx,
		credentials,
		repository,
		parsed,
		request,
		true,
	)
	if err != nil {
		return mergeRequestView{}, err
	}
	if view.HeadCommit != expectedHeadCommit {
		return mergeRequestView{}, huma.Error409Conflict("source branch changed since it was reviewed")
	}
	if view.CurrentApprovals < request.RequiredApprovals {
		return mergeRequestView{}, huma.Error409Conflict("the current source commit is not approved")
	}
	if view.UnresolvedThreads > 0 {
		return mergeRequestView{}, huma.Error409Conflict("resolve all discussion threads before merging")
	}
	if !view.Mergeable {
		return mergeRequestView{}, huma.Error409Conflict("merge request has conflicts")
	}
	claimID := rand.Text()
	activeMergeClaims.Store(claimID, struct{}{})
	claimStarted := time.Now().UTC()
	request, err = a.reviewStore().Update(
		parsed,
		request.ID,
		func(stored *review.MergeRequest) error {
			if stored.State != review.StateOpen {
				return huma.Error409Conflict("only open merge requests can be merged")
			}
			if stored.MergeInProgress {
				return huma.Error409Conflict("merge request is currently being merged")
			}
			approvedBy := map[string]struct{}{}
			for _, approval := range stored.Approvals {
				if (approval.Author != stored.Author || approval.SelfApproval) &&
					approval.HeadCommit == expectedHeadCommit {
					approvedBy[approval.Author] = struct{}{}
				}
			}
			if len(approvedBy) < stored.RequiredApprovals {
				return huma.Error409Conflict("the current source commit is not approved")
			}
			for _, thread := range stored.Threads {
				if !thread.Resolved {
					return huma.Error409Conflict("resolve all discussion threads before merging")
				}
			}
			stored.MergeInProgress = true
			stored.MergeClaimID = claimID
			stored.MergeOwnerID = mergeProcessID
			stored.MergeHeadCommit = expectedHeadCommit
			stored.MergeStartedBy = principal.Name
			stored.MergeStartedAt = &claimStarted
			return nil
		},
	)
	if err != nil {
		activeMergeClaims.Delete(claimID)
		return mergeRequestView{}, reviewMutationError("could not claim merge request", err)
	}
	defer activeMergeClaims.Delete(claimID)
	result, err := a.mergeRepositoryBranchesAtSource(
		repository,
		parsed,
		request.Target,
		request.Source,
		principal.Name,
		fmt.Sprintf("Merge request !%d: %s", request.ID, request.Title),
		expectedHeadCommit,
		func(plan repositoryMergeResult) error {
			_, planErr := a.reviewStore().Update(
				parsed,
				request.ID,
				func(stored *review.MergeRequest) error {
					if stored.State != review.StateOpen ||
						!stored.MergeInProgress ||
						stored.MergeClaimID != claimID {
						return huma.Error409Conflict(
							"merge request state changed while preparing the merge",
						)
					}
					stored.MergeTargetCommit = plan.PreviousTarget
					stored.MergeResultCommit = plan.Commit
					stored.MergeResultStrategy = plan.Strategy
					return nil
				},
			)
			if planErr != nil {
				return reviewMutationError("could not persist merge plan", planErr)
			}
			return nil
		},
	)
	if err != nil {
		activeMergeClaims.Delete(claimID)
		claimed, readErr := a.getStoredMergeRequest(parsed, request.ID)
		if readErr == nil &&
			claimed.MergeInProgress &&
			claimed.MergeClaimID == claimID {
			var notApplied *mergeNotAppliedError
			reconciled, reconcileErr := a.reconcileMergeClaim(
				repository,
				parsed,
				claimed,
				errors.As(err, &notApplied),
			)
			if reconcileErr != nil {
				return mergeRequestView{}, reconcileErr
			}
			if reconciled.State == review.StateMerged {
				return a.buildMergeRequestView(
					ctx,
					credentials,
					repository,
					parsed,
					reconciled,
					true,
				)
			}
		}
		return mergeRequestView{}, err
	}
	now := time.Now().UTC()
	request, err = a.reviewStore().Update(parsed, request.ID, func(stored *review.MergeRequest) error {
		if stored.State != review.StateOpen ||
			!stored.MergeInProgress ||
			stored.MergeClaimID != claimID ||
			stored.MergeHeadCommit != expectedHeadCommit ||
			stored.MergeResultCommit != result.Commit ||
			stored.MergeResultStrategy != result.Strategy {
			return huma.Error409Conflict("merge request state changed while it was being merged")
		}
		stored.State = review.StateMerged
		stored.HeadCommit = expectedHeadCommit
		stored.MergedCommit = result.Commit
		stored.MergedStrategy = result.Strategy
		stored.MergedBy = principal.Name
		stored.MergedAt = &now
		stored.UpdatedAt = now
		clearMergeClaim(stored)
		return nil
	})
	if err != nil {
		return mergeRequestView{}, reviewMutationError("merge succeeded but its review state could not be saved", err)
	}
	return a.buildMergeRequestView(
		ctx,
		credentials,
		repository,
		parsed,
		request,
		true,
	)
}

func (a API) getStoredMergeRequest(
	parsed repopath.Repository,
	id uint64,
) (review.MergeRequest, error) {
	request, err := a.reviewStore().Get(parsed, id)
	if errors.Is(err, review.ErrNotFound) {
		return review.MergeRequest{}, huma.Error404NotFound("merge request not found")
	}
	if err != nil {
		return review.MergeRequest{}, huma.Error500InternalServerError("could not read merge request", err)
	}
	return request, nil
}

func clearMergeClaim(request *review.MergeRequest) {
	request.MergeInProgress = false
	request.MergeClaimID = ""
	request.MergeOwnerID = ""
	request.MergeHeadCommit = ""
	request.MergeTargetCommit = ""
	request.MergeResultCommit = ""
	request.MergeResultStrategy = ""
	request.MergeStartedBy = ""
	request.MergeStartedAt = nil
}

func (a API) releaseMergeClaim(
	parsed repopath.Repository,
	id uint64,
	claimID string,
) {
	_, _ = a.reviewStore().Update(parsed, id, func(request *review.MergeRequest) error {
		if request.State == review.StateOpen &&
			request.MergeInProgress &&
			request.MergeClaimID == claimID {
			clearMergeClaim(request)
		}
		return nil
	})
}

func (a API) prepareReviewMutationWithOperationLock(
	parsed repopath.Repository,
	id uint64,
) (review.MergeRequest, error) {
	request, err := a.getStoredMergeRequest(parsed, id)
	if err != nil {
		return review.MergeRequest{}, err
	}
	return a.recoverInterruptedMergeClaimWithOperationLock(parsed, request)
}

func (a API) recoverInterruptedMergeClaim(
	parsed repopath.Repository,
	request review.MergeRequest,
) (review.MergeRequest, error) {
	if !request.MergeInProgress {
		return request, nil
	}
	if request.MergeOwnerID == mergeProcessID {
		if _, active := activeMergeClaims.Load(request.MergeClaimID); active {
			return request, nil
		}
	}

	releaseOperationLock, err := a.acquireRepositoryOperationLocks(parsed)
	if err != nil {
		return review.MergeRequest{}, huma.Error500InternalServerError(
			"could not lock repository operations",
			err,
		)
	}
	defer func() {
		_ = releaseOperationLock()
	}()
	return a.recoverInterruptedMergeClaimWithOperationLock(parsed, request)
}

func (a API) recoverInterruptedMergeClaimWithOperationLock(
	parsed repopath.Repository,
	request review.MergeRequest,
) (review.MergeRequest, error) {
	if !request.MergeInProgress {
		return request, nil
	}
	if request.MergeOwnerID == mergeProcessID {
		if _, active := activeMergeClaims.Load(request.MergeClaimID); active {
			return request, nil
		}
	}

	releaseRepositoryLock, err := a.reviewStore().AcquireMergeLock(parsed)
	if err != nil {
		return review.MergeRequest{}, huma.Error500InternalServerError(
			"could not lock merge request repository",
			err,
		)
	}
	defer func() {
		_ = releaseRepositoryLock()
	}()
	repositoryPath, err := a.Storage.GitPath(parsed)
	if err != nil {
		return review.MergeRequest{}, huma.Error500InternalServerError(
			"could not resolve merge request repository",
			err,
		)
	}
	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		return review.MergeRequest{}, huma.Error404NotFound("repository not found", err)
	}
	request, err = a.getStoredMergeRequest(parsed, request.ID)
	if err != nil || !request.MergeInProgress {
		return request, err
	}
	if request.MergeOwnerID == mergeProcessID {
		if _, active := activeMergeClaims.Load(request.MergeClaimID); active {
			return request, nil
		}
	}
	return a.reconcileMergeClaim(repository, parsed, request, false)
}

func (a API) reconcileMergeClaim(
	repository *git.Repository,
	parsed repopath.Repository,
	request review.MergeRequest,
	knownHelperFailure bool,
) (review.MergeRequest, error) {
	claimID := request.MergeClaimID
	startedBy := request.MergeStartedBy
	merged := false
	canClear := request.MergeResultCommit == ""
	if request.MergeResultCommit != "" {
		_, _, targetCommit, targetErr := resolveBranch(repository, request.Target)
		if targetErr != nil {
			return review.MergeRequest{}, huma.Error500InternalServerError(
				"could not read the target branch while reconciling an interrupted merge",
				targetErr,
			)
		}
		resultCommit, resultErr := repository.CommitObject(
			plumbing.NewHash(request.MergeResultCommit),
		)
		if resultErr != nil {
			return review.MergeRequest{}, huma.Error500InternalServerError(
				"could not read the planned merge result",
				resultErr,
			)
		}
		if targetCommit.Hash.String() == request.MergeResultCommit {
			merged = true
		} else {
			merged, resultErr = resultCommit.IsAncestor(targetCommit)
			if resultErr != nil {
				return review.MergeRequest{}, huma.Error500InternalServerError(
					"could not inspect the interrupted merge result",
					resultErr,
				)
			}
		}
		canClear = targetCommit.Hash.String() == request.MergeTargetCommit ||
			knownHelperFailure
	}
	if !merged && !canClear {
		return review.MergeRequest{}, huma.Error409Conflict(
			"the target changed after an interrupted merge; " +
				"the recovery record was retained because the outcome is ambiguous",
		)
	}

	now := time.Now().UTC()
	updated, err := a.reviewStore().Update(
		parsed,
		request.ID,
		func(stored *review.MergeRequest) error {
			if stored.State != review.StateOpen ||
				!stored.MergeInProgress ||
				stored.MergeClaimID != claimID ||
				stored.MergeResultCommit != request.MergeResultCommit ||
				stored.MergeResultStrategy != request.MergeResultStrategy {
				return nil
			}
			if merged {
				stored.State = review.StateMerged
				stored.HeadCommit = stored.MergeHeadCommit
				stored.MergedCommit = stored.MergeResultCommit
				stored.MergedStrategy = stored.MergeResultStrategy
				stored.MergedBy = startedBy
				stored.MergedAt = &now
			}
			clearMergeClaim(stored)
			return nil
		},
	)
	if errors.Is(err, review.ErrNotFound) {
		return review.MergeRequest{}, huma.Error404NotFound("merge request not found")
	}
	if err != nil {
		return review.MergeRequest{}, huma.Error500InternalServerError(
			"could not reconcile an interrupted merge",
			err,
		)
	}
	return updated, nil
}

func (a API) openLockedReviewRepository(
	ctx context.Context,
	credentials AuthInput,
	value string,
	role control.Role,
) (*git.Repository, repopath.Repository, auth.Principal, func(), error) {
	parsed, err := parseRepositoryPath(value)
	if err != nil {
		return nil, repopath.Repository{}, auth.Principal{}, nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperationLock, err := a.acquireRepositoryOperationLocks(parsed)
	if err != nil {
		return nil, repopath.Repository{}, auth.Principal{}, nil, huma.Error500InternalServerError(
			"could not lock repository operations",
			err,
		)
	}
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			_ = releaseOperationLock()
		})
	}
	principal, err := a.authorizeRepository(ctx, credentials, parsed, role)
	if err != nil {
		release()
		return nil, repopath.Repository{}, auth.Principal{}, nil, err
	}
	repositoryPath, err := a.Storage.GitPath(parsed)
	if err != nil {
		release()
		return nil, repopath.Repository{}, auth.Principal{}, nil, huma.Error400BadRequest(err.Error())
	}
	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		release()
		return nil, repopath.Repository{}, auth.Principal{}, nil, huma.Error404NotFound(
			"repository not found",
			err,
		)
	}
	return repository, parsed, principal, release, nil
}

func (a API) mergeRequestResponse(
	ctx context.Context,
	credentials AuthInput,
	repository *git.Repository,
	parsed repopath.Repository,
	request review.MergeRequest,
	includeFiles bool,
) (*mergeRequestOutput, error) {
	view, err := a.buildMergeRequestView(
		ctx,
		credentials,
		repository,
		parsed,
		request,
		includeFiles,
	)
	if err != nil {
		return nil, err
	}
	return &mergeRequestOutput{Body: view}, nil
}

func (a API) buildMergeRequestView(
	ctx context.Context,
	credentials AuthInput,
	repository *git.Repository,
	parsed repopath.Repository,
	request review.MergeRequest,
	includeFiles bool,
) (mergeRequestView, error) {
	comparison, err := compareMergeRequest(ctx, repository, request, includeFiles)
	if err != nil {
		return mergeRequestView{}, huma.Error500InternalServerError(
			"could not compare merge request branches",
			err,
		)
	}
	principal, principalErr := a.authorizeRepository(
		ctx,
		credentials,
		parsed,
		control.RoleRead,
	)
	canWrite := principalErr == nil && principal.Role.Allows(control.RoleDeveloper)
	canApproveOwn := principalErr == nil &&
		principal.Role.Allows(control.RoleMaintainer)
	approvals := make([]mergeRequestApprovalView, 0, len(request.Approvals))
	currentApprovals := 0
	staleApprovals := 0
	viewerApproved := false
	currentAuthors := map[string]struct{}{}
	for _, approval := range request.Approvals {
		current := (approval.Author != request.Author || approval.SelfApproval) &&
			approval.HeadCommit == comparison.HeadCommit
		approvals = append(approvals, mergeRequestApprovalView{
			Author:     approval.Author,
			HeadCommit: approval.HeadCommit,
			CreatedAt:  approval.CreatedAt,
			Current:    current,
		})
		if current {
			if _, duplicate := currentAuthors[approval.Author]; !duplicate {
				currentAuthors[approval.Author] = struct{}{}
				currentApprovals++
			}
			if principalErr == nil && approval.Author == principal.Name {
				viewerApproved = true
			}
		} else {
			staleApprovals++
		}
	}
	unresolved := 0
	threads := append([]review.Thread(nil), request.Threads...)
	if threads == nil {
		threads = []review.Thread{}
	}
	for _, thread := range threads {
		if !thread.Resolved {
			unresolved++
		}
	}
	required := request.RequiredApprovals
	if required < 1 {
		required = 1
	}
	conflicts := comparison.Conflicts
	if conflicts == nil {
		conflicts = []string{}
	}
	files := comparison.Files
	if files == nil {
		files = []repositoryComparisonFile{}
	}
	stateOpen := request.State == review.StateOpen
	canMerge := stateOpen &&
		!request.MergeInProgress &&
		canWrite &&
		currentApprovals >= required &&
		unresolved == 0 &&
		comparison.Mergeable
	return mergeRequestView{
		ID:                request.ID,
		Repository:        request.Repository,
		Title:             request.Title,
		Description:       request.Description,
		Target:            request.Target,
		Source:            request.Source,
		Author:            request.Author,
		State:             request.State,
		CreatedAt:         request.CreatedAt,
		UpdatedAt:         request.UpdatedAt,
		RequiredApprovals: required,
		CurrentApprovals:  currentApprovals,
		StaleApprovals:    staleApprovals,
		UnresolvedThreads: unresolved,
		HeadCommit:        comparison.HeadCommit,
		TargetCommit:      comparison.TargetCommit,
		Ahead:             comparison.Ahead,
		Behind:            comparison.Behind,
		Mergeable:         comparison.Mergeable,
		Conflicts:         conflicts,
		Files:             files,
		Approvals:         approvals,
		Threads:           threads,
		CanApprove: stateOpen &&
			!request.MergeInProgress &&
			canWrite &&
			(principal.Name != request.Author || canApproveOwn) &&
			!viewerApproved,
		ViewerApproved:  viewerApproved,
		CanMerge:        canMerge,
		CanUpdate:       request.State != review.StateMerged && !request.MergeInProgress && canWrite,
		MergeInProgress: request.MergeInProgress,
		MergedCommit:    request.MergedCommit,
		MergedStrategy:  request.MergedStrategy,
		MergedBy:        request.MergedBy,
		MergedAt:        request.MergedAt,
		ClosedBy:        request.ClosedBy,
		ClosedAt:        request.ClosedAt,
	}, nil
}

func compareMergeRequest(
	ctx context.Context,
	repository *git.Repository,
	request review.MergeRequest,
	includeFiles bool,
) (mergeRequestComparison, error) {
	var targetCommit, sourceCommit *object.Commit
	missing := ""
	if request.State == review.StateOpen {
		_, _, target, targetErr := resolveBranch(repository, request.Target)
		if targetErr == nil {
			targetCommit = target
		} else {
			missing = "Target branch no longer exists"
		}
		_, _, source, sourceErr := resolveBranch(repository, request.Source)
		if sourceErr == nil {
			sourceCommit = source
		} else if missing == "" {
			missing = "Source branch no longer exists"
		}
	}
	if targetCommit == nil && plumbing.IsHash(request.BaseCommit) {
		targetCommit, _ = repository.CommitObject(plumbing.NewHash(request.BaseCommit))
	}
	if sourceCommit == nil && plumbing.IsHash(request.HeadCommit) {
		sourceCommit, _ = repository.CommitObject(plumbing.NewHash(request.HeadCommit))
	}
	result := mergeRequestComparison{
		TargetCommit: request.BaseCommit,
		HeadCommit:   request.HeadCommit,
		Conflicts:    []string{},
		Files:        []repositoryComparisonFile{},
	}
	if targetCommit == nil || sourceCommit == nil {
		if missing == "" {
			missing = "Stored comparison commits are unavailable"
		}
		result.Conflicts = []string{missing}
		return result, nil
	}
	result.TargetCommit = targetCommit.Hash.String()
	result.HeadCommit = sourceCommit.Hash.String()
	ahead, behind, err := commitDifference(repository, targetCommit, sourceCommit)
	if err != nil {
		return result, err
	}
	result.Ahead = ahead
	result.Behind = behind
	mergeBase, mergeable, conflicts, err := assessBranchMerge(
		repository,
		targetCommit,
		sourceCommit,
	)
	if err != nil {
		return result, err
	}
	result.Mergeable = mergeable && missing == ""
	result.Conflicts = conflicts
	if missing != "" {
		result.Conflicts = append(result.Conflicts, missing)
	}
	if !includeFiles {
		return result, nil
	}
	diffBase := targetCommit
	if mergeBase != nil {
		diffBase = mergeBase
	}
	fromTree, err := diffBase.Tree()
	if err != nil {
		return result, err
	}
	toTree, err := sourceCommit.Tree()
	if err != nil {
		return result, err
	}
	result.Files, err = compareTrees(ctx, fromTree, toTree)
	return result, err
}

func validatedReviewBody(value string) (string, error) {
	body := strings.TrimSpace(value)
	if body == "" {
		return "", huma.Error400BadRequest("comment body is required")
	}
	if len(body) > maxMergeRequestMessageBytes {
		return "", huma.Error400BadRequest("comment body is too long")
	}
	return body, nil
}

func reviewMutationError(message string, err error) error {
	if errors.Is(err, review.ErrNotFound) {
		return huma.Error404NotFound("merge request not found")
	}
	if errors.Is(err, review.ErrDuplicate) {
		return huma.Error409Conflict(err.Error())
	}
	var statusErr huma.StatusError
	if errors.As(err, &statusErr) {
		return err
	}
	return huma.Error500InternalServerError(message, err)
}
