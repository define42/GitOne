package review

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

func validPersistedRequest(repository repopath.Repository) MergeRequest {
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	return MergeRequest{
		ID:                  1,
		Repository:          repository.Full(),
		Title:               "Review",
		Description:         "Review this change",
		Target:              "main",
		Source:              "feature",
		Author:              "alice",
		State:               StateOpen,
		CreatedAt:           now,
		UpdatedAt:           now,
		BaseCommit:          strings.Repeat("1", 64),
		HeadCommit:          strings.Repeat("2", 64),
		RequiredApprovals:   1,
		Approvals:           []Approval{},
		Threads:             []Thread{},
		MergedCommit:        "",
		MergedStrategy:      "",
		MergedBy:            "",
		MergedAt:            nil,
		ClosedBy:            "",
		ClosedAt:            nil,
		MergeInProgress:     false,
		MergeClaimID:        "",
		MergeOwnerID:        "",
		MergeHeadCommit:     "",
		MergeTargetCommit:   "",
		MergeResultCommit:   "",
		MergeResultStrategy: "",
		MergeStartedBy:      "",
		MergeStartedAt:      nil,
	}
}

func TestValidateRejectsMalformedMergeRequests(t *testing.T) {
	repository := testRepository()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	hash := strings.Repeat("3", 64)
	tests := []struct {
		name   string
		mutate func(*MergeRequest)
	}{
		{"repository mismatch", func(request *MergeRequest) {
			request.Repository = "engineering/other"
		}},
		{"blank title", func(request *MergeRequest) { request.Title = " " }},
		{"missing target", func(request *MergeRequest) { request.Target = "" }},
		{"same branches", func(request *MergeRequest) { request.Source = request.Target }},
		{"blank author", func(request *MergeRequest) { request.Author = " " }},
		{"invalid state", func(request *MergeRequest) { request.State = State("pending") }},
		{"missing timestamp", func(request *MergeRequest) { request.CreatedAt = time.Time{} }},
		{"time moves backwards", func(request *MergeRequest) {
			request.UpdatedAt = request.CreatedAt.Add(-time.Second)
		}},
		{"missing commit", func(request *MergeRequest) { request.BaseCommit = "" }},
		{"invalid commit", func(request *MergeRequest) { request.HeadCommit = "not-a-hash" }},
		{"legacy SHA-1 commit", func(request *MergeRequest) {
			request.HeadCommit = strings.Repeat("a", 40)
		}},
		{"invalid approval requirement", func(request *MergeRequest) {
			request.RequiredApprovals = 0
		}},
		{"nil approvals", func(request *MergeRequest) { request.Approvals = nil }},
		{"malformed approval", func(request *MergeRequest) {
			request.Approvals = []Approval{{Author: "bob"}}
		}},
		{"approval by author", func(request *MergeRequest) {
			request.Approvals = []Approval{{
				Author:     request.Author,
				HeadCommit: request.HeadCommit,
				CreatedAt:  now,
			}}
		}},
		{"invalid self-approval override", func(request *MergeRequest) {
			request.Approvals = []Approval{{
				Author:       "bob",
				HeadCommit:   request.HeadCommit,
				CreatedAt:    now,
				SelfApproval: true,
			}}
		}},
		{"duplicate approval", func(request *MergeRequest) {
			approval := Approval{Author: "bob", HeadCommit: request.HeadCommit, CreatedAt: now}
			request.Approvals = []Approval{approval, approval}
		}},
		{"zero thread ID", func(request *MergeRequest) {
			request.Threads = []Thread{{CreatedAt: now, Comments: []Comment{}}}
		}},
		{"duplicate thread ID", func(request *MergeRequest) {
			thread := Thread{ID: 1, CreatedAt: now, Comments: []Comment{}}
			request.Threads = []Thread{thread, thread}
		}},
		{"malformed thread", func(request *MergeRequest) {
			request.Threads = []Thread{{ID: 1, Comments: []Comment{}}}
		}},
		{"resolved thread missing metadata", func(request *MergeRequest) {
			request.Threads = []Thread{{
				ID: 1, CreatedAt: now, Comments: []Comment{}, Resolved: true,
			}}
		}},
		{"unresolved thread with metadata", func(request *MergeRequest) {
			request.Threads = []Thread{{
				ID: 1, CreatedAt: now, Comments: []Comment{}, ResolvedBy: "bob",
			}}
		}},
		{"zero comment ID", func(request *MergeRequest) {
			request.Threads = []Thread{{
				ID: 1, CreatedAt: now, Comments: []Comment{{
					Author: "bob", Body: "comment", CreatedAt: now,
				}},
			}}
		}},
		{"duplicate comment ID", func(request *MergeRequest) {
			comment := Comment{ID: 1, Author: "bob", Body: "comment", CreatedAt: now}
			request.Threads = []Thread{{
				ID: 1, CreatedAt: now, Comments: []Comment{comment, comment},
			}}
		}},
		{"malformed comment", func(request *MergeRequest) {
			request.Threads = []Thread{{
				ID: 1, CreatedAt: now, Comments: []Comment{{ID: 1}},
			}}
		}},
		{"open with completion", func(request *MergeRequest) { request.ClosedBy = "alice" }},
		{"closed without metadata", func(request *MergeRequest) { request.State = StateClosed }},
		{"closed with merge metadata", func(request *MergeRequest) {
			request.State = StateClosed
			request.ClosedBy = "alice"
			request.ClosedAt = &now
			request.MergedCommit = hash
		}},
		{"merged without metadata", func(request *MergeRequest) { request.State = StateMerged }},
		{"merged with closure metadata", func(request *MergeRequest) {
			request.State = StateMerged
			request.MergedCommit = hash
			request.MergedStrategy = "merge-commit"
			request.MergedBy = "alice"
			request.MergedAt = &now
			request.ClosedBy = "alice"
		}},
		{"incomplete merge claim", func(request *MergeRequest) {
			request.MergeInProgress = true
		}},
		{"incomplete merge result", func(request *MergeRequest) {
			setMergeClaim(request, now)
			request.MergeTargetCommit = hash
		}},
		{"malformed merge result hash", func(request *MergeRequest) {
			setMergeClaim(request, now)
			request.MergeTargetCommit = "invalid"
			request.MergeResultCommit = hash
			request.MergeResultStrategy = "fast-forward"
		}},
		{"invalid merge result strategy", func(request *MergeRequest) {
			setMergeClaim(request, now)
			request.MergeTargetCommit = hash
			request.MergeResultCommit = hash
			request.MergeResultStrategy = "squash"
		}},
		{"inactive merge intent", func(request *MergeRequest) {
			request.MergeClaimID = "claim"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validPersistedRequest(repository)
			test.mutate(&request)
			if err := validate(repository, request.ID, request); err == nil {
				t.Fatal("malformed merge request was accepted")
			}
		})
	}
}

