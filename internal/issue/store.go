package issue

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

const maximumRecordBytes = 8 << 20

const storeSuffix = ".issues"

var ErrNotFound = errors.New("issue not found")

type invalidRecordError struct {
	cause error
}

func (e *invalidRecordError) Error() string {
	return e.cause.Error()
}

func (e *invalidRecordError) Unwrap() error {
	return e.cause
}

// Store persists repository issues as JSON records in a `<repo>.issues`
// directory beside the repository's `<repo>.git` directory.
type Store struct {
	Root string
}

func NewStore(root string) *Store {
	return &Store{Root: root}
}

func (s *Store) List(repository repopath.Repository) ([]Issue, error) {
	unlock, err := s.lockRepositories(repository)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = unlock()
	}()

	return s.list(repository)
}

func (s *Store) Get(repository repopath.Repository, id uint64) (Issue, error) {
	if id == 0 {
		return Issue{}, errors.New("issue ID must be greater than zero")
	}
	unlock, err := s.lockRepositories(repository)
	if err != nil {
		return Issue{}, err
	}
	defer func() {
		_ = unlock()
	}()

	return s.get(repository, id)
}

func (s *Store) Create(repository repopath.Repository, record *Issue) error {
	if record == nil {
		return errors.New("issue is required")
	}
	unlock, err := s.lockRepositories(repository)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()

	gitDirectory, err := repositoryGitDirectory(s.Root, repository)
	if err != nil {
		return err
	}
	info, err := os.Stat(gitDirectory)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("repository does not exist")
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("repository Git store is not a directory")
	}

	_, maximumID, err := s.listValid(repository)
	if err != nil {
		return err
	}
	nextID, err := s.nextRecordID(repository, maximumID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	record.ID = nextID
	if record.Repository == "" {
		record.Repository = repository.Full()
	}
	if record.State == "" {
		record.State = StateOpen
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.CreatedAt
	}
	normalize(record)
	if err = validate(repository, record.ID, *record); err != nil {
		return err
	}
	return s.save(repository, *record)
}

func (s *Store) Update(
	repository repopath.Repository,
	id uint64,
	update func(*Issue) error,
) (Issue, error) {
	if id == 0 {
		return Issue{}, errors.New("issue ID must be greater than zero")
	}
	if update == nil {
		return Issue{}, errors.New("issue update is required")
	}
	unlock, err := s.lockRepositories(repository)
	if err != nil {
		return Issue{}, err
	}
	defer func() {
		_ = unlock()
	}()

	record, err := s.get(repository, id)
	if err != nil {
		return Issue{}, err
	}
	if err = update(&record); err != nil {
		return Issue{}, err
	}
	record.UpdatedAt = time.Now().UTC()
	normalize(&record)
	if err = validate(repository, id, record); err != nil {
		return Issue{}, err
	}
	if err = s.save(repository, record); err != nil {
		return Issue{}, err
	}
	return record, nil
}

// Relocate moves a repository's issue records to a renamed repository.
func (s *Store) Relocate(
	repository repopath.Repository,
	destination repopath.Repository,
) error {
	unlock, err := s.lockRepositories(repository, destination)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()

	return s.relocate(repository, destination)
}

// RelocateLocked moves a repository's issue records while its caller already
// holds the issue repository locks for both paths.
func (s *Store) RelocateLocked(
	repository repopath.Repository,
	destination repopath.Repository,
) error {
	return s.relocate(repository, destination)
}

