package review

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

func testRepository() repopath.Repository {
	return repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
}

func testMergeRequest(title string) MergeRequest {
	return MergeRequest{
		Title:             title,
		Description:       "Review this change",
		Target:            "main",
		Source:            "feature",
		Author:            "alice",
		BaseCommit:        "1111111111111111111111111111111111111111111111111111111111111111",
		HeadCommit:        "2222222222222222222222222222222222222222222222222222222222222222",
		RequiredApprovals: 1,
	}
}

func createTestGitStore(t *testing.T, root string, repository repopath.Repository) {
	t.Helper()
	path, err := repositoryGitDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
}

func TestStorePersistsCreatesListsAndUpdates(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	nonUTC := time.FixedZone("test", 2*60*60)
	createdAt := time.Date(2026, 7, 28, 10, 0, 0, 0, nonUTC)
	first := testMergeRequest("First")
	first.CreatedAt = createdAt
	first.UpdatedAt = createdAt

	if err := store.Create(repository, &first); err != nil {
		t.Fatal(err)
	}
	if first.ID != 1 || first.Repository != repository.Full() ||
		first.State != StateOpen || first.RequiredApprovals != 1 {
		t.Fatalf("unexpected created merge request: %#v", first)
	}
	if first.CreatedAt.Location() != time.UTC ||
		first.UpdatedAt.Location() != time.UTC {
		t.Fatalf("timestamps were not normalized to UTC: %#v", first)
	}
	if first.Approvals == nil || first.Threads == nil {
		t.Fatalf("arrays were not normalized: %#v", first)
	}

	second := testMergeRequest("Second")
	second.Source = "feature-two"
	if err := store.Create(repository, &second); err != nil {
		t.Fatal(err)
	}
	if second.ID != 2 {
		t.Fatalf("second merge request ID = %d, want 2", second.ID)
	}

	reopened := NewStore(root)
	requests, err := reopened.List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].ID != 2 || requests[1].ID != 1 {
		t.Fatalf("merge requests are not in newest-ID-first order: %#v", requests)
	}
	persisted, err := reopened.Get(repository, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Title != first.Title || persisted.CreatedAt != first.CreatedAt {
		t.Fatalf("unexpected persisted merge request: %#v", persisted)
	}

	approvalTime := time.Date(2026, 7, 29, 9, 0, 0, 0, nonUTC)
	threadTime := approvalTime.Add(time.Minute)
	updated, err := reopened.Update(repository, first.ID, func(request *MergeRequest) error {
		request.Approvals = append(request.Approvals, Approval{
			Author:     "bob",
			HeadCommit: request.HeadCommit,
			CreatedAt:  approvalTime,
		})
		request.Threads = append(request.Threads, Thread{
			ID:        1,
			CreatedAt: threadTime,
			Comments: []Comment{{
				ID:        1,
				Author:    "bob",
				Body:      "Please add a test.",
				CreatedAt: threadTime,
			}},
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Approvals) != 1 || len(updated.Threads) != 1 ||
		updated.Approvals[0].CreatedAt.Location() != time.UTC ||
		updated.Threads[0].CreatedAt.Location() != time.UTC ||
		updated.Threads[0].Comments[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("unexpected normalized update: %#v", updated)
	}
	again, err := NewStore(root).Get(repository, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Approvals) != 1 || len(again.Threads) != 1 {
		t.Fatalf("update was not persisted: %#v", again)
	}
}

func TestStoreRejectsMissingInvalidAndFailedUpdates(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)

	requests, err := store.List(repository)
	if err != nil || len(requests) != 0 {
		t.Fatalf("missing review directory list = %#v, %v", requests, err)
	}
	if _, err = store.Get(repository, 0); err == nil {
		t.Fatal("zero merge request ID was accepted")
	}
	if _, err = store.Get(repository, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing merge request error = %v", err)
	}
	if err = store.Create(repository, nil); err == nil {
		t.Fatal("nil merge request was accepted")
	}

	request := testMergeRequest("Original")
	if err = store.Create(repository, &request); err != nil {
		t.Fatal(err)
	}
	updateErr := errors.New("do not save")
	if _, err = store.Update(repository, request.ID, func(candidate *MergeRequest) error {
		candidate.Title = "Changed"
		return updateErr
	}); !errors.Is(err, updateErr) {
		t.Fatalf("failed update error = %v", err)
	}
	persisted, err := store.Get(repository, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Title != "Original" {
		t.Fatalf("failed update was persisted: %#v", persisted)
	}
	if _, err = store.Update(repository, request.ID, func(candidate *MergeRequest) error {
		candidate.ID++
		return nil
	}); err == nil {
		t.Fatal("update changed the merge request ID")
	}
}

func TestStoreDoesNotCreateReviewsForMissingRepository(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	store := NewStore(root)
	request := testMergeRequest("Orphan")

	if err := store.Create(repository, &request); err == nil {
		t.Fatal("merge request was created without a repository")
	}
	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan review directory exists: %v", err)
	}
}

func TestMergeLockSerializesRepositoryTransactions(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	store := NewStore(root)
	releaseFirst, err := store.AcquireMergeLock(repository)
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan func() error, 1)
	errs := make(chan error, 1)
	go func() {
		release, lockErr := NewStore(root).AcquireMergeLock(repository)
		if lockErr != nil {
			errs <- lockErr
			return
		}
		acquired <- release
	}()

	select {
	case release := <-acquired:
		_ = release()
		t.Fatal("second merge lock was acquired before the first was released")
	case lockErr := <-errs:
		t.Fatal(lockErr)
	case <-time.After(50 * time.Millisecond):
	}
	if err = releaseFirst(); err != nil {
		t.Fatal(err)
	}
	select {
	case release := <-acquired:
		if err = release(); err != nil {
			t.Fatal(err)
		}
	case lockErr := <-errs:
		t.Fatal(lockErr)
	case <-time.After(time.Second):
		t.Fatal("second merge lock did not acquire after release")
	}
}

func TestStoreSkipsMalformedPersistedRecords(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	request := testMergeRequest("Valid")
	if err := store.Create(repository, &request); err != nil {
		t.Fatal(err)
	}
	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}

	if err = os.WriteFile(
		filepath.Join(directory, "2.json"),
		[]byte(`{"id":2,"unknown":true}`),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	requests, err := store.List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].ID != request.ID {
		t.Fatalf("malformed JSON record affected list: %#v", requests)
	}
	if err = os.Remove(filepath.Join(directory, "2.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "02.json"), []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	requests, err = store.List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].ID != request.ID {
		t.Fatalf("non-canonical record name affected list: %#v", requests)
	}
	if err = os.Remove(filepath.Join(directory, "02.json")); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "1.json"), []byte("{}"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(repository, 1); err == nil {
		t.Fatal("zero-ID persisted record was accepted")
	}
}

func TestMalformedRecordDoesNotBlockCreateOrReopen(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	first := testMergeRequest("First")
	if err := store.Create(repository, &first); err != nil {
		t.Fatal(err)
	}
	closedAt := time.Now().UTC()
	if _, err := store.Update(repository, first.ID, func(request *MergeRequest) error {
		request.State = StateClosed
		request.ClosedBy = "alice"
		request.ClosedAt = &closedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	malformedPath := filepath.Join(directory, "2.json")
	malformedContents := []byte("{broken")
	if err = os.WriteFile(malformedPath, malformedContents, 0o640); err != nil {
		t.Fatal(err)
	}

	second := testMergeRequest("Second")
	second.Source = "feature-two"
	if err = store.Create(repository, &second); err != nil {
		t.Fatalf("malformed record blocked create: %v", err)
	}
	if second.ID != 3 {
		t.Fatalf("new merge request ID = %d, want 3", second.ID)
	}
	remaining, err := os.ReadFile(malformedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(remaining) != string(malformedContents) {
		t.Fatalf("malformed record was overwritten: %q", remaining)
	}

	reopened, err := store.Update(repository, first.ID, func(request *MergeRequest) error {
		request.State = StateOpen
		request.ClosedBy = ""
		request.ClosedAt = nil
		return nil
	})
	if err != nil {
		t.Fatalf("malformed record blocked reopen: %v", err)
	}
	if reopened.State != StateOpen {
		t.Fatalf("merge request was not reopened: %#v", reopened)
	}
	requests, err := store.List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].ID != second.ID || requests[1].ID != first.ID {
		t.Fatalf("unexpected merge request list: %#v", requests)
	}
}

func TestStoreSerializesConcurrentCreates(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	const count = 20
	ids := make(chan uint64, count)
	errs := make(chan error, count)
	var wait sync.WaitGroup
	for index := 0; index < count; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			request := testMergeRequest(fmt.Sprintf("Request %d", index))
			request.Source = fmt.Sprintf("feature-%d", index)
			if err := store.Create(repository, &request); err != nil {
				errs <- err
				return
			}
			ids <- request.ID
		}(index)
	}
	wait.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	collected := make([]int, 0, count)
	for id := range ids {
		collected = append(collected, int(id))
	}
	sort.Ints(collected)
	for index, id := range collected {
		if id != index+1 {
			t.Fatalf("concurrent IDs = %#v", collected)
		}
	}
}

func TestStoreAtomicallyRejectsDuplicateOpenRequestsAcrossInstances(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	stores := []*Store{NewStore(root), NewStore(root)}
	errs := make(chan error, len(stores))
	var wait sync.WaitGroup
	for index, store := range stores {
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			request := testMergeRequest(fmt.Sprintf("Duplicate %d", index))
			errs <- store.Create(repository, &request)
		}(index, store)
	}
	wait.Wait()
	close(errs)

	var created, duplicate int
	for err := range errs {
		switch {
		case err == nil:
			created++
		case errors.Is(err, ErrDuplicate):
			duplicate++
		default:
			t.Fatal(err)
		}
	}
	if created != 1 || duplicate != 1 {
		t.Fatalf("created=%d duplicate=%d, want one of each", created, duplicate)
	}
	requests, err := NewStore(root).List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("duplicate requests were persisted: %#v", requests)
	}
}

