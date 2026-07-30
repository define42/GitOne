package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/lfspointer"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	importLFSBatchSize          = 100
	importLFSMaximumResponse    = 10 << 20
	importLFSMaximumObjectBytes = int64(100 << 30)
)

type importLFSObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type importLFSBatchRequest struct {
	Operation string            `json:"operation"`
	Transfers []string          `json:"transfers"`
	Objects   []importLFSObject `json:"objects"`
}

type importLFSAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

type importLFSObjectError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type importLFSBatchObject struct {
	OID     string                     `json:"oid"`
	Size    int64                      `json:"size"`
	Actions map[string]importLFSAction `json:"actions"`
	Error   *importLFSObjectError      `json:"error"`
}

type importLFSBatchResponse struct {
	Transfer string                 `json:"transfer"`
	Objects  []importLFSBatchObject `json:"objects"`
}

func importRemoteLFS(
	ctx context.Context,
	repository *git.Repository,
	options ImportRepositoryOptions,
	lfsPath string,
	existingLFSUsage int64,
) error {
	objects, err := reachableLFSObjects(repository)
	if err != nil {
		return fmt.Errorf("inspect imported Git LFS pointers: %w", err)
	}
	if len(objects) == 0 {
		return os.MkdirAll(filepath.Join(lfsPath, "objects"), 0o750)
	}

	batchURL, ok, err := remoteLFSBatchURL(options.URL)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("remote repository contains LFS objects but has no HTTP(S) LFS endpoint")
	}
	if err = os.MkdirAll(filepath.Join(lfsPath, "objects"), 0o750); err != nil {
		return err
	}

	sorted := make([]importLFSObject, 0, len(objects))
	for oid, size := range objects {
		sorted = append(sorted, importLFSObject{OID: oid, Size: size})
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].OID < sorted[j].OID
	})

	if err = enforceImportLFSPolicy(options.LFSPolicy, sorted, existingLFSUsage); err != nil {
		return err
	}

	client := newImportHTTPClient()
	for start := 0; start < len(sorted); start += importLFSBatchSize {
		end := min(start+importLFSBatchSize, len(sorted))
		if err = downloadLFSBatch(
			ctx,
			client,
			batchURL,
			options,
			lfsPath,
			sorted[start:end],
		); err != nil {
			return err
		}
	}
	return nil
}

// enforceImportLFSPolicy rejects an import whose reachable LFS objects would
// breach the destination group's per-object or total storage limits, before any
// object is downloaded. Sizes are attacker-controlled (the remote LFS server is
// untrusted), so the total is accumulated overflow-safely against the remaining
// budget rather than summed first.
func enforceImportLFSPolicy(
	policy control.LFSPolicy,
	objects []importLFSObject,
	existingUsage int64,
) error {
	remaining := int64(-1)
	if policy.MaximumStorageBytes > 0 {
		remaining = policy.MaximumStorageBytes - existingUsage
		if remaining < 0 {
			remaining = 0
		}
	}
	var total int64
	for _, object := range objects {
		if object.Size < 0 {
			return fmt.Errorf("remote Git LFS object %s has an invalid size", object.OID)
		}
		if policy.MaximumObjectBytes > 0 && object.Size > policy.MaximumObjectBytes {
			return fmt.Errorf(
				"imported Git LFS object %s exceeds the group's %d-byte object limit",
				object.OID,
				policy.MaximumObjectBytes,
			)
		}
		if remaining >= 0 {
			if object.Size > remaining-total {
				return errors.New("imported Git LFS objects exceed the group's storage limit")
			}
			total += object.Size
		}
	}
	return nil
}