func (s *Store) relocate(
	repository repopath.Repository,
	destination repopath.Repository,
) error {
	sourceDirectory, err := repositoryDirectory(s.Root, repository)
	if err != nil {
		return err
	}
	destinationDirectory, err := repositoryDirectory(s.Root, destination)
	if err != nil {
		return err
	}
	if sourceDirectory == destinationDirectory {
		return nil
	}
	if _, err = os.Stat(destinationDirectory); err == nil {
		return errors.New("destination issue store exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = os.Stat(sourceDirectory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}

	records, err := listDirectory(sourceDirectory, repository)
	if err != nil {
		return err
	}
	parent := filepath.Dir(destinationDirectory)
	if err = os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	stagedDirectory, err := os.MkdirTemp(parent, ".issues-relocate-")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(stagedDirectory)
	}()
	if err = os.Chmod(stagedDirectory, 0o750); err != nil {
		return err
	}
	for _, record := range records {
		record.Repository = destination.Full()
		normalize(&record)
		if err = validate(destination, record.ID, record); err != nil {
			return err
		}
		if err = writeRecord(stagedDirectory, record); err != nil {
			return err
		}
	}

	backupDirectory := stagedDirectory + ".original"
	if err = os.Rename(sourceDirectory, backupDirectory); err != nil {
		return err
	}
	if err = os.Rename(stagedDirectory, destinationDirectory); err != nil {
		rollbackErr := os.Rename(backupDirectory, sourceDirectory)
		if rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore issue store: %w", rollbackErr))
		}
		return err
	}
	_ = os.RemoveAll(backupDirectory)
	return nil
}

// RewriteGroup rewrites the repository path recorded in every issue below a
// renamed group.
func (s *Store) RewriteGroup(sourceGroup, destinationGroup string) error {
	unlock, err := s.lockGroups(sourceGroup, destinationGroup)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()

	return s.rewriteGroup(sourceGroup, destinationGroup)
}

// RewriteGroupLocked rewrites group issue records while its caller already
// holds the issue group locks for both paths.
func (s *Store) RewriteGroupLocked(sourceGroup, destinationGroup string) error {
	return s.rewriteGroup(sourceGroup, destinationGroup)
}

func (s *Store) rewriteGroup(sourceGroup, destinationGroup string) error {
	sourceParts, err := repopath.ParseGroup(sourceGroup)
	if err != nil {
		return fmt.Errorf("invalid source group: %w", err)
	}
	destinationParts, err := repopath.ParseGroup(destinationGroup)
	if err != nil {
		return fmt.Errorf("invalid destination group: %w", err)
	}
	destinationDirectory, err := repopath.SafeJoin(s.Root, destinationParts...)
	if err != nil {
		return err
	}

	type groupRewrite struct {
		directory   string
		source      repopath.Repository
		destination repopath.Repository
	}
	rewrites := make([]groupRewrite, 0)
	err = filepath.WalkDir(
		destinationDirectory,
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if !entry.IsDir() || path == destinationDirectory {
				return nil
			}
			name := entry.Name()
			switch {
			case strings.HasSuffix(name, storeSuffix):
				relativeParent, relativeErr := filepath.Rel(
					destinationDirectory,
					filepath.Dir(path),
				)
				if relativeErr != nil {
					return relativeErr
				}
				suffix := []string{}
				if relativeParent != "." {
					suffix = strings.Split(filepath.ToSlash(relativeParent), "/")
				}
				repositoryName := strings.TrimSuffix(name, storeSuffix)
				source := repopath.Repository{
					Groups: append(append([]string(nil), sourceParts...), suffix...),
					Name:   repositoryName,
				}
				destination := repopath.Repository{
					Groups: append(
						append([]string(nil), destinationParts...),
						suffix...,
					),
					Name: repositoryName,
				}
				if validationErr := validateRepository(source); validationErr != nil {
					return validationErr
				}
				if validationErr := validateRepository(destination); validationErr != nil {
					return validationErr
				}
				rewrites = append(rewrites, groupRewrite{
					directory:   path,
					source:      source,
					destination: destination,
				})
				return fs.SkipDir
			case strings.HasSuffix(name, ".git"),
				strings.HasSuffix(name, ".lfs"),
				strings.HasSuffix(name, ".build"),
				strings.HasSuffix(name, ".reviews"):
				return fs.SkipDir
			default:
				return nil
			}
		},
	)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	sort.Slice(rewrites, func(left, right int) bool {
		return rewrites[left].directory < rewrites[right].directory
	})

	completed := 0
	for index, rewrite := range rewrites {
		if err = rewriteDirectory(
			rewrite.directory,
			rewrite.source,
			rewrite.destination,
		); err != nil {
			for rollbackIndex := completed - 1; rollbackIndex >= 0; rollbackIndex-- {
				rollback := rewrites[rollbackIndex]
				if rollbackErr := rewriteDirectory(
					rollback.directory,
					rollback.destination,
					rollback.source,
				); rollbackErr != nil {
					err = errors.Join(
						err,
						fmt.Errorf(
							"restore issue store %q: %w",
							rollback.directory,
							rollbackErr,
						),
					)
				}
			}
			return fmt.Errorf("rewrite issue store %q: %w", rewrites[index].directory, err)
		}
		completed++
	}
	return nil
}

