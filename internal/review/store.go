package review

import (
	"encoding/hex"
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
	"sync"
	"time"

	"github.com/define42/GitOne/internal/repopath"
)

const maximumRecordBytes = 8 << 20

var (
	ErrNotFound  = errors.New("merge request not found")
	ErrDuplicate = errors.New("an open merge request already exists for these branches")
	storeLocks   sync.Map //nolint:gochecknoglobals // Coordinates stores opened for the same root.
)

type Store struct {
	Root string
	mu   *sync.Mutex
}

func NewStore(root string) *Store {
	key, err := filepath.Abs(root)
	if err != nil {
		key = filepath.Clean(root)
	} else if resolved, resolveErr := filepath.EvalSymlinks(key); resolveErr == nil {
		key = resolved
	}
	lock, _ := storeLocks.LoadOrStore(key, &sync.Mutex{})
	return &Store{Root: root, mu: lock.(*sync.Mutex)}
}

func (s *Store) List(repository repopath.Repository) ([]MergeRequest, error) {
	unlock, err := s.lockStore()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = unlock()
	}()

	return s.list(repository)
}

func (s *Store) Get(repository repopath.Repository, id uint64) (MergeRequest, error) {
	if id == 0 {
		return MergeRequest{}, errors.New("merge request ID must be greater than zero")
	}
	unlock, err := s.lockStore()
	if err != nil {
		return MergeRequest{}, err
	}
	defer func() {
		_ = unlock()
	}()

	return s.get(repository, id)
}

func (s *Store) Create(
	repository repopath.Repository,
	request *MergeRequest,
) error {
	if request == nil {
		return errors.New("merge request is required")
	}
	unlock, err := s.lockStore()
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

	existing, err := s.list(repository)
	if err != nil {
		return err
	}
	var maximumID uint64
	for _, candidate := range existing {
		if candidate.ID > maximumID {
			maximumID = candidate.ID
		}
		if candidate.State == StateOpen &&
			candidate.Target == request.Target &&
			candidate.Source == request.Source {
			return fmt.Errorf("%w: !%d", ErrDuplicate, candidate.ID)
		}
	}
	if maximumID == math.MaxUint64 {
		return errors.New("merge request ID space is exhausted")
	}

	now := time.Now().UTC()
	request.ID = maximumID + 1
	if request.Repository == "" {
		request.Repository = repository.Full()
	}
	if request.State == "" {
		request.State = StateOpen
	}
	if request.CreatedAt.IsZero() {
		request.CreatedAt = now
	}
	if request.UpdatedAt.IsZero() {
		request.UpdatedAt = request.CreatedAt
	}
	if request.RequiredApprovals == 0 {
		request.RequiredApprovals = 1
	}
	normalize(request)
	if err = validate(repository, request.ID, *request); err != nil {
		return err
	}
	return s.save(repository, *request)
}

func (s *Store) Update(
	repository repopath.Repository,
	id uint64,
	update func(*MergeRequest) error,
) (MergeRequest, error) {
	if id == 0 {
		return MergeRequest{}, errors.New("merge request ID must be greater than zero")
	}
	if update == nil {
		return MergeRequest{}, errors.New("merge request update is required")
	}
	unlock, err := s.lockStore()
	if err != nil {
		return MergeRequest{}, err
	}
	defer func() {
		_ = unlock()
	}()

	request, err := s.get(repository, id)
	if err != nil {
		return MergeRequest{}, err
	}
	if err = update(&request); err != nil {
		return MergeRequest{}, err
	}
	request.UpdatedAt = time.Now().UTC()
	normalize(&request)
	if err = validate(repository, id, request); err != nil {
		return MergeRequest{}, err
	}
	if request.State == StateOpen {
		existing, listErr := s.list(repository)
		if listErr != nil {
			return MergeRequest{}, listErr
		}
		for _, candidate := range existing {
			if candidate.ID != request.ID &&
				candidate.State == StateOpen &&
				candidate.Target == request.Target &&
				candidate.Source == request.Source {
				return MergeRequest{}, fmt.Errorf("%w: !%d", ErrDuplicate, candidate.ID)
			}
		}
	}
	if err = s.save(repository, request); err != nil {
		return MergeRequest{}, err
	}
	return request, nil
}