func TestValidateAcceptsCompleteMergeIntent(t *testing.T) {
	repository := testRepository()
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	request := validPersistedRequest(repository)
	setMergeClaim(&request, now)
	request.MergeTargetCommit = strings.Repeat("3", 64)
	request.MergeResultCommit = strings.Repeat("4", 64)
	request.MergeResultStrategy = "merge-commit"
	if err := validate(repository, request.ID, request); err != nil {
		t.Fatal(err)
	}
}

func setMergeClaim(request *MergeRequest, now time.Time) {
	request.MergeInProgress = true
	request.MergeClaimID = "claim"
	request.MergeOwnerID = "worker"
	request.MergeHeadCommit = strings.Repeat("5", 64)
	request.MergeStartedBy = "alice"
	request.MergeStartedAt = &now
}

func TestNormalizeAllOptionalTimestampsAndCollections(t *testing.T) {
	zone := time.FixedZone("test", 2*60*60)
	value := time.Date(2026, 7, 29, 14, 0, 0, 0, zone)
	request := MergeRequest{
		CreatedAt:      value,
		UpdatedAt:      value,
		MergedAt:       &value,
		ClosedAt:       &value,
		MergeStartedAt: &value,
		Approvals:      []Approval{{CreatedAt: value}},
		Threads: []Thread{{
			ID:         1,
			CreatedAt:  value,
			ResolvedAt: &value,
		}},
	}
	normalize(&request)
	for name, timestamp := range map[string]time.Time{
		"created":       request.CreatedAt,
		"updated":       request.UpdatedAt,
		"merged":        *request.MergedAt,
		"closed":        *request.ClosedAt,
		"merge started": *request.MergeStartedAt,
		"approval":      request.Approvals[0].CreatedAt,
		"thread":        request.Threads[0].CreatedAt,
		"resolved":      *request.Threads[0].ResolvedAt,
	} {
		if timestamp.Location() != time.UTC {
			t.Fatalf("%s timestamp was not normalized: %v", name, timestamp)
		}
	}
	if request.Threads[0].Comments == nil {
		t.Fatal("nil comments were not normalized")
	}
}

