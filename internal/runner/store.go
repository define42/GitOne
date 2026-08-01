package runner

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/repopath"
)

type Status string

const (
	StatusManual    Status = "manual"
	StatusWaiting   Status = "waiting"
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

type JobDependency struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type Job struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Repository     string          `json:"repository"`
	Branch         string          `json:"branch"`
	Commit         string          `json:"commit"`
	Image          string          `json:"image,omitempty"`
	Needs          []JobDependency `json:"needs,omitempty"`
	Status         Status          `json:"status"`
	CreatedAt      time.Time       `json:"createdAt"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
	Error          string          `json:"error,omitempty"`
	RunnerID       string          `json:"runnerId,omitempty"`
	Attempt        int             `json:"attempt,omitempty"`
	LeaseExpiresAt *time.Time      `json:"leaseExpiresAt,omitempty"`
	RerunOf        string          `json:"rerunOf,omitempty"`
}

type Store struct {
	Root string
}

const maximumLogBytes = 1 << 20

func NewStore(storageRoot string) Store {
	return Store{Root: storageRoot}
}

func (s Store) List(repository repopath.Repository) ([]Job, error) {
	directory, err := s.repositoryDirectory(repository)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []Job{}, nil
	}
	if err != nil {
		return nil, err
	}
	jobs := make([]Job, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		job, readErr := readJob(directory, strings.TrimSuffix(entry.Name(), ".json"))
		if readErr != nil {
			return nil, readErr
		}
		jobs = append(jobs, job)
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].CreatedAt.Equal(jobs[j].CreatedAt) {
			return jobs[i].Name < jobs[j].Name
		}
		return jobs[i].CreatedAt.After(jobs[j].CreatedAt)
	})
	return jobs, nil
}

func (s Store) Get(repository repopath.Repository, id string) (Job, error) {
	directory, err := s.repositoryDirectory(repository)
	if err != nil {
		return Job{}, err
	}
	return readJob(directory, id)
}

func readJob(directory, id string) (Job, error) {
	path, err := buildPath(directory, id, ".json")
	if err != nil {
		return Job{}, err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Job{}, err
	}
	var job Job
	if err = json.Unmarshal(contents, &job); err != nil {
		return Job{}, fmt.Errorf("read build %q: %w", id, err)
	}
	if !gitformat.IsSHA256OID(job.Commit) {
		return Job{}, fmt.Errorf("read build %q: commit must be a lowercase SHA-256 object ID", id)
	}
	return job, nil
}

func (s Store) Log(repository repopath.Repository, id string) (string, error) {
	directory, err := s.repositoryDirectory(repository)
	if err != nil {
		return "", err
	}
	path, err := buildPath(directory, id, ".log")
	if err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return readLog(file)
}

func readLog(file *os.File) (string, error) {
	defer func() {
		_ = file.Close()
	}()
	contents, err := io.ReadAll(io.LimitReader(file, maximumLogBytes+1))
	if err != nil {
		return "", err
	}
	if len(contents) > maximumLogBytes {
		contents = append(contents[:maximumLogBytes], []byte("\n[log truncated by GitOne]\n")...)
	}
	return string(contents), nil
}

func (s Store) save(repository repopath.Repository, job Job) error {
	if !gitformat.IsSHA256OID(job.Commit) {
		return errors.New("build commit must be a lowercase SHA-256 object ID")
	}
	path, err := s.jobPath(repository, job.ID, ".json")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".build-*.tmp")
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
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (s Store) createLog(repository repopath.Repository, id string) (*os.File, error) {
	path, err := s.jobPath(repository, id, ".log")
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
}

func (s Store) logSize(repository repopath.Repository, id string) (int64, error) {
	path, err := s.jobPath(repository, id, ".log")
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

func (s Store) appendLog(
	repository repopath.Repository,
	id string,
	offset int64,
	contents []byte,
) (int64, error) {
	path, err := s.jobPath(repository, id, ".log")
	if err != nil {
		return 0, err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return 0, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	defer func() {
		_ = file.Close()
	}()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if info.Size() != offset {
		return info.Size(), fmt.Errorf(
			"build log offset is %d, not %d",
			info.Size(),
			offset,
		)
	}
	if info.Size() >= MaximumStoredLogBytes {
		return info.Size(), nil
	}
	remaining := MaximumStoredLogBytes - info.Size()
	if int64(len(contents)) > remaining {
		contents = contents[:remaining]
	}
	if _, err = file.Seek(0, io.SeekEnd); err != nil {
		return 0, err
	}
	if _, err = file.Write(contents); err != nil {
		return 0, err
	}
	return info.Size() + int64(len(contents)), nil
}

func (s Store) workRoot() (string, error) {
	return repopath.SafeJoin(s.Root, ".gitone", "work")
}

func (s Store) repositoryDirectory(repository repopath.Repository) (string, error) {
	parts := append(append([]string(nil), repository.Groups...), repository.Name+".build")
	return repopath.SafeJoin(s.Root, parts...)
}

func (s Store) jobPath(repository repopath.Repository, id, suffix string) (string, error) {
	directory, err := s.repositoryDirectory(repository)
	if err != nil {
		return "", err
	}
	return buildPath(directory, id, suffix)
}

func buildPath(directory, id, suffix string) (string, error) {
	if !validJobID(id) {
		return "", errors.New("invalid build ID")
	}
	return repopath.SafeJoin(directory, id+suffix)
}

func validJobID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	for _, character := range id {
		if !((character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_') {
			return false
		}
	}
	return true
}
