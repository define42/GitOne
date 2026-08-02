package issue

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

func testRepository() repopath.Repository {
	return repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
}

func testIssue(title string) Issue {
	return Issue{
		Title:       title,
		Description: "Something is broken",
		Author:      "alice",
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

func TestStoreCreatesListsAndUpdatesIssues(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)

	nonUTC := time.FixedZone("test", 2*60*60)
	createdAt := time.Date(2026, 7, 28, 10, 0, 0, 0, nonUTC)
	first := testIssue("First")
	first.CreatedAt = createdAt
	first.UpdatedAt = createdAt
	if err := store.Create(repository, &first); err != nil {
		t.Fatal(err)
	}
	if first.ID != 1 || first.Repository != repository.Full() || first.State != StateOpen {
		t.Fatalf("unexpected created issue: %#v", first)
	}
	if first.CreatedAt.Location() != time.UTC || first.UpdatedAt.Location() != time.UTC {
		t.Fatalf("timestamps were not normalized to UTC: %#v", first)
	}
	if first.Labels == nil || first.Assignees == nil || first.Comments == nil {
		t.Fatalf("arrays were not normalized: %#v", first)
	}

	second := testIssue("Second")
	if err := store.Create(repository, &second); err != nil {
		t.Fatal(err)
	}
	if second.ID != 2 {
		t.Fatalf("second issue ID = %d, want 2", second.ID)
	}

	records, err := NewStore(root).List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[0].ID != 2 || records[1].ID != 1 {
		t.Fatalf("unexpected list order: %#v", records)
	}

	updated, err := store.Update(repository, 1, func(record *Issue) error {
		now := time.Now().UTC()
		record.State = StateClosed
		record.ClosedBy = "bob"
		record.ClosedAt = &now
		record.Labels = []string{"bug"}
		record.Assignees = []string{"carol"}
		record.Comments = append(record.Comments, Comment{
			ID:        1,
			Author:    "bob",
			Body:      "Reproduced",
			CreatedAt: now,
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.State != StateClosed || updated.ClosedBy != "bob" ||
		len(updated.Comments) != 1 || len(updated.Labels) != 1 {
		t.Fatalf("unexpected updated issue: %#v", updated)
	}

	stored, err := store.Get(repository, 1)
	if err != nil {
		t.Fatal(err)
	}
	if stored.State != StateClosed || len(stored.Comments) != 1 {
		t.Fatalf("update was not persisted: %#v", stored)
	}
}

func TestStoreRejectsMissingRepositoryAndUnknownIssue(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	store := NewStore(root)

	record := testIssue("Missing repository")
	if err := store.Create(repository, &record); err == nil {
		t.Fatal("expected an error when the Git store is missing")
	}

	createTestGitStore(t, root, repository)
	if _, err := store.Get(repository, 7); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
	if _, err := store.Update(repository, 7, func(*Issue) error { return nil }); !errors.Is(
		err,
		ErrNotFound,
	) {
		t.Fatalf("Update error = %v, want ErrNotFound", err)
	}
	if _, err := store.Get(repository, 0); err == nil {
		t.Fatal("expected an error for a zero issue ID")
	}
	if err := store.Create(repository, nil); err == nil {
		t.Fatal("expected an error for a nil issue")
	}
}

func TestStoreValidatesIssueContent(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)

	cases := map[string]func(*Issue){
		"empty title":      func(record *Issue) { record.Title = "  " },
		"long title":       func(record *Issue) { record.Title = strings.Repeat("t", MaximumTitleBytes+1) },
		"long description": func(record *Issue) { record.Description = strings.Repeat("d", MaximumBodyBytes+1) },
		"missing author":   func(record *Issue) { record.Author = "" },
		"invalid state":    func(record *Issue) { record.State = State("wontfix") },
		"closure metadata": func(record *Issue) { record.ClosedBy = "bob" },
		"duplicate label":  func(record *Issue) { record.Labels = []string{"bug", "bug"} },
		"untrimmed label":  func(record *Issue) { record.Labels = []string{" bug"} },
		"long label": func(record *Issue) {
			record.Labels = []string{strings.Repeat("l", MaximumLabelBytes+1)}
		},
		"too many labels": func(record *Issue) {
			labels := make([]string, 0, MaximumLabels+1)
			for index := range MaximumLabels + 1 {
				labels = append(labels, strings.Repeat("l", index+1))
			}
			record.Labels = labels
		},
		"duplicate assignee": func(record *Issue) {
			record.Assignees = []string{"alice", "alice"}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			record := testIssue("Valid")
			mutate(&record)
			if err := store.Create(repository, &record); err == nil {
				t.Fatalf("expected a validation error for %s", name)
			}
		})
	}
}

func TestStoreValidatesComments(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	record := testIssue("Comments")
	if err := store.Create(repository, &record); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	cases := map[string][]Comment{
		"zero id":   {{ID: 0, Author: "a", Body: "b", CreatedAt: now}},
		"duplicate": {{ID: 1, Author: "a", Body: "b", CreatedAt: now}, {ID: 1, Author: "a", Body: "c", CreatedAt: now}},
		"empty body": {
			{ID: 1, Author: "a", Body: "  ", CreatedAt: now},
		},
		"long body": {
			{ID: 1, Author: "a", Body: strings.Repeat("b", MaximumBodyBytes+1), CreatedAt: now},
		},
		"missing time": {{ID: 1, Author: "a", Body: "b"}},
	}
	for name, comments := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := store.Update(repository, record.ID, func(stored *Issue) error {
				stored.Comments = comments
				return nil
			}); err == nil {
				t.Fatalf("expected a validation error for %s", name)
			}
		})
	}
}