func (s *Store) Relocate(
	repository repopath.Repository,
	destination repopath.Repository,
) error {
	unlock, err := s.lockStore()
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()

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
		return errors.New("destination review store exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if _, err = os.Stat(sourceDirectory); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}

	requests, err := s.list(repository)
	if err != nil {
		return err
	}
	parent := filepath.Dir(destinationDirectory)
	if err = os.MkdirAll(parent, 0o750); err != nil {
		return err
	}
	stagedDirectory, err := os.MkdirTemp(parent, ".reviews-relocate-")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(stagedDirectory)
	}()
	if err = os.Chmod(stagedDirectory, 0o750); err != nil {
		return err
	}
	for _, request := range requests {
		request.Repository = destination.Full()
		normalize(&request)
		if err = validate(destination, request.ID, request); err != nil {
			return err
		}
		if err = writeRecord(stagedDirectory, request); err != nil {
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
			return errors.Join(err, fmt.Errorf("restore review store: %w", rollbackErr))
		}
		return err
	}
	_ = os.RemoveAll(backupDirectory)
	return nil
}

func (s *Store) RewriteGroup(sourceGroup, destinationGroup string) error {
	unlock, err := s.lockStore()
	if err != nil {
		return err
	}
	defer func() {
		_ = unlock()
	}()

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
			case strings.HasSuffix(name, ".reviews"):
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
				repositoryName := strings.TrimSuffix(name, ".reviews")
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
				strings.HasSuffix(name, ".build"):
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
							"restore review store %q: %w",
							rollback.directory,
							rollbackErr,
						),
					)
				}
			}
			return fmt.Errorf("rewrite review store %q: %w", rewrites[index].directory, err)
		}
		completed++
	}
	return nil
}

func (s *Store) list(repository repopath.Repository) ([]MergeRequest, error) {
	directory, err := repositoryDirectory(s.Root, repository)
	if err != nil {
		return nil, err
	}
	return listDirectory(directory, repository)
}

func listDirectory(
	directory string,
	repository repopath.Repository,
) ([]MergeRequest, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []MergeRequest{}, nil
	}
	if err != nil {
		return nil, err
	}

	requests := make([]MergeRequest, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return nil, infoErr
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("merge request record %q is not a regular file", entry.Name())
		}
		id, parseErr := parseRecordName(entry.Name())
		if parseErr != nil {
			return nil, parseErr
		}
		request, readErr := readRecord(directory, repository, id)
		if readErr != nil {
			return nil, readErr
		}
		requests = append(requests, request)
	}
	sort.Slice(requests, func(left, right int) bool {
		return requests[left].ID > requests[right].ID
	})
	return requests, nil
}

func rewriteDirectory(
	directory string,
	source repopath.Repository,
	destination repopath.Repository,
) error {
	requests, err := listDirectory(directory, source)
	if err != nil {
		return err
	}
	parent := filepath.Dir(directory)
	stagedDirectory, err := os.MkdirTemp(parent, ".reviews-rewrite-")
	if err != nil {
		return err
	}
	defer func() {
		_ = os.RemoveAll(stagedDirectory)
	}()
	if err = os.Chmod(stagedDirectory, 0o750); err != nil {
		return err
	}
	for _, request := range requests {
		request.Repository = destination.Full()
		normalize(&request)
		if err = validate(destination, request.ID, request); err != nil {
			return err
		}
		if err = writeRecord(stagedDirectory, request); err != nil {
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
			return errors.Join(err, fmt.Errorf("restore review store: %w", rollbackErr))
		}
		return err
	}
	_ = os.RemoveAll(backupDirectory)
	return nil
}