func TestStoreRejectsReopeningDuplicateRequest(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	first := testMergeRequest("First")
	if err := store.Create(repository, &first); err != nil {
		t.Fatal(err)
	}
	closedAt := time.Now().UTC()
	if _, err := store.Update(repository, first.ID, func(request *MergeRequest) error {
		request.State = StateClosed
		request.ClosedBy = "alice"
		request.ClosedAt = &closedAt
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	second := testMergeRequest("Second")
	if err := store.Create(repository, &second); err != nil {
		t.Fatal(err)
	}

	_, err := store.Update(repository, first.ID, func(request *MergeRequest) error {
		request.State = StateOpen
		request.ClosedBy = ""
		request.ClosedAt = nil
		return nil
	})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("reopening duplicate request returned %v", err)
	}
	persisted, getErr := store.Get(repository, first.ID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if persisted.State != StateClosed {
		t.Fatalf("failed reopen changed persisted state: %#v", persisted)
	}
}

func TestStoreRelocatesAndRewritesRepository(t *testing.T) {
	root := t.TempDir()
	source := testRepository()
	destination := repopath.Repository{Groups: []string{"engineering"}, Name: "service"}
	createTestGitStore(t, root, source)
	store := NewStore(root)
	request := testMergeRequest("Move me")
	if err := store.Create(source, &request); err != nil {
		t.Fatal(err)
	}

	if err := store.Relocate(source, destination); err != nil {
		t.Fatal(err)
	}
	if sourceRequests, err := NewStore(root).List(source); err != nil ||
		len(sourceRequests) != 0 {
		t.Fatalf("source review store remains after relocation: %#v, %v", sourceRequests, err)
	}
	moved, err := NewStore(root).Get(destination, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Repository != destination.Full() || moved.Title != request.Title {
		t.Fatalf("unexpected relocated merge request: %#v", moved)
	}
}

func TestStoreRelocationFailurePreservesSource(t *testing.T) {
	root := t.TempDir()
	source := testRepository()
	destination := repopath.Repository{Groups: []string{"engineering"}, Name: "service"}
	createTestGitStore(t, root, source)
	store := NewStore(root)
	request := testMergeRequest("Do not lose me")
	if err := store.Create(source, &request); err != nil {
		t.Fatal(err)
	}
	destinationDirectory, err := repositoryDirectory(root, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(destinationDirectory, 0o750); err != nil {
		t.Fatal(err)
	}

	if err = store.Relocate(source, destination); err == nil {
		t.Fatal("relocation to an existing store succeeded")
	}
	persisted, err := NewStore(root).Get(source, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Repository != source.Full() {
		t.Fatalf("failed relocation rewrote the source: %#v", persisted)
	}
}

func TestStoreRejectsOversizedRecordBeforeWriting(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	request := testMergeRequest("Too large")
	request.Description = strings.Repeat("x", maximumRecordBytes)

	if err := store.Create(repository, &request); err == nil {
		t.Fatal("oversized merge request was persisted")
	}
	requests, err := store.List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 0 {
		t.Fatalf("oversized record remains in the store: %#v", requests)
	}
}