func (s *Store) list(repository repopath.Repository) ([]Issue, error) {
	records, _, err := s.listValid(repository)
	return records, err
}

func (s *Store) listValid(repository repopath.Repository) ([]Issue, uint64, error) {
	directory, err := repositoryDirectory(s.Root, repository)
	if err != nil {
		return nil, 0, err
	}
	return scanDirectory(directory, repository, true)
}

func listDirectory(directory string, repository repopath.Repository) ([]Issue, error) {
	records, _, err := scanDirectory(directory, repository, false)
	return records, err
}

func scanDirectory(
	directory string,
	repository repopath.Repository,
	skipInvalid bool,
) ([]Issue, uint64, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Issue{}, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}

	records := make([]Issue, 0, len(entries))
	var maximumID uint64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, 0, infoErr
		}
		if !info.Mode().IsRegular() {
			if skipInvalid {
				continue
			}
			return nil, 0, fmt.Errorf("issue record %q is not a regular file", entry.Name())
		}
		id, parseErr := parseRecordName(entry.Name())
		if parseErr != nil {
			if skipInvalid {
				continue
			}
			return nil, 0, parseErr
		}
		record, readErr := readRecord(directory, repository, id)
		if readErr != nil {
			var invalid *invalidRecordError
			if skipInvalid && errors.As(readErr, &invalid) {
				continue
			}
			return nil, 0, readErr
		}
		records = append(records, record)
		if id > maximumID {
			maximumID = id
		}
	}
	sort.Slice(records, func(left, right int) bool {
		return records[left].ID > records[right].ID
	})
	return records, maximumID, nil
}

func (s *Store) nextRecordID(
	repository repopath.Repository,
	maximumID uint64,
) (uint64, error) {
	directory, err := repositoryDirectory(s.Root, repository)
	if err != nil {
		return 0, err
	}
	for maximumID < math.MaxUint64 {
		candidate := maximumID + 1
		path, pathErr := recordPath(directory, candidate)
		if pathErr != nil {
			return 0, pathErr
		}
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return candidate, nil
		} else if statErr != nil {
			return 0, statErr
		}
		maximumID = candidate
	}
	return 0, errors.New("issue ID space is exhausted")
}

func rewriteDirectory(
	directory string,
	source repopath.Repository,
	destination repopath.Repository,
) error {
	records, err := listDirectory(directory, source)
	if err != nil {
		return err
	}
	parent := filepath.Dir(directory)
	stagedDirectory, err := os.MkdirTemp(parent, ".issues-rewrite-")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(stagedDirectory)
	}()
	if err = os.Chmod(stagedDirectory, 0o750); err != nil {
		return err
	}
	for _, record := range records {
		record.Repository = destination.Full()
		normalize(&record)
		if err = validate(destination, record.ID, record); err != nil {
			return err
		}
		if err = writeRecord(stagedDirectory, record); err != nil {
			return err
		}
	}
	backupDirectory := stagedDirectory + ".original"
	if err = os.Rename(directory, backupDirectory); err != nil {
		return err
	}
	if err = os.Rename(stagedDirectory, directory); err != nil {
		rollbackErr := os.Rename(backupDirectory, directory)
		if rollbackErr != nil {
			return errors.Join(err, fmt.Errorf("restore issue store: %w", rollbackErr))
		}
		return err
	}
	_ = os.RemoveAll(backupDirectory)
	return nil
}