func TestStoreAdditionalCreateAndUpdateErrors(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	invalid := repopath.Repository{Groups: []string{".."}, Name: "api"}
	request := testMergeRequest("Invalid")
	if err := store.Create(invalid, &request); err == nil {
		t.Fatal("create accepted an invalid repository")
	}

	repository := testRepository()
	gitDirectory, err := repositoryGitDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(gitDirectory), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(gitDirectory, []byte("not a directory"), 0o640); err != nil {
		t.Fatal(err)
	}
	request = testMergeRequest("File repository")
	if err = store.Create(repository, &request); err == nil {
		t.Fatal("create accepted a Git store that is not a directory")
	}

	if err = os.Remove(gitDirectory); err != nil {
		t.Fatal(err)
	}
	createTestGitStore(t, root, repository)
	defaultApprovals := testMergeRequest("Default approvals")
	defaultApprovals.RequiredApprovals = 0
	if err = store.Create(repository, &defaultApprovals); err != nil {
		t.Fatal(err)
	}
	if defaultApprovals.RequiredApprovals != 1 {
		t.Fatalf("required approvals = %d", defaultApprovals.RequiredApprovals)
	}
	badRepository := testMergeRequest("Wrong repository")
	badRepository.Source = "other-feature"
	badRepository.Repository = "engineering/other"
	if err = store.Create(repository, &badRepository); err == nil {
		t.Fatal("create accepted mismatched repository metadata")
	}

	if _, err = store.Update(repository, 0, func(*MergeRequest) error { return nil }); err == nil {
		t.Fatal("update accepted a zero ID")
	}
	if _, err = store.Update(repository, defaultApprovals.ID, nil); err == nil {
		t.Fatal("update accepted a nil callback")
	}
	if _, err = store.Update(repository, 99, func(*MergeRequest) error { return nil }); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing update error = %v", err)
	}

	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Update(
		repository,
		defaultApprovals.ID,
		func(*MergeRequest) error {
			if removeErr := os.RemoveAll(directory); removeErr != nil {
				return removeErr
			}
			return os.WriteFile(directory, []byte("blocked"), 0o640)
		},
	); err == nil {
		t.Fatal("update ignored a persistence failure")
	}
}

