package issue

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

func TestInvalidRecordErrorReportsItsCause(t *testing.T) {
	cause := errors.New("broken record")
	err := &invalidRecordError{cause: cause}
	if err.Error() != cause.Error() {
		t.Fatalf("Error() = %q, want %q", err.Error(), cause.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("the cause was not unwrapped")
	}
}

func TestRecordHelpersRejectInvalidInput(t *testing.T) {
	directory := t.TempDir()
	if _, err := recordPath(directory, 0); err == nil {
		t.Fatal("expected an error for a zero issue ID")
	}
	if err := writeRecord(directory, Issue{}); err == nil {
		t.Fatal("expected an error when writing a record without an ID")
	}

	oversized := Issue{
		ID:          1,
		Repository:  testRepository().Full(),
		Title:       "Oversized",
		Description: strings.Repeat("d", maximumRecordBytes),
		Author:      "alice",
	}
	if err := writeRecord(directory, oversized); err == nil {
		t.Fatal("expected an error for an oversized record")
	}
	if err := writeRecord(
		filepath.Join(directory, "absent"),
		Issue{ID: 1, Title: "Missing directory", Author: "alice"},
	); err == nil {
		t.Fatal("expected an error when the record directory is missing")
	}

	if _, err := readRecord(directory, testRepository(), 0); err == nil {
		t.Fatal("expected an error when reading a zero issue ID")
	}
	if err := os.Mkdir(filepath.Join(directory, "9.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecord(directory, testRepository(), 9); err == nil {
		t.Fatal("expected an error when the record is not a regular file")
	}
	if _, _, err := scanDirectory(directory, testRepository(), false); err == nil {
		t.Fatal("expected an error when scanning an irregular record")
	}
	records, _, err := scanDirectory(directory, testRepository(), true)
	if err != nil || len(records) != 0 {
		t.Fatalf("scanDirectory(skipInvalid) = %#v, %v", records, err)
	}
}

func TestScanDirectoryReportsInvalidRecords(t *testing.T) {
	directory := t.TempDir()
	repository := testRepository()
	if err := os.WriteFile(
		filepath.Join(directory, "notanid.json"),
		[]byte("{}"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := listDirectory(directory, repository); err == nil {
		t.Fatal("expected an error for an unparsable record name")
	}
	if err := os.Remove(filepath.Join(directory, "notanid.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "1.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listDirectory(directory, repository); err == nil {
		t.Fatal("expected an error for an unreadable record")
	}
}

func TestReadRecordRejectsTrailingDocuments(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	record := testIssue("Trailing")
	if err := store.Create(repository, &record); err != nil {
		t.Fatal(err)
	}
	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "1.json")
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	contents = append(contents, []byte("{}\n")...)
	if err = os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(repository, 1); err == nil {
		t.Fatal("expected an error for a record with trailing content")
	}
	records, err := store.List(repository)
	if err != nil || len(records) != 0 {
		t.Fatalf("List = %#v, %v, want no records", records, err)
	}
}

func TestStoreRejectsUnusableDirectories(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)

	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(directory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := testIssue("Blocked")
	if err = store.Create(repository, &record); err == nil {
		t.Fatal("expected an error when the issue store is not a directory")
	}
	if err = store.save(repository, Issue{ID: 1}); err == nil {
		t.Fatal("expected an error when saving into a blocked issue store")
	}
}

func TestStoreRejectsInvalidRepositoryForEveryOperation(t *testing.T) {
	store := NewStore(t.TempDir())
	invalid := repopath.Repository{Groups: []string{".."}, Name: "api"}

	if _, err := store.get(invalid, 1); err == nil {
		t.Fatal("expected an error for an invalid repository")
	}
	if err := store.save(invalid, Issue{ID: 1}); err == nil {
		t.Fatal("expected an error for an invalid repository")
	}
	if _, err := repositoryGitDirectory(store.Root, invalid); err == nil {
		t.Fatal("expected an error for an invalid repository")
	}
	record := testIssue("Invalid repository")
	if err := store.Create(invalid, &record); err == nil {
		t.Fatal("expected an error for an invalid repository")
	}
	if err := store.Relocate(testRepository(), invalid); err == nil {
		t.Fatal("expected an error for an invalid destination repository")
	}
}

func TestStoreRejectsGitStoreThatIsNotADirectory(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	path, err := repositoryGitDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	record := testIssue("Not a directory")
	if err = NewStore(root).Create(repository, &record); err == nil {
		t.Fatal("expected an error when the Git store is not a directory")
	}
}

func TestUpdateRejectsInvalidArguments(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Update(testRepository(), 0, func(*Issue) error { return nil }); err == nil {
		t.Fatal("expected an error for a zero issue ID")
	}
	if _, err := store.Update(testRepository(), 1, nil); err == nil {
		t.Fatal("expected an error for a nil update function")
	}
}

func TestRewriteGroupRejectsInvalidGroups(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.RewriteGroup("..", "platform"); err == nil {
		t.Fatal("expected an error for an invalid source group")
	}
	if err := store.RewriteGroup("engineering", ".."); err == nil {
		t.Fatal("expected an error for an invalid destination group")
	}
}

func TestValidateRejectsMalformedRecords(t *testing.T) {
	repository := testRepository()
	now := time.Now().UTC()
	valid := func() Issue {
		return Issue{
			ID:         1,
			Repository: repository.Full(),
			Title:      "Valid",
			Author:     "alice",
			State:      StateOpen,
			CreatedAt:  now,
			UpdatedAt:  now,
			Labels:     []string{},
			Assignees:  []string{},
			Comments:   []Comment{},
		}
	}
	cases := map[string]func(*Issue){
		"zero id":            func(record *Issue) { record.ID = 0 },
		"mismatched id":      func(record *Issue) { record.ID = 2 },
		"other repository":   func(record *Issue) { record.Repository = "engineering/other" },
		"missing timestamps": func(record *Issue) { record.UpdatedAt = time.Time{} },
		"reordered times": func(record *Issue) {
			record.CreatedAt = now.Add(time.Hour)
		},
		"nil labels":    func(record *Issue) { record.Labels = nil },
		"nil assignees": func(record *Issue) { record.Assignees = nil },
		"nil comments":  func(record *Issue) { record.Comments = nil },
		"closed without metadata": func(record *Issue) {
			record.State = StateClosed
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			record := valid()
			mutate(&record)
			if err := validate(repository, 1, record); err == nil {
				t.Fatalf("expected a validation error for %s", name)
			}
		})
	}
	if err := validate(repository, 0, valid()); err == nil {
		t.Fatal("expected an error for a zero expected ID")
	}
}

func TestMoveActionsJoinRollbackFailures(t *testing.T) {
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

	rollbackErr := errors.New("rollback failed")
	moveErr := errors.New("move failed")
	err := store.MoveRepositoryLocked(
		source,
		destination,
		func() error { return moveErr },
		func() error { return rollbackErr },
	)
	if !errors.Is(err, moveErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("MoveRepositoryLocked error = %v, want both failures", err)
	}

	err = store.MoveRepositoryLocked(
		source,
		destination,
		func() error { return nil },
		func() error { return rollbackErr },
	)
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("MoveRepositoryLocked error = %v, want the rollback failure", err)
	}

	err = store.MoveGroupLocked(
		"engineering",
		"..",
		func() error { return nil },
		func() error { return rollbackErr },
	)
	if !errors.Is(err, rollbackErr) {
		t.Fatalf("MoveGroupLocked error = %v, want the rollback failure", err)
	}
	err = store.MoveGroupLocked(
		"engineering",
		"..",
		func() error { return nil },
		func() error { return nil },
	)
	if err == nil {
		t.Fatal("expected the rewrite failure to be returned")
	}
}

func TestRelocateHandlesEdgeCases(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	record := testIssue("Relocate")
	if err := store.Create(repository, &record); err != nil {
		t.Fatal(err)
	}

	if err := store.Relocate(repository, repository); err != nil {
		t.Fatalf("relocating onto itself = %v, want nil", err)
	}
	invalid := repopath.Repository{Groups: []string{".."}, Name: "api"}
	if err := store.Relocate(invalid, repository); err == nil {
		t.Fatal("expected an error for an invalid source repository")
	}

	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(directory, "2.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	destination := repopath.Repository{Groups: []string{"engineering"}, Name: "gateway"}
	if err = store.Relocate(repository, destination); err == nil {
		t.Fatal("expected an error when a record is not a regular file")
	}
}

func TestGetRejectsRecordsFromAnotherRepository(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	record := testIssue("Foreign")
	if err := store.Create(repository, &record); err != nil {
		t.Fatal(err)
	}
	directory, err := repositoryDirectory(root, repository)
	if err != nil {
		t.Fatal(err)
	}
	record.Repository = "engineering/other"
	contents, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, "1.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Get(repository, 1); err == nil {
		t.Fatal("expected an error for a record owned by another repository")
	}
}

func TestNextRecordIDReportsAnExhaustedIDSpace(t *testing.T) {
	root := t.TempDir()
	repository := testRepository()
	createTestGitStore(t, root, repository)
	store := NewStore(root)
	if _, err := store.nextRecordID(repository, math.MaxUint64); err == nil {
		t.Fatal("expected an error when the issue ID space is exhausted")
	}
}

func TestRewriteGroupRejectsUnnamedStores(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "platform", ".issues"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := NewStore(root).RewriteGroup("engineering", "platform"); err == nil {
		t.Fatal("expected an error for an unnamed issue store")
	}
}

func TestRewriteGroupRollsBackCompletedStores(t *testing.T) {
	root := t.TempDir()
	store := NewStore(root)
	first := repopath.Repository{Groups: []string{"engineering"}, Name: "alpha"}
	second := repopath.Repository{Groups: []string{"engineering"}, Name: "beta"}
	for _, repository := range []repopath.Repository{first, second} {
		createTestGitStore(t, root, repository)
		record := testIssue("Group rewrite")
		if err := store.Create(repository, &record); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := repositoryDirectory(root, second)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(directory, "2.json"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(
		filepath.Join(root, "engineering"),
		filepath.Join(root, "platform"),
	); err != nil {
		t.Fatal(err)
	}

	if err = store.RewriteGroup("engineering", "platform"); err == nil {
		t.Fatal("expected the group rewrite to fail")
	}
	restored := repopath.Repository{Groups: []string{"engineering"}, Name: "alpha"}
	moved := repopath.Repository{Groups: []string{"platform"}, Name: "alpha"}
	rolledBack, err := listDirectory(filepath.Join(root, "platform", "alpha.issues"), restored)
	if err != nil {
		t.Fatalf("the completed store was not restored: %v", err)
	}
	if len(rolledBack) != 1 || rolledBack[0].Repository == moved.Full() {
		t.Fatalf("unexpected restored records: %#v", rolledBack)
	}
}

func TestRelocateReportsUnreadableStorePaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "engineering"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "engineering", "nested"),
		[]byte("not a directory"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	store := NewStore(root)
	nested := repopath.Repository{Groups: []string{"engineering", "nested"}, Name: "api"}
	destination := repopath.Repository{Groups: []string{"engineering"}, Name: "gateway"}

	if err := store.Relocate(testRepository(), nested); err == nil {
		t.Fatal("expected an error when the destination path is unreadable")
	}
	if err := store.Relocate(nested, destination); err == nil {
		t.Fatal("expected an error when the source path is unreadable")
	}
}
