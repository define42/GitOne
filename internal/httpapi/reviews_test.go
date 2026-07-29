package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/review"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

type mergeRequestAPIFixture struct {
	service    API
	repository *git.Repository
	path       repopath.Repository
	alice      AuthInput
	bob        AuthInput
	base       plumbing.Hash
	head       plumbing.Hash
}

type mutableReviewIdentityProvider struct {
	mu        sync.RWMutex
	passwords map[string]string
}

func (p *mutableReviewIdentityProvider) Authenticate(
	_ context.Context,
	username string,
	password string,
) (string, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.passwords[username] != password {
		return "", errors.New("invalid credentials")
	}
	return username, nil
}

func (p *mutableReviewIdentityProvider) revoke(username string) {
	p.mu.Lock()
	delete(p.passwords, username)
	p.mu.Unlock()
}

func newMergeRequestAPIFixture(t *testing.T) mergeRequestAPIFixture {
	t.Helper()
	service, alice, base := repositoryAPIFixture(t)
	path := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}

	document, err := service.Resolver.Controls.Load(context.Background(), path.Group())
	if err != nil {
		t.Fatal(err)
	}
	document.Members["bob"] = control.RoleDeveloper
	if err = service.Storage.UpdateGroupControl(path.Group(), document, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate(path.Group())
	service.Resolver.Directory = testIdentityProvider{
		"alice": "secret",
		"bob":   "bob-secret",
	}
	service.Reviews = review.NewStore(service.Storage.Root)

	gitPath, err := service.Storage.GitPath(path)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	baseHash := plumbing.NewHash(base)
	head := commitReviewBranchFile(
		t,
		repository,
		baseHash,
		"feature",
		"feature.txt",
		"reviewed change\n",
	)

	return mergeRequestAPIFixture{
		service:    service,
		repository: repository,
		path:       path,
		alice:      alice,
		bob:        mergeRequestCredentials(t, "bob", "bob-secret"),
		base:       baseHash,
		head:       head,
	}
}

func mergeRequestCredentials(t *testing.T, username, password string) AuthInput {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth(username, password)
	return AuthInput{Authorization: request.Header.Get("Authorization")}
}

func commitReviewBranchFile(
	t *testing.T,
	repository *git.Repository,
	parent plumbing.Hash,
	branch string,
	name string,
	content string,
) plumbing.Hash {
	t.Helper()
	parentCommit, err := repository.CommitObject(parent)
	if err != nil {
		t.Fatal(err)
	}
	parentTree, err := parentCommit.Tree()
	if err != nil {
		t.Fatal(err)
	}
	entries := append([]object.TreeEntry(nil), parentTree.Entries...)
	replacement := object.TreeEntry{
		Name: name,
		Mode: filemode.Regular,
		Hash: storeTestBlob(t, repository, []byte(content)),
	}
	replaced := false
	for index := range entries {
		if entries[index].Name == name {
			entries[index] = replacement
			replaced = true
			break
		}
	}
	if !replaced {
		entries = append(entries, replacement)
	}
	commit := storeTestCommit(
		t,
		repository,
		storeTestTree(t, repository, entries...),
		parent,
	)
	if err = repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName(branch),
		commit.Hash,
	)); err != nil {
		t.Fatal(err)
	}
	return commit.Hash
}