func TestStoreSkipsInvalidRecordsAndAllocatesGaps(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	record := testIssue("First")
	if err := store.Create(repository, &record); err != nil {
		t.Fatal(err)
	}
	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "4.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "notanid.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "ignored.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	records, err := store.List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != 1 {
		t.Fatalf("invalid records were not skipped: %#v", records)
	}

	next := testIssue("Next")
	if err = store.Create(repository, &next); err != nil {
		t.Fatal(err)
	}
	if next.ID != 2 {
		t.Fatalf("next issue ID = %d, want 2", next.ID)
	}
	if err = os.WriteFile(filepath.Join(directory, "3.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	gap := testIssue("Gap")
	if err = store.Create(repository, &gap); err != nil {
		t.Fatal(err)
	}
	if gap.ID != 5 {
		t.Fatalf("next issue ID = %d, want 5 to skip the occupied records", gap.ID)
	}
}

func TestStoreRejectsOversizedRecords(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	record := testIssue("First")
	if err := store.Create(repository, &record); err != nil {
		t.Fatal(err)
	}
	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	oversized := make([]byte, maximumRecordBytes+1)
	for index := range oversized {
		oversized[index] = ' '
	}
	if err = os.WriteFile(filepath.Join(directory, "1.json"), oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := store.List(repository)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("oversized record was not skipped: %#v", records)
	}
	if _, err = store.Get(repository, 1); err == nil {
		t.Fatal("expected an error for an oversized record")
	}
}

func TestStoreRejectsInvalidRepositoryPaths(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	for name, repository := range map[string]repopath.Repository{
		"traversal group": {Groups: []string{".."}, Name: "api"},
		"traversal name":  {Groups: []string{"engineering"}, Name: ".."},
		"empty name":      {Groups: []string{"engineering"}, Name: ""},
		"empty group":     {Groups: []string{""}, Name: "api"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.List(repository); err == nil {
				t.Fatal("expected an error for an invalid repository")
			}
		})
	}
}