func TestStoreRecordFilesystemAndFormatErrors(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	repository := testRepository()
	invalid := repopath.Repository{Groups: []string{".."}, Name: "api"}

	if _, err := store.List(invalid); err == nil {
		t.Fatal("list accepted an invalid repository")
	}
	if _, err := store.Get(invalid, 1); err == nil {
		t.Fatal("get accepted an invalid repository")
	}
	if err := store.save(invalid, validPersistedRequest(repository)); err == nil {
		t.Fatal("save accepted an invalid repository")
	}
	if err := writeRecord(root, MergeRequest{}); err == nil {
		t.Fatal("write accepted a zero ID")
	}
	if _, err := readRecord(root, repository, 0); err == nil {
		t.Fatal("read accepted a zero ID")
	}
	if err := writeRecord(filepath.Join(root, "missing"), validPersistedRequest(repository)); err == nil {
		t.Fatal("write created a missing record directory")
	}

	directory := filepath.Join(root, "records")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "1.json"),
		make([]byte, maximumRecordBytes+1),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecord(directory, repository, 1); err == nil {
		t.Fatal("oversized record was accepted")
	}
	if err := os.Remove(filepath.Join(directory, "1.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(directory, "1.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecord(directory, repository, 1); err == nil {
		t.Fatal("directory record was accepted")
	}
	if err := os.RemoveAll(filepath.Join(directory, "1.json")); err != nil {
		t.Fatal(err)
	}
	valid := validPersistedRequest(repository)
	if err := writeRecord(directory, valid); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "1.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, []byte("{}\n")...)
	if err = os.WriteFile(path, contents, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = readRecord(directory, repository, 1); err == nil {
		t.Fatal("multiple JSON documents were accepted")
	}

	blocked := filepath.Join(root, "blocked.reviews")
	if err = os.WriteFile(blocked, []byte("blocked"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err = listDirectory(blocked, repository); err == nil {
		t.Fatal("list accepted a regular file as its directory")
	}
	symlinkDirectory := filepath.Join(root, "symlink-records")
	if err = os.MkdirAll(symlinkDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(
		path,
		filepath.Join(symlinkDirectory, "1.json"),
	); err != nil {
		t.Fatal(err)
	}
	if _, err = listDirectory(symlinkDirectory, repository); err == nil {
		t.Fatal("list accepted a symlink record")
	}
}

func TestStoreRejectsExhaustedMergeRequestIDs(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	repository := testRepository()
	createTestGitStore(t, root, repository)
	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	request := validPersistedRequest(repository)
	request.ID = math.MaxUint64
	if err = writeRecord(directory, request); err != nil {
		t.Fatal(err)
	}
	next := testMergeRequest("No IDs left")
	if err = store.Create(repository, &next); err == nil {
		t.Fatal("create accepted an exhausted ID space")
	}
}

func TestRewriteGroupNestedRepositoriesAndMissingDestination(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	sourceGroup := "engineering"
	destinationGroup := "platform"
	source := repopath.Repository{
		Groups: []string{sourceGroup, "backend"},
		Name:   "api",
	}
	createTestGitStore(t, root, source)
	request := testMergeRequest("Nested")
	if err := store.Create(source, &request); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(root, sourceGroup)
	destinationPath := filepath.Join(root, destinationGroup)
	if err := os.Rename(sourcePath, destinationPath); err != nil {
		t.Fatal(err)
	}
	if err := store.RewriteGroup(sourceGroup, destinationGroup); err != nil {
		t.Fatal(err)
	}
	destination := repopath.Repository{
		Groups: []string{destinationGroup, "backend"},
		Name:   "api",
	}
	persisted, err := store.Get(destination, request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Repository != destination.Full() {
		t.Fatalf("repository = %q, want %q", persisted.Repository, destination.Full())
	}

	if err = store.RewriteGroup("missing", "also-missing"); err != nil {
		t.Fatalf("missing destination rewrite: %v", err)
	}
	if err = store.RewriteGroup("../invalid", destinationGroup); err == nil {
		t.Fatal("rewrite accepted an invalid source group")
	}
	if err = store.RewriteGroup(sourceGroup, "../invalid"); err == nil {
		t.Fatal("rewrite accepted an invalid destination group")
	}
}

func TestRewriteGroupRollsBackCompletedReviewStores(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	sourceGroup := "engineering"
	destinationGroup := "platform"
	first := repopath.Repository{Groups: []string{sourceGroup}, Name: "a"}
	second := repopath.Repository{Groups: []string{sourceGroup}, Name: "z"}
	for _, repository := range []repopath.Repository{first, second} {
		createTestGitStore(t, root, repository)
		request := testMergeRequest("Rewrite " + repository.Name)
		request.Source += "-" + repository.Name
		if err := store.Create(repository, &request); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Rename(
		filepath.Join(root, sourceGroup),
		filepath.Join(root, destinationGroup),
	); err != nil {
		t.Fatal(err)
	}
	brokenDirectory := filepath.Join(root, destinationGroup, second.Name+".reviews")
	if err := os.WriteFile(
		filepath.Join(brokenDirectory, "1.json"),
		[]byte("{broken"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	if err := store.RewriteGroup(sourceGroup, destinationGroup); err == nil {
		t.Fatal("group rewrite accepted malformed review data")
	}

	firstDirectory := filepath.Join(root, destinationGroup, first.Name+".reviews")
	requests, err := listDirectory(firstDirectory, first)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Repository != first.Full() {
		t.Fatalf("completed rewrite was not rolled back: %#v", requests)
	}
}

func TestCreateReturnsRepositoryStatError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission behavior is required")
	}
	root := t.TempDir()
	repository := testRepository()
	group := filepath.Join(root, repository.Group())
	if err := os.MkdirAll(group, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(group, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(group, 0o750) })
	request := testMergeRequest("Unreadable")
	if err := NewStore(root).Create(repository, &request); err == nil {
		t.Skip("current user can stat files through mode 000")
	}
}