func remoteLFSBatchURL(rawURL string) (*url.URL, bool, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, false, fmt.Errorf("parse remote LFS URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, false, nil
	}
	if parsed.Hostname() == "" {
		return nil, false, errors.New("remote LFS URL has no host")
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/info/lfs/objects/batch"
	return parsed, true, nil
}

func downloadLFSBatch(
	ctx context.Context,
	client *http.Client,
	batchURL *url.URL,
	options ImportRepositoryOptions,
	lfsPath string,
	objects []importLFSObject,
) error {
	payload, err := json.Marshal(importLFSBatchRequest{
		Operation: "download",
		Transfers: []string{"basic"},
		Objects:   objects,
	})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		batchURL.String(),
		bytes.NewReader(payload),
	)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/vnd.git-lfs+json")
	request.Header.Set("Content-Type", "application/vnd.git-lfs+json")
	if options.Username != "" {
		request.SetBasicAuth(options.Username, options.Password)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("request remote Git LFS objects: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, importLFSMaximumResponse+1))
	closeErr := response.Body.Close()
	if readErr != nil {
		return fmt.Errorf("read remote Git LFS batch response: %w", readErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close remote Git LFS batch response: %w", closeErr)
	}
	if len(body) > importLFSMaximumResponse {
		return errors.New("remote Git LFS batch response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("remote Git LFS batch request returned HTTP %d", response.StatusCode)
	}

	var batch importLFSBatchResponse
	if err = json.Unmarshal(body, &batch); err != nil {
		return errors.New("remote Git LFS batch response is invalid")
	}
	if batch.Transfer != "" && batch.Transfer != "basic" {
		return fmt.Errorf("remote Git LFS transfer %q is not supported", batch.Transfer)
	}
	expected := make(map[string]int64, len(objects))
	for _, object := range objects {
		expected[object.OID] = object.Size
	}
	seen := make(map[string]bool, len(objects))
	for _, object := range batch.Objects {
		size, requested := expected[object.OID]
		if !requested {
			return fmt.Errorf("remote Git LFS returned unexpected object %q", object.OID)
		}
		if seen[object.OID] {
			return fmt.Errorf("remote Git LFS returned duplicate object %q", object.OID)
		}
		seen[object.OID] = true
		if object.Size != size {
			return fmt.Errorf("remote Git LFS object %s has an unexpected size", object.OID)
		}
		if object.Error != nil {
			return fmt.Errorf(
				"remote Git LFS object %s is unavailable (HTTP %d): %s",
				object.OID,
				object.Error.Code,
				object.Error.Message,
			)
		}
		action, ok := object.Actions["download"]
		if !ok || strings.TrimSpace(action.Href) == "" {
			return fmt.Errorf("remote Git LFS object %s has no download action", object.OID)
		}
		if err = downloadLFSObject(
			ctx,
			client,
			batchURL,
			options,
			lfsPath,
			importLFSObject{OID: object.OID, Size: size},
			action,
		); err != nil {
			return err
		}
	}
	for _, object := range objects {
		if !seen[object.OID] {
			return fmt.Errorf("remote Git LFS omitted object %s", object.OID)
		}
	}
	return nil
}

func downloadLFSObject(
	ctx context.Context,
	client *http.Client,
	batchURL *url.URL,
	options ImportRepositoryOptions,
	lfsPath string,
	object importLFSObject,
	action importLFSAction,
) error {
	if object.Size < 0 || object.Size > importLFSMaximumObjectBytes {
		return fmt.Errorf("remote Git LFS object %s has an invalid size", object.OID)
	}
	actionURL, err := batchURL.Parse(action.Href)
	if err != nil || actionURL.Hostname() == "" ||
		(actionURL.Scheme != "http" && actionURL.Scheme != "https") {
		return fmt.Errorf("remote Git LFS object %s has an invalid download URL", object.OID)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, actionURL.String(), nil)
	if err != nil {
		return err
	}
	if options.Username != "" && sameImportURLOrigin(batchURL, actionURL) {
		request.SetBasicAuth(options.Username, options.Password)
	}
	for name, value := range action.Header {
		request.Header.Set(name, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download remote Git LFS object %s: %w", object.OID, err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_ = response.Body.Close()
		return fmt.Errorf(
			"download remote Git LFS object %s returned HTTP %d",
			object.OID,
			response.StatusCode,
		)
	}

	directory := filepath.Join(lfsPath, "objects", object.OID[:2], object.OID[2:4])
	if err = os.MkdirAll(directory, 0o750); err != nil {
		_ = response.Body.Close()
		return err
	}
	temporary, err := os.CreateTemp(directory, ".import-*")
	if err != nil {
		_ = response.Body.Close()
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = os.Remove(temporaryPath)
	}()

	hash := sha256.New()
	written, copyErr := io.Copy(
		io.MultiWriter(temporary, hash),
		io.LimitReader(response.Body, object.Size+1),
	)
	closeBodyErr := response.Body.Close()
	if copyErr != nil {
		_ = temporary.Close()
		return fmt.Errorf("download remote Git LFS object %s: %w", object.OID, copyErr)
	}
	if closeBodyErr != nil {
		_ = temporary.Close()
		return fmt.Errorf("close remote Git LFS object %s: %w", object.OID, closeBodyErr)
	}
	if written != object.Size {
		_ = temporary.Close()
		return fmt.Errorf(
			"remote Git LFS object %s size mismatch: expected %d bytes, received %d",
			object.OID,
			object.Size,
			written,
		)
	}
	if actual := hex.EncodeToString(hash.Sum(nil)); actual != object.OID {
		_ = temporary.Close()
		return fmt.Errorf("remote Git LFS object %s failed SHA-256 verification", object.OID)
	}
	if err = temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, filepath.Join(directory, object.OID))
}

func reachableLFSObjects(repository *git.Repository) (map[string]int64, error) {
	references, err := repository.References()
	if err != nil {
		return nil, err
	}
	defer references.Close()

	objects := make(map[string]int64)
	seenReferences := make(map[plumbing.Hash]bool)
	seenCommits := make(map[plumbing.Hash]bool)
	seenTrees := make(map[plumbing.Hash]bool)
	seenBlobs := make(map[plumbing.Hash]bool)
	err = references.ForEach(func(reference *plumbing.Reference) error {
		if reference.Type() != plumbing.HashReference || seenReferences[reference.Hash()] {
			return nil
		}
		seenReferences[reference.Hash()] = true
		commit, peelErr := peelImportCommit(repository, reference.Hash())
		if peelErr != nil {
			return fmt.Errorf("resolve %s: %w", reference.Name(), peelErr)
		}
		if commit == nil {
			return nil
		}
		commits := object.NewCommitPreorderIter(commit, seenCommits, nil)
		iterateErr := commits.ForEach(func(candidate *object.Commit) error {
			seenCommits[candidate.Hash] = true
			return collectCommitLFSObjects(
				repository,
				candidate,
				seenTrees,
				seenBlobs,
				objects,
			)
		})
		commits.Close()
		return iterateErr
	})
	return objects, err
}

func peelImportCommit(
	repository *git.Repository,
	hash plumbing.Hash,
) (*object.Commit, error) {
	seen := make(map[plumbing.Hash]bool)
	for {
		if seen[hash] {
			return nil, fmt.Errorf("tag cycle at %s", hash)
		}
		seen[hash] = true
		current, err := repository.Object(plumbing.AnyObject, hash)
		if err != nil {
			return nil, err
		}
		switch typed := current.(type) {
		case *object.Commit:
			return typed, nil
		case *object.Tag:
			hash = typed.Target
		default:
			return nil, nil
		}
	}
}

func collectCommitLFSObjects(
	repository *git.Repository,
	commit *object.Commit,
	seenTrees map[plumbing.Hash]bool,
	seenBlobs map[plumbing.Hash]bool,
	objects map[string]int64,
) error {
	tree, err := commit.Tree()
	if err != nil {
		return fmt.Errorf("load tree for commit %s: %w", commit.Hash, err)
	}
	return collectTreeLFSObjects(repository, tree, commit.Hash, seenTrees, seenBlobs, objects)
}

func collectTreeLFSObjects(
	repository *git.Repository,
	tree *object.Tree,
	commit plumbing.Hash,
	seenTrees map[plumbing.Hash]bool,
	seenBlobs map[plumbing.Hash]bool,
	objects map[string]int64,
) error {
	if seenTrees[tree.Hash] {
		return nil
	}
	seenTrees[tree.Hash] = true
	for _, entry := range tree.Entries {
		switch {
		case entry.Mode == filemode.Dir:
			subtree, err := repository.TreeObject(entry.Hash)
			if err != nil {
				return fmt.Errorf("load tree in commit %s: %w", commit, err)
			}
			if err = collectTreeLFSObjects(
				repository,
				subtree,
				commit,
				seenTrees,
				seenBlobs,
				objects,
			); err != nil {
				return err
			}
			continue
		case entry.Mode == filemode.Submodule:
			continue
		case !entry.Mode.IsFile():
			return fmt.Errorf("invalid file mode %s in commit %s", entry.Mode, commit)
		}
		if seenBlobs[entry.Hash] {
			continue
		}
		seenBlobs[entry.Hash] = true
		blob, err := repository.BlobObject(entry.Hash)
		if err != nil {
			return fmt.Errorf("load blob in commit %s: %w", commit, err)
		}
		if blob.Size > lfspointer.MaxPointerSize {
			continue
		}
		reader, err := blob.Reader()
		if err != nil {
			return fmt.Errorf("open blob in commit %s: %w", commit, err)
		}
		content, readErr := io.ReadAll(io.LimitReader(reader, lfspointer.MaxPointerSize+1))
		closeErr := reader.Close()
		if readErr != nil {
			return fmt.Errorf("read blob in commit %s: %w", commit, readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close blob in commit %s: %w", commit, closeErr)
		}
		pointer, ok := lfspointer.Parse(content)
		if !ok {
			continue
		}
		if size, exists := objects[pointer.OID]; exists && size != pointer.Size {
			return fmt.Errorf("LFS object %s is referenced with conflicting sizes", pointer.OID)
		}
		objects[pointer.OID] = pointer.Size
	}
	return nil
}