func (s *Store) get(repository repopath.Repository, id uint64) (MergeRequest, error) {
	directory, err := repositoryDirectory(s.Root, repository)
	if err != nil {
		return MergeRequest{}, err
	}
	request, err := readRecord(directory, repository, id)
	if errors.Is(err, os.ErrNotExist) {
		return MergeRequest{}, fmt.Errorf("%w: %d", ErrNotFound, id)
	}
	return request, err
}

func (s *Store) save(repository repopath.Repository, request MergeRequest) error {
	directory, err := repositoryDirectory(s.Root, repository)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(directory, 0o750); err != nil {
		return err
	}
	return writeRecord(directory, request)
}

func writeRecord(directory string, request MergeRequest) error {
	path, err := recordPath(directory, request.ID)
	if err != nil {
		return err
	}
	contents, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	if len(contents) > maximumRecordBytes {
		return fmt.Errorf(
			"merge request record %d exceeds %d bytes",
			request.ID,
			maximumRecordBytes,
		)
	}

	temporary, err := os.CreateTemp(directory, ".review-*.tmp")
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
	parts := append(append([]string(nil), repository.Groups...), repository.Name+".reviews")
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
		return "", errors.New("merge request ID must be greater than zero")
	}
	return repopath.SafeJoin(directory, strconv.FormatUint(id, 10)+".json")
}

func parseRecordName(name string) (uint64, error) {
	stem := strings.TrimSuffix(name, ".json")
	id, err := strconv.ParseUint(stem, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != stem {
		return 0, fmt.Errorf("invalid merge request record name %q", name)
	}
	return id, nil
}

func readRecord(
	directory string,
	repository repopath.Repository,
	id uint64,
) (MergeRequest, error) {
	path, err := recordPath(directory, id)
	if err != nil {
		return MergeRequest{}, err
	}
	file, err := os.Open(path)
	if err != nil {
		return MergeRequest{}, err
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return MergeRequest{}, err
	}
	if !info.Mode().IsRegular() {
		return MergeRequest{}, fmt.Errorf("merge request record %q is not a regular file", filepath.Base(path))
	}
	if info.Size() > maximumRecordBytes {
		return MergeRequest{}, fmt.Errorf("merge request record %q is too large", filepath.Base(path))
	}

	decoder := json.NewDecoder(io.LimitReader(file, maximumRecordBytes+1))
	decoder.DisallowUnknownFields()
	var request MergeRequest
	if err = decoder.Decode(&request); err != nil {
		return MergeRequest{}, fmt.Errorf("read merge request %d: %w", id, err)
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("record must contain one JSON document")
		}
		return MergeRequest{}, fmt.Errorf("read merge request %d: %w", id, err)
	}
	if err = validate(repository, id, request); err != nil {
		return MergeRequest{}, fmt.Errorf("read merge request %d: %w", id, err)
	}
	return request, nil
}

func normalize(request *MergeRequest) {
	request.CreatedAt = request.CreatedAt.UTC()
	request.UpdatedAt = request.UpdatedAt.UTC()
	if request.MergedAt != nil {
		mergedAt := request.MergedAt.UTC()
		request.MergedAt = &mergedAt
	}
	if request.ClosedAt != nil {
		closedAt := request.ClosedAt.UTC()
		request.ClosedAt = &closedAt
	}
	if request.MergeStartedAt != nil {
		mergeStartedAt := request.MergeStartedAt.UTC()
		request.MergeStartedAt = &mergeStartedAt
	}
	if request.Approvals == nil {
		request.Approvals = []Approval{}
	}
	for index := range request.Approvals {
		request.Approvals[index].CreatedAt = request.Approvals[index].CreatedAt.UTC()
	}
	if request.Threads == nil {
		request.Threads = []Thread{}
	}
	for threadIndex := range request.Threads {
		thread := &request.Threads[threadIndex]
		thread.CreatedAt = thread.CreatedAt.UTC()
		if thread.ResolvedAt != nil {
			resolvedAt := thread.ResolvedAt.UTC()
			thread.ResolvedAt = &resolvedAt
		}
		if thread.Comments == nil {
			thread.Comments = []Comment{}
		}
		for commentIndex := range thread.Comments {
			thread.Comments[commentIndex].CreatedAt = thread.Comments[commentIndex].CreatedAt.UTC()
		}
	}
}