func createTestMergeRequest(
	t *testing.T,
	fixture mergeRequestAPIFixture,
) *mergeRequestOutput {
	t.Helper()
	output, err := fixture.service.createMergeRequest(
		context.Background(),
		&createMergeRequestInput{
			AuthInput:  fixture.alice,
			Repository: fixture.path.Full(),
			Body: createMergeRequestBody{
				Title:       "  Add reviewed feature  ",
				Description: "  Ready for review.  ",
				Target:      "main",
				Source:      "feature",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func requireReviewHTTPStatus(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected HTTP %d error, got nil", want)
	}
	var statusError huma.StatusError
	if !errors.As(err, &statusError) {
		t.Fatalf("expected HTTP %d error, got %T: %v", want, err, err)
	}
	if statusError.GetStatus() != want {
		t.Fatalf("status = %d, want %d: %v", statusError.GetStatus(), want, err)
	}
}

func startReviewMutation(
	invoke func() error,
) (<-chan struct{}, <-chan error) {
	started := make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		close(started)
		completed <- invoke()
	}()
	return started, completed
}

func requireReviewMutationBlocked(
	t *testing.T,
	started <-chan struct{},
	completed <-chan error,
) {
	t.Helper()
	<-started
	select {
	case err := <-completed:
		t.Fatalf("review mutation completed while the operation lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
}

func awaitReviewMutation(t *testing.T, completed <-chan error) error {
	t.Helper()
	select {
	case err := <-completed:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("review mutation did not complete after the operation lock was released")
		return nil
	}
}

func requireUnchangedOpenReview(
	t *testing.T,
	fixture mergeRequestAPIFixture,
	id uint64,
) {
	t.Helper()
	request, err := fixture.service.Reviews.Get(fixture.path, id)
	if err != nil {
		t.Fatal(err)
	}
	if request.State != review.StateOpen ||
		len(request.Threads) != 0 ||
		len(request.Approvals) != 0 {
		t.Fatalf("review mutation escaped the operation lock: %#v", request)
	}
}

func TestReviewMutationsWaitForOperationLock(t *testing.T) {
	tests := []struct {
		name     string
		invoke   func(mergeRequestAPIFixture, uint64) error
		validate func(*testing.T, review.MergeRequest)
	}{
		{
			name: "state update",
			invoke: func(fixture mergeRequestAPIFixture, id uint64) error {
				_, err := fixture.service.updateMergeRequest(
					context.Background(),
					&updateMergeRequestInput{
						MergeRequestInput: mergeRequestInput{
							AuthInput:  fixture.alice,
							Repository: fixture.path.Full(),
							ID:         id,
						},
						Body: updateMergeRequestBody{State: review.StateClosed},
					},
				)
				return err
			},
			validate: func(t *testing.T, request review.MergeRequest) {
				t.Helper()
				if request.State != review.StateClosed || request.ClosedBy != "alice" {
					t.Fatalf("state update was not persisted after unlock: %#v", request)
				}
			},
		},
		{
			name: "discussion thread",
			invoke: func(fixture mergeRequestAPIFixture, id uint64) error {
				_, err := fixture.service.createReviewThread(
					context.Background(),
					&createReviewThreadInput{
						MergeRequestInput: mergeRequestInput{
							AuthInput:  fixture.alice,
							Repository: fixture.path.Full(),
							ID:         id,
						},
						Body: createReviewThreadBody{Body: "Wait for the repository lock."},
					},
				)
				return err
			},
			validate: func(t *testing.T, request review.MergeRequest) {
				t.Helper()
				if len(request.Threads) != 1 ||
					request.Threads[0].Comments[0].Body != "Wait for the repository lock." {
					t.Fatalf("discussion thread was not persisted after unlock: %#v", request)
				}
			},
		},
		{
			name: "approval",
			invoke: func(fixture mergeRequestAPIFixture, id uint64) error {
				_, err := fixture.service.approveMergeRequest(
					context.Background(),
					&approveMergeRequestInput{
						MergeRequestInput: mergeRequestInput{
							AuthInput:  fixture.bob,
							Repository: fixture.path.Full(),
							ID:         id,
						},
						Body: approveMergeRequestBody{
							ExpectedHeadCommit: fixture.head.String(),
						},
					},
				)
				return err
			},
			validate: func(t *testing.T, request review.MergeRequest) {
				t.Helper()
				if request.State != review.StateMerged ||
					len(request.Approvals) != 1 ||
					request.Approvals[0].Author != "bob" {
					t.Fatalf("approval was not persisted after unlock: %#v", request)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMergeRequestAPIFixture(t)
			created := createTestMergeRequest(t, fixture)
			release, err := fixture.service.Reviews.AcquireOperationLock()
			if err != nil {
				t.Fatal(err)
			}
			released := false
			defer func() {
				if !released {
					_ = release()
				}
			}()

			started, completed := startReviewMutation(func() error {
				return test.invoke(fixture, created.Body.ID)
			})
			requireReviewMutationBlocked(t, started, completed)
			requireUnchangedOpenReview(t, fixture, created.Body.ID)

			if err = release(); err != nil {
				t.Fatal(err)
			}
			released = true
			if err = awaitReviewMutation(t, completed); err != nil {
				t.Fatal(err)
			}
			persisted, err := fixture.service.Reviews.Get(fixture.path, created.Body.ID)
			if err != nil {
				t.Fatal(err)
			}
			test.validate(t, persisted)
		})
	}
}

func TestReviewMutationsReopenRepositoryAfterOperationLock(t *testing.T) {
	tests := []struct {
		name   string
		invoke func(mergeRequestAPIFixture, uint64) error
	}{
		{
			name: "state update",
			invoke: func(fixture mergeRequestAPIFixture, id uint64) error {
				_, err := fixture.service.updateMergeRequest(
					context.Background(),
					&updateMergeRequestInput{
						MergeRequestInput: mergeRequestInput{
							AuthInput:  fixture.alice,
							Repository: fixture.path.Full(),
							ID:         id,
						},
						Body: updateMergeRequestBody{State: review.StateClosed},
					},
				)
				return err
			},
		},
		{
			name: "discussion thread",
			invoke: func(fixture mergeRequestAPIFixture, id uint64) error {
				_, err := fixture.service.createReviewThread(
					context.Background(),
					&createReviewThreadInput{
						MergeRequestInput: mergeRequestInput{
							AuthInput:  fixture.alice,
							Repository: fixture.path.Full(),
							ID:         id,
						},
						Body: createReviewThreadBody{Body: "Do not write to a retired repository."},
					},
				)
				return err
			},
		},
		{
			name: "approval",
			invoke: func(fixture mergeRequestAPIFixture, id uint64) error {
				_, err := fixture.service.approveMergeRequest(
					context.Background(),
					&approveMergeRequestInput{
						MergeRequestInput: mergeRequestInput{
							AuthInput:  fixture.bob,
							Repository: fixture.path.Full(),
							ID:         id,
						},
						Body: approveMergeRequestBody{
							ExpectedHeadCommit: fixture.head.String(),
						},
					},
				)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newMergeRequestAPIFixture(t)
			created := createTestMergeRequest(t, fixture)
			release, err := fixture.service.Reviews.AcquireOperationLock()
			if err != nil {
				t.Fatal(err)
			}
			released := false
			defer func() {
				if !released {
					_ = release()
				}
			}()

			started, completed := startReviewMutation(func() error {
				return test.invoke(fixture, created.Body.ID)
			})
			requireReviewMutationBlocked(t, started, completed)
			gitPath, err := fixture.service.Storage.GitPath(fixture.path)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(gitPath, gitPath+".retired"); err != nil {
				t.Fatal(err)
			}
			if err = release(); err != nil {
				t.Fatal(err)
			}
			released = true

			err = awaitReviewMutation(t, completed)
			requireReviewHTTPStatus(t, err, http.StatusNotFound)
			requireUnchangedOpenReview(t, fixture, created.Body.ID)
		})
	}
}

func TestReviewApprovalReauthorizesAfterOperationLock(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	directory := &mutableReviewIdentityProvider{
		passwords: map[string]string{
			"alice": "secret",
			"bob":   "bob-secret",
		},
	}
	fixture.service.Resolver.Directory = directory
	release, err := fixture.service.Reviews.AcquireOperationLock()
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = release()
		}
	}()

	started, completed := startReviewMutation(func() error {
		_, approveErr := fixture.service.approveMergeRequest(
			context.Background(),
			&approveMergeRequestInput{
				MergeRequestInput: mergeRequestInput{
					AuthInput:  fixture.bob,
					Repository: fixture.path.Full(),
					ID:         created.Body.ID,
				},
				Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
			},
		)
		return approveErr
	})
	requireReviewMutationBlocked(t, started, completed)

	directory.revoke("bob")
	if err = release(); err != nil {
		t.Fatal(err)
	}
	released = true

	err = awaitReviewMutation(t, completed)
	requireReviewHTTPStatus(t, err, http.StatusUnauthorized)
	requireUnchangedOpenReview(t, fixture, created.Body.ID)
}

func TestMergeRequestCreatePersistsListsGetsAndValidatesOpenDuplicates(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	ctx := context.Background()
	created := createTestMergeRequest(t, fixture)
	if created.Body.ID != 1 ||
		created.Body.Repository != fixture.path.Full() ||
		created.Body.Title != "Add reviewed feature" ||
		created.Body.Description != "Ready for review." ||
		created.Body.Target != "main" ||
		created.Body.Source != "feature" ||
		created.Body.Author != "alice" ||
		created.Body.State != review.StateOpen ||
		created.Body.HeadCommit != fixture.head.String() ||
		created.Body.TargetCommit != fixture.base.String() ||
		created.Body.RequiredApprovals != 1 ||
		created.Body.CurrentApprovals != 0 ||
		!created.Body.CanApprove ||
		!created.Body.CanUpdate ||
		len(created.Body.Files) != 1 {
		t.Fatalf("unexpected created merge request: %#v", created.Body)
	}

	persisted, err := review.NewStore(fixture.service.Storage.Root).Get(
		fixture.path,
		created.Body.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Title != created.Body.Title ||
		persisted.Description != created.Body.Description ||
		persisted.HeadCommit != fixture.head.String() ||
		persisted.BaseCommit != fixture.base.String() ||
		persisted.Approvals == nil ||
		persisted.Threads == nil {
		t.Fatalf("unexpected persisted merge request: %#v", persisted)
	}

	listed, err := fixture.service.listMergeRequests(ctx, &mergeRequestsInput{
		AuthInput:  fixture.bob,
		Repository: fixture.path.Full(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if listed.Body.Repository != fixture.path.Full() ||
		len(listed.Body.MergeRequests) != 1 ||
		listed.Body.MergeRequests[0].ID != created.Body.ID ||
		len(listed.Body.MergeRequests[0].Files) != 0 {
		t.Fatalf("unexpected merge request list: %#v", listed.Body)
	}
	detail, err := fixture.service.getMergeRequest(ctx, &mergeRequestInput{
		AuthInput:  fixture.bob,
		Repository: fixture.path.Full(),
		ID:         created.Body.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Body.ID != created.Body.ID ||
		detail.Body.Author != "alice" ||
		!detail.Body.CanApprove ||
		len(detail.Body.Files) != 1 {
		t.Fatalf("unexpected merge request detail: %#v", detail.Body)
	}

	_, err = fixture.service.createMergeRequest(ctx, &createMergeRequestInput{
		AuthInput:  fixture.alice,
		Repository: fixture.path.Full(),
		Body: createMergeRequestBody{
			Title:  "Duplicate",
			Target: "main",
			Source: "feature",
		},
	})
	requireReviewHTTPStatus(t, err, http.StatusConflict)
	_, err = fixture.service.createMergeRequest(ctx, &createMergeRequestInput{
		AuthInput:  fixture.alice,
		Repository: fixture.path.Full(),
		Body: createMergeRequestBody{
			Title:  " ",
			Target: "main",
			Source: "feature",
		},
	})
	requireReviewHTTPStatus(t, err, http.StatusBadRequest)
	_, err = fixture.service.createMergeRequest(ctx, &createMergeRequestInput{
		AuthInput:  fixture.alice,
		Repository: fixture.path.Full(),
		Body: createMergeRequestBody{
			Title:  "Same branch",
			Target: "main",
			Source: "main",
		},
	})
	requireReviewHTTPStatus(t, err, http.StatusBadRequest)
	if err = fixture.repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("no-changes"),
		fixture.base,
	)); err != nil {
		t.Fatal(err)
	}
	_, err = fixture.service.createMergeRequest(ctx, &createMergeRequestInput{
		AuthInput:  fixture.alice,
		Repository: fixture.path.Full(),
		Body: createMergeRequestBody{
			Title:  "Nothing to merge",
			Target: "main",
			Source: "no-changes",
		},
	})
	requireReviewHTTPStatus(t, err, http.StatusConflict)

	closed, err := fixture.service.updateMergeRequest(ctx, &updateMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.alice,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: updateMergeRequestBody{State: review.StateClosed},
	})
	if err != nil {
		t.Fatal(err)
	}
	if closed.Body.State != review.StateClosed ||
		closed.Body.ClosedBy != "alice" ||
		closed.Body.ClosedAt == nil {
		t.Fatalf("unexpected closed merge request: %#v", closed.Body)
	}
	replacement := createTestMergeRequest(t, fixture)
	if replacement.Body.ID != 2 || replacement.Body.State != review.StateOpen {
		t.Fatalf("unexpected replacement merge request: %#v", replacement.Body)
	}
	_, err = fixture.service.updateMergeRequest(ctx, &updateMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.alice,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: updateMergeRequestBody{State: review.StateOpen},
	})
	requireReviewHTTPStatus(t, err, http.StatusConflict)
	all, err := fixture.service.listMergeRequests(ctx, &mergeRequestsInput{
		AuthInput:  fixture.alice,
		Repository: fixture.path.Full(),
		State:      "all",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Body.MergeRequests) != 2 ||
		all.Body.MergeRequests[0].ID != replacement.Body.ID ||
		all.Body.MergeRequests[1].ID != created.Body.ID {
		t.Fatalf("unexpected all-state merge request list: %#v", all.Body)
	}
}

func TestMergeRequestHTTPRoutes(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	mux := http.NewServeMux()
	Register(mux, fixture.service)

	create := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/merge-requests",
		strings.NewReader(`{
			"title":"Review over HTTP",
			"description":"Persist this review.",
			"target":"main",
			"source":"feature"
		}`),
	)
	create.Header.Set("Content-Type", "application/json")
	create.SetBasicAuth("alice", "secret")
	createdResponse := httptest.NewRecorder()
	mux.ServeHTTP(createdResponse, create)
	if createdResponse.Code != http.StatusCreated {
		t.Fatalf(
			"create status = %d, want %d: %s",
			createdResponse.Code,
			http.StatusCreated,
			createdResponse.Body.String(),
		)
	}
	var created mergeRequestView
	if err := json.Unmarshal(createdResponse.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID != 1 ||
		created.Title != "Review over HTTP" ||
		created.HeadCommit != fixture.head.String() {
		t.Fatalf("unexpected HTTP create response: %#v", created)
	}

	list := httptest.NewRequest(
		http.MethodGet,
		"/api/repositories/engineering%2Fapi/merge-requests?state=all",
		nil,
	)
	list.SetBasicAuth("bob", "bob-secret")
	listResponse := httptest.NewRecorder()
	mux.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK {
		t.Fatalf(
			"list status = %d, want %d: %s",
			listResponse.Code,
			http.StatusOK,
			listResponse.Body.String(),
		)
	}
	var listed struct {
		Repository    string             `json:"repository"`
		MergeRequests []mergeRequestView `json:"mergeRequests"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.Repository != fixture.path.Full() ||
		len(listed.MergeRequests) != 1 ||
		listed.MergeRequests[0].ID != created.ID {
		t.Fatalf("unexpected HTTP list response: %#v", listed)
	}

	thread := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/merge-requests/1/threads",
		strings.NewReader(`{"body":"Please explain this change."}`),
	)
	thread.Header.Set("Content-Type", "application/json")
	thread.SetBasicAuth("bob", "bob-secret")
	threadResponse := httptest.NewRecorder()
	mux.ServeHTTP(threadResponse, thread)
	if threadResponse.Code != http.StatusCreated {
		t.Fatalf(
			"thread status = %d, want %d: %s",
			threadResponse.Code,
			http.StatusCreated,
			threadResponse.Body.String(),
		)
	}

	reply := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/merge-requests/1/threads/1/comments",
		strings.NewReader(`{"body":"It changes the reviewed behavior."}`),
	)
	reply.Header.Set("Content-Type", "application/json")
	reply.SetBasicAuth("alice", "secret")
	replyResponse := httptest.NewRecorder()
	mux.ServeHTTP(replyResponse, reply)
	if replyResponse.Code != http.StatusCreated {
		t.Fatalf(
			"reply status = %d, want %d: %s",
			replyResponse.Code,
			http.StatusCreated,
			replyResponse.Body.String(),
		)
	}

	resolve := httptest.NewRequest(
		http.MethodPatch,
		"/api/repositories/engineering%2Fapi/merge-requests/1/threads/1",
		strings.NewReader(`{"resolved":true}`),
	)
	resolve.Header.Set("Content-Type", "application/json")
	resolve.SetBasicAuth("bob", "bob-secret")
	resolveResponse := httptest.NewRecorder()
	mux.ServeHTTP(resolveResponse, resolve)
	if resolveResponse.Code != http.StatusOK {
		t.Fatalf(
			"resolve status = %d, want %d: %s",
			resolveResponse.Code,
			http.StatusOK,
			resolveResponse.Body.String(),
		)
	}

	for _, state := range []review.State{review.StateClosed, review.StateOpen} {
		update := httptest.NewRequest(
			http.MethodPatch,
			"/api/repositories/engineering%2Fapi/merge-requests/1",
			strings.NewReader(fmt.Sprintf(`{"state":%q}`, state)),
		)
		update.Header.Set("Content-Type", "application/json")
		update.SetBasicAuth("alice", "secret")
		updateResponse := httptest.NewRecorder()
		mux.ServeHTTP(updateResponse, update)
		if updateResponse.Code != http.StatusOK {
			t.Fatalf(
				"update to %s status = %d, want %d: %s",
				state,
				updateResponse.Code,
				http.StatusOK,
				updateResponse.Body.String(),
			)
		}
	}

	approve := httptest.NewRequest(
		http.MethodPost,
		"/api/repositories/engineering%2Fapi/merge-requests/1/approvals",
		strings.NewReader(fmt.Sprintf(
			`{"expectedHeadCommit":%q}`,
			fixture.head.String(),
		)),
	)
	approve.Header.Set("Content-Type", "application/json")
	approve.SetBasicAuth("bob", "bob-secret")
	approveResponse := httptest.NewRecorder()
	mux.ServeHTTP(approveResponse, approve)
	if approveResponse.Code != http.StatusOK {
		t.Fatalf(
			"approval status = %d, want %d: %s",
			approveResponse.Code,
			http.StatusOK,
			approveResponse.Body.String(),
		)
	}
	var approved mergeRequestView
	if err := json.Unmarshal(approveResponse.Body.Bytes(), &approved); err != nil {
		t.Fatal(err)
	}
	if approved.State != review.StateMerged {
		t.Fatalf("approval did not merge the request: %#v", approved)
	}
}

func TestMergeRequestDiscussionApprovalAndExplicitMergeRetry(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	ctx := context.Background()
	created := createTestMergeRequest(t, fixture)

	_, err := fixture.service.approveMergeRequest(ctx, &approveMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: approveMergeRequestBody{ExpectedHeadCommit: plumbing.ZeroHash.String()},
	})
	requireReviewHTTPStatus(t, err, http.StatusConflict)

	threaded, err := fixture.service.createReviewThread(ctx, &createReviewThreadInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.alice,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: createReviewThreadBody{Body: "  Please explain this change.  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	if threaded.Body.UnresolvedThreads != 1 ||
		len(threaded.Body.Threads) != 1 ||
		threaded.Body.Threads[0].ID != 1 ||
		len(threaded.Body.Threads[0].Comments) != 1 ||
		threaded.Body.Threads[0].Comments[0].Author != "alice" ||
		threaded.Body.Threads[0].Comments[0].Body != "Please explain this change." {
		t.Fatalf("unexpected discussion thread: %#v", threaded.Body)
	}

	replied, err := fixture.service.addReviewComment(ctx, &addReviewCommentInput{
		ReviewThreadInput: reviewThreadInput{
			MergeRequestInput: mergeRequestInput{
				AuthInput:  fixture.bob,
				Repository: fixture.path.Full(),
				ID:         created.Body.ID,
			},
			ThreadID: 1,
		},
		Body: createReviewThreadBody{Body: "  It fixes the review workflow.  "},
	})
	if err != nil {
		t.Fatal(err)
	}
	comments := replied.Body.Threads[0].Comments
	if len(comments) != 2 ||
		comments[1].ID != 2 ||
		comments[1].Author != "bob" ||
		comments[1].Body != "It fixes the review workflow." {
		t.Fatalf("unexpected discussion reply: %#v", comments)
	}

	approved, err := fixture.service.approveMergeRequest(ctx, &approveMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Body.State != review.StateOpen ||
		approved.Body.CurrentApprovals != 1 ||
		approved.Body.UnresolvedThreads != 1 ||
		!approved.Body.ViewerApproved ||
		approved.Body.CanMerge ||
		len(approved.Body.Approvals) != 1 ||
		approved.Body.Approvals[0].Author != "bob" ||
		approved.Body.Approvals[0].HeadCommit != fixture.head.String() ||
		!approved.Body.Approvals[0].Current {
		t.Fatalf("unexpected approval state: %#v", approved.Body)
	}
	persisted, err := review.NewStore(fixture.service.Storage.Root).Get(
		fixture.path,
		created.Body.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Approvals) != 1 ||
		len(persisted.Threads) != 1 ||
		len(persisted.Threads[0].Comments) != 2 {
		t.Fatalf("review mutations were not persisted: %#v", persisted)
	}

	_, err = fixture.service.mergeApprovedRequest(ctx, &approveMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
	})
	requireReviewHTTPStatus(t, err, http.StatusConflict)
	target, err := fixture.repository.Reference(
		plumbing.NewBranchReferenceName("main"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if target.Hash() != fixture.base {
		t.Fatalf("blocked merge moved main to %s", target.Hash())
	}

	resolved, err := fixture.service.updateReviewThread(ctx, &updateReviewThreadInput{
		ReviewThreadInput: reviewThreadInput{
			MergeRequestInput: mergeRequestInput{
				AuthInput:  fixture.bob,
				Repository: fixture.path.Full(),
				ID:         created.Body.ID,
			},
			ThreadID: 1,
		},
		Body: updateReviewThreadBody{Resolved: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Body.UnresolvedThreads != 0 ||
		!resolved.Body.CanMerge ||
		!resolved.Body.Threads[0].Resolved ||
		resolved.Body.Threads[0].ResolvedBy != "bob" ||
		resolved.Body.Threads[0].ResolvedAt == nil {
		t.Fatalf("unexpected resolved thread: %#v", resolved.Body)
	}

	merged, err := fixture.service.mergeApprovedRequest(ctx, &approveMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Body.State != review.StateMerged ||
		merged.Body.MergedCommit != fixture.head.String() ||
		merged.Body.MergedStrategy != "fast-forward" ||
		merged.Body.MergedBy != "bob" ||
		merged.Body.MergedAt == nil ||
		merged.Body.CanMerge ||
		merged.Body.CanUpdate {
		t.Fatalf("unexpected merged request: %#v", merged.Body)
	}
	target, err = fixture.repository.Reference(
		plumbing.NewBranchReferenceName("main"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if target.Hash() != fixture.head {
		t.Fatalf("main = %s, want merged head %s", target.Hash(), fixture.head)
	}
}

func TestMergeRequestMaintainerAndOwnerCanApproveOwnChanges(t *testing.T) {
	for _, role := range []control.Role{
		control.RoleMaintainer,
		control.RoleOwner,
	} {
		t.Run(string(role), func(t *testing.T) {
			fixture := newMergeRequestAPIFixture(t)
			if role == control.RoleMaintainer {
				document, err := fixture.service.Resolver.Controls.Load(
					context.Background(),
					fixture.path.Group(),
				)
				if err != nil {
					t.Fatal(err)
				}
				document.Members["alice"] = control.RoleMaintainer
				document.Members["carol"] = control.RoleOwner
				if err = fixture.service.Storage.UpdateGroupControl(
					fixture.path.Group(),
					document,
					"alice",
				); err != nil {
					t.Fatal(err)
				}
				fixture.service.Resolver.Controls.Invalidate(fixture.path.Group())
			}
			created := createTestMergeRequest(t, fixture)
			if !created.Body.CanApprove {
				t.Fatalf("%s cannot approve own request: %#v", role, created.Body)
			}

			merged, err := fixture.service.approveMergeRequest(
				context.Background(),
				&approveMergeRequestInput{
					MergeRequestInput: mergeRequestInput{
						AuthInput:  fixture.alice,
						Repository: fixture.path.Full(),
						ID:         created.Body.ID,
					},
					Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if merged.Body.State != review.StateMerged ||
				merged.Body.CurrentApprovals != 1 ||
				!merged.Body.ViewerApproved ||
				merged.Body.MergedBy != "alice" {
				t.Fatalf("%s approval did not merge own request: %#v", role, merged.Body)
			}
			persisted, err := review.NewStore(fixture.service.Storage.Root).Get(
				fixture.path,
				created.Body.ID,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(persisted.Approvals) != 1 ||
				persisted.Approvals[0].Author != "alice" ||
				!persisted.Approvals[0].SelfApproval {
				t.Fatalf("%s self-approval was not persisted: %#v", role, persisted.Approvals)
			}
		})
	}
}

func TestMergeRequestDeveloperCannotApproveOwnChanges(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created, err := fixture.service.createMergeRequest(
		context.Background(),
		&createMergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			Body: createMergeRequestBody{
				Title:  "Bob's feature",
				Target: "main",
				Source: "feature",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if created.Body.Author != "bob" || created.Body.CanApprove {
		t.Fatalf("developer author has unexpected permissions: %#v", created.Body)
	}

	_, err = fixture.service.approveMergeRequest(
		context.Background(),
		&approveMergeRequestInput{
			MergeRequestInput: mergeRequestInput{
				AuthInput:  fixture.bob,
				Repository: fixture.path.Full(),
				ID:         created.Body.ID,
			},
			Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
		},
	)
	requireReviewHTTPStatus(t, err, http.StatusForbidden)
}

func TestMergeRequestApprovalAutoMergesWhenReady(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)

	merged, err := fixture.service.approveMergeRequest(
		context.Background(),
		&approveMergeRequestInput{
			MergeRequestInput: mergeRequestInput{
				AuthInput:  fixture.bob,
				Repository: fixture.path.Full(),
				ID:         created.Body.ID,
			},
			Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Body.State != review.StateMerged ||
		merged.Body.CurrentApprovals != 1 ||
		merged.Body.MergedCommit != fixture.head.String() ||
		merged.Body.MergedStrategy != "fast-forward" ||
		merged.Body.MergedBy != "bob" {
		t.Fatalf("approval did not auto-merge ready request: %#v", merged.Body)
	}
	persisted, err := review.NewStore(fixture.service.Storage.Root).Get(
		fixture.path,
		created.Body.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != review.StateMerged ||
		persisted.MergedCommit != fixture.head.String() ||
		persisted.MergedBy != "bob" ||
		len(persisted.Approvals) != 1 {
		t.Fatalf("auto-merge state was not persisted: %#v", persisted)
	}
}

func TestMergeRequestSourceAdvanceMakesApprovalStale(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	ctx := context.Background()
	created := createTestMergeRequest(t, fixture)
	_, err := fixture.service.createReviewThread(ctx, &createReviewThreadInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.alice,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: createReviewThreadBody{Body: "Hold this merge while reviewing."},
	})
	if err != nil {
		t.Fatal(err)
	}
	approved, err := fixture.service.approveMergeRequest(ctx, &approveMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if approved.Body.State != review.StateOpen || approved.Body.CurrentApprovals != 1 {
		t.Fatalf("unexpected initial approval: %#v", approved.Body)
	}

	advancedHead := commitReviewBranchFile(
		t,
		fixture.repository,
		fixture.head,
		"feature",
		"feature.txt",
		"unreviewed follow-up\n",
	)
	detail, err := fixture.service.getMergeRequest(ctx, &mergeRequestInput{
		AuthInput:  fixture.bob,
		Repository: fixture.path.Full(),
		ID:         created.Body.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Body.HeadCommit != advancedHead.String() ||
		detail.Body.CurrentApprovals != 0 ||
		detail.Body.StaleApprovals != 1 ||
		detail.Body.ViewerApproved ||
		len(detail.Body.Approvals) != 1 ||
		detail.Body.Approvals[0].Current {
		t.Fatalf("source advance did not stale approval: %#v", detail.Body)
	}

	_, err = fixture.service.mergeApprovedRequest(ctx, &approveMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: approveMergeRequestBody{ExpectedHeadCommit: fixture.head.String()},
	})
	requireReviewHTTPStatus(t, err, http.StatusConflict)
	_, err = fixture.service.mergeApprovedRequest(ctx, &approveMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: approveMergeRequestBody{ExpectedHeadCommit: advancedHead.String()},
	})
	requireReviewHTTPStatus(t, err, http.StatusConflict)
	target, err := fixture.repository.Reference(
		plumbing.NewBranchReferenceName("main"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if target.Hash() != fixture.base {
		t.Fatalf("stale approval moved main to %s", target.Hash())
	}

	reapproved, err := fixture.service.approveMergeRequest(ctx, &approveMergeRequestInput{
		MergeRequestInput: mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
		Body: approveMergeRequestBody{ExpectedHeadCommit: advancedHead.String()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if reapproved.Body.State != review.StateOpen ||
		reapproved.Body.CurrentApprovals != 1 ||
		reapproved.Body.StaleApprovals != 0 ||
		reapproved.Body.Approvals[0].HeadCommit != advancedHead.String() ||
		!reapproved.Body.Approvals[0].Current {
		t.Fatalf("unexpected renewed approval: %#v", reapproved.Body)
	}
}

func TestMergeRequestClaimBlocksConcurrentReviewMutations(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	started := time.Now().Add(-24 * time.Hour).UTC()
	claimID := "11111111111111111111111111111111"
	activeMergeClaims.Store(claimID, struct{}{})
	defer activeMergeClaims.Delete(claimID)
	_, err := fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.Approvals = append(request.Approvals, review.Approval{
				Author:     "bob",
				HeadCommit: fixture.head.String(),
				CreatedAt:  started,
			})
			request.MergeInProgress = true
			request.MergeClaimID = claimID
			request.MergeOwnerID = mergeProcessID
			request.MergeHeadCommit = fixture.head.String()
			request.MergeStartedBy = "bob"
			request.MergeStartedAt = &started
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	detail, err := fixture.service.getMergeRequest(
		context.Background(),
		&mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !detail.Body.MergeInProgress ||
		detail.Body.CanApprove ||
		detail.Body.CanMerge ||
		detail.Body.CanUpdate {
		t.Fatalf("merge claim did not disable review mutations: %#v", detail.Body)
	}

	_, err = fixture.service.createReviewThread(
		context.Background(),
		&createReviewThreadInput{
			MergeRequestInput: mergeRequestInput{
				AuthInput:  fixture.alice,
				Repository: fixture.path.Full(),
				ID:         created.Body.ID,
			},
			Body: createReviewThreadBody{Body: "This must not race the merge."},
		},
	)
	requireReviewHTTPStatus(t, err, http.StatusConflict)
	_, err = fixture.service.updateMergeRequest(
		context.Background(),
		&updateMergeRequestInput{
			MergeRequestInput: mergeRequestInput{
				AuthInput:  fixture.alice,
				Repository: fixture.path.Full(),
				ID:         created.Body.ID,
			},
			Body: updateMergeRequestBody{State: review.StateClosed},
		},
	)
	requireReviewHTTPStatus(t, err, http.StatusConflict)
}

func TestMergeRequestOldReleaseCannotClearNewClaim(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	started := time.Now().UTC()
	currentClaim := "44444444444444444444444444444444"
	_, err := fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.MergeInProgress = true
			request.MergeClaimID = currentClaim
			request.MergeOwnerID = mergeProcessID
			request.MergeHeadCommit = fixture.head.String()
			request.MergeStartedBy = "bob"
			request.MergeStartedAt = &started
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	fixture.service.releaseMergeClaim(
		fixture.path,
		created.Body.ID,
		"55555555555555555555555555555555",
	)
	persisted, err := fixture.service.Reviews.Get(fixture.path, created.Body.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.MergeInProgress || persisted.MergeClaimID != currentClaim {
		t.Fatalf("old release cleared the current claim: %#v", persisted)
	}

	fixture.service.releaseMergeClaim(fixture.path, created.Body.ID, currentClaim)
	persisted, err = fixture.service.Reviews.Get(fixture.path, created.Body.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.MergeInProgress || persisted.MergeClaimID != "" {
		t.Fatalf("matching release did not clear the claim: %#v", persisted)
	}
}

func TestMergeRequestRecoveryWaitsForForeignMergeTransaction(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	started := time.Now().UTC()
	_, err := fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.MergeInProgress = true
			request.MergeClaimID = "77777777777777777777777777777777"
			request.MergeOwnerID = "other-live-process"
			request.MergeHeadCommit = fixture.head.String()
			request.MergeStartedBy = "bob"
			request.MergeStartedAt = &started
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	release, err := fixture.service.Reviews.AcquireMergeLock(fixture.path)
	if err != nil {
		t.Fatal(err)
	}
	type response struct {
		output *mergeRequestOutput
		err    error
	}
	completed := make(chan response, 1)
	go func() {
		output, requestErr := fixture.service.getMergeRequest(
			context.Background(),
			&mergeRequestInput{
				AuthInput:  fixture.bob,
				Repository: fixture.path.Full(),
				ID:         created.Body.ID,
			},
		)
		completed <- response{output: output, err: requestErr}
	}()
	select {
	case result := <-completed:
		t.Fatalf("recovery bypassed the foreign merge lock: %#v, %v", result.output, result.err)
	case <-time.After(50 * time.Millisecond):
	}

	if err = fixture.repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		fixture.head,
	)); err != nil {
		t.Fatal(err)
	}
	mergedAt := time.Now().UTC()
	_, err = fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.State = review.StateMerged
			request.HeadCommit = fixture.head.String()
			request.MergedCommit = fixture.head.String()
			request.MergedStrategy = "fast-forward"
			request.MergedBy = "bob"
			request.MergedAt = &mergedAt
			clearMergeClaim(request)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = release(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-completed:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.output.Body.State != review.StateMerged ||
			result.output.Body.MergedCommit != fixture.head.String() {
			t.Fatalf("unexpected result after foreign merge completed: %#v", result.output.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("recovery did not resume after the foreign merge lock was released")
	}
}

func TestMergeRequestRecoversInterruptedMergeClaim(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	started := time.Now().Add(-time.Hour).UTC()
	_, err := fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.Approvals = append(request.Approvals, review.Approval{
				Author:     "bob",
				HeadCommit: fixture.head.String(),
				CreatedAt:  started,
			})
			request.MergeInProgress = true
			request.MergeClaimID = "22222222222222222222222222222222"
			request.MergeOwnerID = "retired-test-process"
			request.MergeHeadCommit = fixture.head.String()
			request.MergeTargetCommit = fixture.base.String()
			request.MergeResultCommit = fixture.head.String()
			request.MergeResultStrategy = "fast-forward"
			request.MergeStartedBy = "bob"
			request.MergeStartedAt = &started
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.Storer.SetReference(plumbing.NewHashReference(
		plumbing.NewBranchReferenceName("main"),
		fixture.head,
	)); err != nil {
		t.Fatal(err)
	}

	recovered, err := fixture.service.getMergeRequest(
		context.Background(),
		&mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Body.State != review.StateMerged ||
		recovered.Body.MergeInProgress ||
		recovered.Body.MergedCommit != fixture.head.String() ||
		recovered.Body.MergedBy != "bob" {
		t.Fatalf("stale merge claim was not reconciled: %#v", recovered.Body)
	}
	persisted, err := review.NewStore(fixture.service.Storage.Root).Get(
		fixture.path,
		created.Body.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.State != review.StateMerged ||
		persisted.MergeInProgress ||
		persisted.MergeStartedAt != nil ||
		persisted.MergeClaimID != "" {
		t.Fatalf("reconciled merge was not persisted: %#v", persisted)
	}
}

func TestMergeRequestRecoveryPreservesPlannedResultAfterTargetAdvances(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	advancedTarget := commitReviewBranchFile(
		t,
		fixture.repository,
		fixture.head,
		"main",
		"after.txt",
		"later target change\n",
	)
	started := time.Now().Add(-time.Hour).UTC()
	_, err := fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.MergeInProgress = true
			request.MergeClaimID = "66666666666666666666666666666666"
			request.MergeOwnerID = "retired-test-process"
			request.MergeHeadCommit = fixture.head.String()
			request.MergeTargetCommit = fixture.base.String()
			request.MergeResultCommit = fixture.head.String()
			request.MergeResultStrategy = "fast-forward"
			request.MergeStartedBy = "bob"
			request.MergeStartedAt = &started
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := fixture.service.getMergeRequest(
		context.Background(),
		&mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Body.State != review.StateMerged ||
		recovered.Body.MergeInProgress ||
		recovered.Body.MergedCommit != fixture.head.String() ||
		recovered.Body.MergedStrategy != "fast-forward" ||
		recovered.Body.MergedBy != "bob" {
		t.Fatalf("advanced target lost the planned merge metadata: %#v", recovered.Body)
	}
	target, err := fixture.repository.Reference(
		plumbing.NewBranchReferenceName("main"),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	if target.Hash() != advancedTarget {
		t.Fatalf("recovery changed the advanced target to %s", target.Hash())
	}
}

func TestMergeRequestRecoveryRetainsAmbiguousPlannedResult(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	_ = commitReviewBranchFile(
		t,
		fixture.repository,
		fixture.base,
		"main",
		"target-only.txt",
		"rewritten target\n",
	)
	started := time.Now().Add(-time.Hour).UTC()
	claimID := "99999999999999999999999999999999"
	_, err := fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.MergeInProgress = true
			request.MergeClaimID = claimID
			request.MergeOwnerID = "retired-test-process"
			request.MergeHeadCommit = fixture.head.String()
			request.MergeTargetCommit = fixture.base.String()
			request.MergeResultCommit = fixture.head.String()
			request.MergeResultStrategy = "fast-forward"
			request.MergeStartedBy = "bob"
			request.MergeStartedAt = &started
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = fixture.service.getMergeRequest(
		context.Background(),
		&mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
	)
	requireReviewHTTPStatus(t, err, http.StatusConflict)
	persisted, err := fixture.service.Reviews.Get(fixture.path, created.Body.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.MergeInProgress ||
		persisted.MergeClaimID != claimID ||
		persisted.MergeResultCommit != fixture.head.String() {
		t.Fatalf("ambiguous recovery discarded its write-ahead plan: %#v", persisted)
	}
}

func TestMergeRequestReleasesAbandonedMergeClaim(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	started := time.Now().Add(-time.Hour).UTC()
	_, err := fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.MergeInProgress = true
			request.MergeClaimID = "33333333333333333333333333333333"
			request.MergeOwnerID = "retired-test-process"
			request.MergeHeadCommit = fixture.head.String()
			request.MergeStartedBy = "bob"
			request.MergeStartedAt = &started
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := fixture.service.getMergeRequest(
		context.Background(),
		&mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Body.State != review.StateOpen ||
		recovered.Body.MergeInProgress ||
		!recovered.Body.CanApprove {
		t.Fatalf("abandoned merge claim was not released: %#v", recovered.Body)
	}
}

func TestMergeRequestRecoveryRetainsPlanWhenTargetCannotBeRead(t *testing.T) {
	fixture := newMergeRequestAPIFixture(t)
	created := createTestMergeRequest(t, fixture)
	started := time.Now().Add(-time.Hour).UTC()
	claimID := "88888888888888888888888888888888"
	_, err := fixture.service.Reviews.Update(
		fixture.path,
		created.Body.ID,
		func(request *review.MergeRequest) error {
			request.MergeInProgress = true
			request.MergeClaimID = claimID
			request.MergeOwnerID = "retired-test-process"
			request.MergeHeadCommit = fixture.head.String()
			request.MergeTargetCommit = fixture.base.String()
			request.MergeResultCommit = fixture.head.String()
			request.MergeResultStrategy = "fast-forward"
			request.MergeStartedBy = "bob"
			request.MergeStartedAt = &started
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixture.repository.Storer.RemoveReference(
		plumbing.NewBranchReferenceName("main"),
	); err != nil {
		t.Fatal(err)
	}

	_, err = fixture.service.getMergeRequest(
		context.Background(),
		&mergeRequestInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         created.Body.ID,
		},
	)
	requireReviewHTTPStatus(t, err, http.StatusInternalServerError)
	persisted, err := fixture.service.Reviews.Get(fixture.path, created.Body.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted.MergeInProgress ||
		persisted.MergeClaimID != claimID ||
		persisted.MergeResultCommit != fixture.head.String() {
		t.Fatalf("failed recovery discarded the write-ahead plan: %#v", persisted)
	}
}