func TestStoreRelocatesRecords(t *testing.T) {
	root := t.TempDir()
	source := testRepository()
	destination := repopath.Repository{Groups: []string{"engineering"}, Name: "gateway"}
	createTestGitStore(t, root, source)
	createTestGitStore(t, root, destination)
	store := NewStore(root)
	record := testIssue("Relocate")
	if err := store.Create(source, &record); err != nil {
		t.Fatal(err)
	}

	if err := store.Relocate(source, destination); err != nil {
		t.Fatal(err)
	}
	moved, err := store.Get(destination, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if moved.Repository != destination.Full() {
		t.Fatalf("relocated issue repository = %q", moved.Repository)
	}
	sourceDirectory, err := repositoryDirectory(root, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(sourceDirectory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source issue store still exists: %v", err)
	}

	// Relocating an absent store is a no-op, and an occupied destination fails.
	absent := repopath.Repository{Groups: []string{"engineering"}, Name: "absent"}
	if err = store.Relocate(source, absent); err != nil {
		t.Fatal(err)
	}
	other := repopath.Repository{Groups: []string{"engineering"}, Name: "edge"}
	createTestGitStore(t, root, other)
	otherRecord := testIssue("Other")
	if err = store.Create(other, &otherRecord); err != nil {
		t.Fatal(err)
	}
	if err = store.Relocate(other, destination); err == nil {
		t.Fatal("expected an error when the destination issue store exists")
	}
}

func TestStoreRewritesGroupRecords(t *testing.T) {
	root := t.TempDir()
	source := repopath.Repository{Groups: []string{"engineering", "backend"}, Name: "api"}
	createTestGitStore(t, root, source)
	store := NewStore(root)
	record := testIssue("Group move")
	if err := store.Create(source, &record); err != nil {
		t.Fatal(err)
	}

	if err := os.Rename(
		filepath.Join(root, "engineering"),
		filepath.Join(root, "platform"),
	); err != nil {
		t.Fatal(err)
	}
	if err := store.RewriteGroup("engineering", "platform"); err != nil {
		t.Fatal(err)
	}
	moved := repopath.Repository{Groups: []string{"platform", "backend"}, Name: "api"}
	rewritten, err := store.Get(moved, record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rewritten.Repository != moved.Full() {
		t.Fatalf("rewritten issue repository = %q", rewritten.Repository)
	}

	if err = store.RewriteGroup("absent", "missing"); err != nil {
		t.Fatalf("rewriting an absent group = %v, want nil", err)
	}
}

func TestMoveRepositoryLockedRollsBackOnRelocationFailure(t *testing.T) {
	root := t.TempDir()
	source := testRepository()
	destination := repopath.Repository{Groups: []string{"engineering"}, Name: "gateway"}
	createTestGitStore(t, root, source)
	createTestGitStore(t, root, destination)
	store := NewStore(root)
	sourceRecord := testIssue("Source")
	if err := store.Create(source, &sourceRecord); err != nil {
		t.Fatal(err)
	}
	destinationRecord := testIssue("Destination")
	if err := store.Create(destination, &destinationRecord); err != nil {
		t.Fatal(err)
	}

	rolledBack := false
	err := store.MoveRepositoryLocked(
		source,
		destination,
		func() error { return nil },
		func() error {
			rolledBack = true
			return nil
		},
	)
	if err == nil {
		t.Fatal("expected the relocation to fail")
	}
	if !rolledBack {
		t.Fatal("rollback was not invoked")
	}
	if _, getErr := store.Get(source, sourceRecord.ID); getErr != nil {
		t.Fatalf("source issue was not preserved: %v", getErr)
	}
}

func TestMoveGroupLockedRewritesAndRollsBack(t *testing.T) {
	root := t.TempDir()
	source := repopath.Repository{Groups: []string{"engineering"}, Name: "api"}
	createTestGitStore(t, root, source)
	store := NewStore(root)
	record := testIssue("Group")
	if err := store.Create(source, &record); err != nil {
		t.Fatal(err)
	}

	if err := store.MoveGroupLocked(
		"engineering",
		"platform",
		func() error {
			return os.Rename(
				filepath.Join(root, "engineering"),
				filepath.Join(root, "platform"),
			)
		},
		func() error {
			return os.Rename(
				filepath.Join(root, "platform"),
				filepath.Join(root, "engineering"),
			)
		},
	); err != nil {
		t.Fatal(err)
	}
	moved := repopath.Repository{Groups: []string{"platform"}, Name: "api"}
	if _, err := store.Get(moved, record.ID); err != nil {
		t.Fatal(err)
	}

	if err := store.MoveGroupLocked(
		"platform",
		"broken",
		func() error { return errors.New("move failed") },
		func() error { return nil },
	); err == nil {
		t.Fatal("expected the move failure to be returned")
	}
}

func TestLifecycleActionsRequireCallbacks(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.WithRepositoryLocks(nil, nil); err == nil {
		t.Fatal("expected an error for a nil repository action")
	}
	if err := store.WithGroupLocks(nil, nil); err == nil {
		t.Fatal("expected an error for a nil group action")
	}
	if err := store.MoveRepositoryLocked(
		testRepository(),
		testRepository(),
		nil,
		nil,
	); err == nil {
		t.Fatal("expected an error for nil move and rollback actions")
	}
	if err := store.MoveGroupLocked("a", "b", nil, nil); err == nil {
		t.Fatal("expected an error for nil move and rollback actions")
	}
}