func validate(
	repository repopath.Repository,
	expectedID uint64,
	request MergeRequest,
) error {
	if expectedID == 0 || request.ID == 0 {
		return errors.New("merge request ID must be greater than zero")
	}
	if request.ID != expectedID {
		return fmt.Errorf("merge request ID is %d, expected %d", request.ID, expectedID)
	}
	if request.Repository != repository.Full() {
		return fmt.Errorf(
			"merge request repository is %q, expected %q",
			request.Repository,
			repository.Full(),
		)
	}
	if strings.TrimSpace(request.Title) == "" {
		return errors.New("merge request title is required")
	}
	if strings.TrimSpace(request.Target) == "" || strings.TrimSpace(request.Source) == "" {
		return errors.New("merge request target and source are required")
	}
	if request.Target == request.Source {
		return errors.New("merge request target and source must be different")
	}
	if strings.TrimSpace(request.Author) == "" {
		return errors.New("merge request author is required")
	}
	switch request.State {
	case StateOpen, StateClosed, StateMerged:
	default:
		return fmt.Errorf("invalid merge request state %q", request.State)
	}
	if request.CreatedAt.IsZero() || request.UpdatedAt.IsZero() {
		return errors.New("merge request timestamps are required")
	}
	if request.UpdatedAt.Before(request.CreatedAt) {
		return errors.New("merge request update time precedes its creation time")
	}
	if strings.TrimSpace(request.BaseCommit) == "" ||
		strings.TrimSpace(request.HeadCommit) == "" {
		return errors.New("merge request base and head commits are required")
	}
	if !validCommitHash(request.BaseCommit) || !validCommitHash(request.HeadCommit) {
		return errors.New("merge request base and head commits must be complete Git hashes")
	}
	if request.RequiredApprovals < 1 {
		return errors.New("merge request must require at least one approval")
	}
	if request.Approvals == nil || request.Threads == nil {
		return errors.New("merge request approval and thread arrays are required")
	}
	approvalAuthors := make(map[string]struct{}, len(request.Approvals))
	for index, approval := range request.Approvals {
		if strings.TrimSpace(approval.Author) == "" ||
			strings.TrimSpace(approval.HeadCommit) == "" ||
			approval.CreatedAt.IsZero() ||
			!validCommitHash(approval.HeadCommit) {
			return fmt.Errorf("approval %d is malformed", index)
		}
		if approval.Author == request.Author && !approval.OwnerOverride {
			return fmt.Errorf("approval %d was made by the merge request author", index)
		}
		if approval.OwnerOverride && approval.Author != request.Author {
			return fmt.Errorf("approval %d has an invalid owner override", index)
		}
		if _, duplicate := approvalAuthors[approval.Author]; duplicate {
			return fmt.Errorf("duplicate approval by %q", approval.Author)
		}
		approvalAuthors[approval.Author] = struct{}{}
	}

	threadIDs := make(map[uint64]struct{}, len(request.Threads))
	for threadIndex, thread := range request.Threads {
		if thread.ID == 0 {
			return fmt.Errorf("thread %d has a zero ID", threadIndex)
		}
		if _, exists := threadIDs[thread.ID]; exists {
			return fmt.Errorf("duplicate thread ID %d", thread.ID)
		}
		threadIDs[thread.ID] = struct{}{}
		if thread.CreatedAt.IsZero() || thread.Comments == nil {
			return fmt.Errorf("thread %d is malformed", thread.ID)
		}
		if thread.Resolved {
			if strings.TrimSpace(thread.ResolvedBy) == "" || thread.ResolvedAt == nil {
				return fmt.Errorf("resolved thread %d lacks resolution metadata", thread.ID)
			}
		} else if thread.ResolvedBy != "" || thread.ResolvedAt != nil {
			return fmt.Errorf("unresolved thread %d has resolution metadata", thread.ID)
		}
		commentIDs := make(map[uint64]struct{}, len(thread.Comments))
		for commentIndex, comment := range thread.Comments {
			if comment.ID == 0 {
				return fmt.Errorf("thread %d comment %d has a zero ID", thread.ID, commentIndex)
			}
			if _, exists := commentIDs[comment.ID]; exists {
				return fmt.Errorf("thread %d has duplicate comment ID %d", thread.ID, comment.ID)
			}
			commentIDs[comment.ID] = struct{}{}
			if strings.TrimSpace(comment.Author) == "" ||
				strings.TrimSpace(comment.Body) == "" ||
				comment.CreatedAt.IsZero() {
				return fmt.Errorf("thread %d comment %d is malformed", thread.ID, comment.ID)
			}
		}
	}

	switch request.State {
	case StateOpen:
		if request.MergedCommit != "" || request.MergedStrategy != "" ||
			request.MergedBy != "" || request.MergedAt != nil ||
			request.ClosedBy != "" || request.ClosedAt != nil {
			return errors.New("open merge request has completion metadata")
		}
	case StateClosed:
		if strings.TrimSpace(request.ClosedBy) == "" || request.ClosedAt == nil {
			return errors.New("closed merge request lacks closure metadata")
		}
		if request.MergedCommit != "" || request.MergedStrategy != "" ||
			request.MergedBy != "" || request.MergedAt != nil {
			return errors.New("closed merge request has merge metadata")
		}
	case StateMerged:
		if strings.TrimSpace(request.MergedCommit) == "" ||
			strings.TrimSpace(request.MergedStrategy) == "" ||
			strings.TrimSpace(request.MergedBy) == "" ||
			request.MergedAt == nil ||
			!validCommitHash(request.MergedCommit) {
			return errors.New("merged merge request lacks merge metadata")
		}
		if request.ClosedBy != "" || request.ClosedAt != nil {
			return errors.New("merged merge request has closure metadata")
		}
	}
	mergeClaimFields := 0
	if request.MergeClaimID != "" {
		mergeClaimFields++
	}
	if request.MergeOwnerID != "" {
		mergeClaimFields++
	}
	if request.MergeHeadCommit != "" {
		mergeClaimFields++
	}
	if request.MergeStartedBy != "" {
		mergeClaimFields++
	}
	if request.MergeStartedAt != nil {
		mergeClaimFields++
	}
	mergeResultFields := 0
	if request.MergeTargetCommit != "" {
		mergeResultFields++
	}
	if request.MergeResultCommit != "" {
		mergeResultFields++
	}
	if request.MergeResultStrategy != "" {
		mergeResultFields++
	}
	if request.MergeInProgress {
		if request.State != StateOpen ||
			mergeClaimFields != 5 ||
			!validCommitHash(request.MergeHeadCommit) {
			return errors.New("merge request has malformed merge-in-progress metadata")
		}
		if mergeResultFields != 0 && mergeResultFields != 3 {
			return errors.New("merge request has incomplete planned merge metadata")
		}
		if mergeResultFields == 3 {
			if !validCommitHash(request.MergeTargetCommit) ||
				!validCommitHash(request.MergeResultCommit) {
				return errors.New("merge request has malformed planned merge commits")
			}
			switch request.MergeResultStrategy {
			case "already-up-to-date", "fast-forward", "merge-commit":
			default:
				return errors.New("merge request has an invalid planned merge strategy")
			}
		}
	} else if mergeClaimFields != 0 || mergeResultFields != 0 {
		return errors.New("merge request has merge intent metadata without an active merge")
	}
	return nil
}

func validCommitHash(value string) bool {
	if len(value) != 40 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