func (s *Store) get(repository repopath.Repository, id uint64) (Issue, error) {
	directory, err := repositoryDirectory(s.Root, repository)
	if err != nil {
		return Issue{}, err
	}
	record, err := readRecord(directory, repository, id)
	if errors.Is(err, os.ErrNotExist) {
		return Issue{}, fmt.Errorf("%w: %d", ErrNotFound, id)
	}
	return record, err
}

func (s *Store) save(repository repopath.Repository, record Issue) error {
	directory, err := repositoryDirectory(s.Root, repository)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	return writeRecord(directory, record)
}

func writeRecord(directory string, record Issue) error {
	path, err := recordPath(directory, record.ID)
	if err != nil {
		return err
	}
	contents, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if len(contents) > maximumRecordBytes {
		return fmt.Errorf("issue record %d exceeds %d bytes", record.ID, maximumRecordBytes)
	}

	temporary, err := os.CreateTemp(directory, ".issue-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()
	if err = temporary.Chmod(0o640); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func repositoryDirectory(root string, repository repopath.Repository) (string, error) {
	if err := validateRepository(repository); err != nil {
		return "", err
	}
	parts := append(append([]string(nil), repository.Groups...), repository.Name+storeSuffix)
	return repopath.SafeJoin(root, parts...)
}

func repositoryGitDirectory(root string, repository repopath.Repository) (string, error) {
	if err := validateRepository(repository); err != nil {
		return "", err
	}
	parts := append(append([]string(nil), repository.Groups...), repository.Name+".git")
	return repopath.SafeJoin(root, parts...)
}

func validateRepository(repository repopath.Repository) error {
	parsed, _, err := repopath.ParseGitRequestPath("/" + repository.Full() + ".git/info/refs")
	if err != nil || parsed.Full() != repository.Full() {
		if err == nil {
			err = errors.New("repository path is not canonical")
		}
		return fmt.Errorf("invalid repository: %w", err)
	}
	return nil
}

func recordPath(directory string, id uint64) (string, error) {
	if id == 0 {
		return "", errors.New("issue ID must be greater than zero")
	}
	return repopath.SafeJoin(directory, strconv.FormatUint(id, 10)+".json")
}

func parseRecordName(name string) (uint64, error) {
	stem := strings.TrimSuffix(name, ".json")
	id, err := strconv.ParseUint(stem, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != stem {
		return 0, fmt.Errorf("invalid issue record name %q", name)
	}
	return id, nil
}

func readRecord(
	directory string,
	repository repopath.Repository,
	id uint64,
) (Issue, error) {
	path, err := recordPath(directory, id)
	if err != nil {
		return Issue{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return Issue{}, err
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return Issue{}, err
	}
	if !info.Mode().IsRegular() {
		return Issue{}, fmt.Errorf("issue record %q is not a regular file", filepath.Base(path))
	}
	if info.Size() > maximumRecordBytes {
		return Issue{}, &invalidRecordError{cause: fmt.Errorf(
			"issue record %q is too large",
			filepath.Base(path),
		)}
	}

	decoder := json.NewDecoder(io.LimitReader(file, maximumRecordBytes+1))
	decoder.DisallowUnknownFields()
	var record Issue
	if err = decoder.Decode(&record); err != nil {
		return Issue{}, &invalidRecordError{cause: fmt.Errorf("read issue %d: %w", id, err)}
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("record must contain one JSON document")
		}
		return Issue{}, &invalidRecordError{cause: fmt.Errorf("read issue %d: %w", id, err)}
	}
	if err = validate(repository, id, record); err != nil {
		return Issue{}, &invalidRecordError{cause: fmt.Errorf("read issue %d: %w", id, err)}
	}
	return record, nil
}

func normalize(record *Issue) {
	record.CreatedAt = record.CreatedAt.UTC()
	record.UpdatedAt = record.UpdatedAt.UTC()
	if record.ClosedAt != nil {
		closedAt := record.ClosedAt.UTC()
		record.ClosedAt = &closedAt
	}
	if record.Labels == nil {
		record.Labels = []string{}
	}
	if record.Assignees == nil {
		record.Assignees = []string{}
	}
	if record.Comments == nil {
		record.Comments = []Comment{}
	}
	for index := range record.Comments {
		record.Comments[index].CreatedAt = record.Comments[index].CreatedAt.UTC()
	}
}

func validate(repository repopath.Repository, expectedID uint64, record Issue) error {
	if expectedID == 0 || record.ID == 0 {
		return errors.New("issue ID must be greater than zero")
	}
	if record.ID != expectedID {
		return fmt.Errorf("issue ID is %d, expected %d", record.ID, expectedID)
	}
	if record.Repository != repository.Full() {
		return fmt.Errorf(
			"issue repository is %q, expected %q",
			record.Repository,
			repository.Full(),
		)
	}
	if strings.TrimSpace(record.Title) == "" {
		return errors.New("issue title is required")
	}
	if len(record.Title) > MaximumTitleBytes {
		return errors.New("issue title is too long")
	}
	if len(record.Description) > MaximumBodyBytes {
		return errors.New("issue description is too long")
	}
	if strings.TrimSpace(record.Author) == "" {
		return errors.New("issue author is required")
	}
	switch record.State {
	case StateOpen, StateClosed:
	default:
		return fmt.Errorf("invalid issue state %q", record.State)
	}
	if record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return errors.New("issue timestamps are required")
	}
	if record.UpdatedAt.Before(record.CreatedAt) {
		return errors.New("issue update time precedes its creation time")
	}
	if record.Labels == nil || record.Assignees == nil || record.Comments == nil {
		return errors.New("issue label, assignee, and comment arrays are required")
	}
	if err := validateNames(
		record.Labels,
		MaximumLabels,
		MaximumLabelBytes,
		"label",
	); err != nil {
		return err
	}
	if err := validateNames(
		record.Assignees,
		MaximumAssignees,
		MaximumAssigneeBytes,
		"assignee",
	); err != nil {
		return err
	}

	commentIDs := make(map[uint64]struct{}, len(record.Comments))
	for index, comment := range record.Comments {
		if comment.ID == 0 {
			return fmt.Errorf("comment %d has a zero ID", index)
		}
		if _, exists := commentIDs[comment.ID]; exists {
			return fmt.Errorf("duplicate comment ID %d", comment.ID)
		}
		commentIDs[comment.ID] = struct{}{}
		if strings.TrimSpace(comment.Author) == "" ||
			strings.TrimSpace(comment.Body) == "" ||
			comment.CreatedAt.IsZero() {
			return fmt.Errorf("comment %d is malformed", comment.ID)
		}
		if len(comment.Body) > MaximumBodyBytes {
			return fmt.Errorf("comment %d body is too long", comment.ID)
		}
	}

	switch record.State {
	case StateOpen:
		if record.ClosedBy != "" || record.ClosedAt != nil {
			return errors.New("open issue has closure metadata")
		}
	case StateClosed:
		if strings.TrimSpace(record.ClosedBy) == "" || record.ClosedAt == nil {
			return errors.New("closed issue lacks closure metadata")
		}
	}
	return nil
}

func validateNames(values []string, maximumCount, maximumBytes int, kind string) error {
	if len(values) > maximumCount {
		return fmt.Errorf("issue has more than %d %ss", maximumCount, kind)
	}
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		if strings.TrimSpace(value) != value || value == "" {
			return fmt.Errorf("%s %d is malformed", kind, index)
		}
		if len(value) > maximumBytes {
			return fmt.Errorf("%s %d is too long", kind, index)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("duplicate %s %q", kind, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
