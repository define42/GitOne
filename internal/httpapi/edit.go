package httpapi

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/gitformat"
	"github.com/define42/GitOne/internal/lockmgr"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func (a API) updateRepositoryFile(
	ctx context.Context,
	input *updateRepositoryFileInput,
) (*updateRepositoryFileOutput, error) {
	cleanPath, err := cleanRepositoryPath(input.Path)
	if err != nil || cleanPath == "" {
		return nil, huma.Error400BadRequest("invalid file path", err)
	}
	content, err := validatedRepositoryFileContent(input.Body.Content)
	if err != nil {
		return nil, err
	}
	releaseOperation, err := a.acquireRepositoryOperationLock(input.Repository)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	change, err := a.prepareRepositoryFileChange(
		ctx,
		input.AuthInput,
		input.Repository,
		input.Ref,
		input.Body.ExpectedCommit,
	)
	if err != nil {
		return nil, err
	}
	entry, err := change.rootTree.FindEntry(cleanPath)
	if err != nil {
		return nil, huma.Error404NotFound("file not found", err)
	}
	if !mergeableTextMode(entry.Mode) {
		return nil, huma.Error400BadRequest("only regular or executable files can be edited")
	}

	blobHash, err := storeRepositoryBlob(change.repository, content)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not store file contents", err)
	}
	treeHash, err := replaceRepositoryTreeBlob(
		change.repository,
		change.rootTree,
		strings.Split(cleanPath, "/"),
		blobHash,
	)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not update repository tree", err)
	}

	return a.commitRepositoryFileChange(
		input.AuthInput,
		change,
		treeHash,
		cleanPath,
		"",
		"updated",
		input.Body.Message,
		"Update "+cleanPath,
	)
}

func (a API) createRepositoryFile(
	ctx context.Context,
	input *createRepositoryFileInput,
) (*updateRepositoryFileOutput, error) {
	cleanPath, err := cleanRepositoryPath(input.Path)
	if err != nil || cleanPath == "" {
		return nil, huma.Error400BadRequest("invalid file path", err)
	}
	content, err := validatedRepositoryFileContent(input.Body.Content)
	if err != nil {
		return nil, err
	}
	releaseOperation, err := a.acquireRepositoryOperationLock(input.Repository)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	change, err := a.prepareRepositoryFileChange(
		ctx,
		input.AuthInput,
		input.Repository,
		input.Ref,
		input.Body.ExpectedCommit,
	)
	if err != nil {
		return nil, err
	}
	if _, err = change.rootTree.FindEntry(cleanPath); err == nil {
		return nil, huma.Error409Conflict("file already exists")
	} else if !repositoryTreePathMissing(err) {
		return nil, huma.Error500InternalServerError("could not inspect repository path", err)
	}
	blobHash, err := storeRepositoryBlob(change.repository, content)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not store file contents", err)
	}
	treeHash, err := addRepositoryTreeEntry(
		change.repository,
		change.rootTree,
		strings.Split(cleanPath, "/"),
		object.TreeEntry{Mode: filemode.Regular, Hash: blobHash},
	)
	if err != nil {
		return nil, huma.Error409Conflict("could not create file", err)
	}
	return a.commitRepositoryFileChange(
		input.AuthInput,
		change,
		treeHash,
		cleanPath,
		"",
		"created",
		input.Body.Message,
		"Create "+cleanPath,
	)
}

func (a API) deleteRepositoryFile(
	ctx context.Context,
	input *deleteRepositoryFileInput,
) (*updateRepositoryFileOutput, error) {
	cleanPath, err := cleanRepositoryPath(input.Path)
	if err != nil || cleanPath == "" {
		return nil, huma.Error400BadRequest("invalid file path", err)
	}
	releaseOperation, err := a.acquireRepositoryOperationLock(input.Repository)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	change, err := a.prepareRepositoryFileChange(
		ctx,
		input.AuthInput,
		input.Repository,
		input.Ref,
		input.Body.ExpectedCommit,
	)
	if err != nil {
		return nil, err
	}
	entry, err := change.rootTree.FindEntry(cleanPath)
	if err != nil {
		return nil, huma.Error404NotFound("file not found", err)
	}
	if !entry.Mode.IsFile() {
		return nil, huma.Error400BadRequest("path is not a file")
	}
	treeHash, _, _, err := removeRepositoryTreeEntry(
		change.repository,
		change.rootTree,
		strings.Split(cleanPath, "/"),
	)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not delete repository file", err)
	}
	return a.commitRepositoryFileChange(
		input.AuthInput,
		change,
		treeHash,
		cleanPath,
		"",
		"deleted",
		input.Body.Message,
		"Delete "+cleanPath,
	)
}

func (a API) renameRepositoryFile(
	ctx context.Context,
	input *renameRepositoryFileInput,
) (*updateRepositoryFileOutput, error) {
	cleanPath, err := cleanRepositoryPath(input.Path)
	if err != nil || cleanPath == "" {
		return nil, huma.Error400BadRequest("invalid file path", err)
	}
	newPath, err := cleanRepositoryPath(input.Body.NewPath)
	if err != nil || newPath == "" {
		return nil, huma.Error400BadRequest("invalid new file path", err)
	}
	if cleanPath == newPath {
		return nil, huma.Error400BadRequest("new file path must be different")
	}
	releaseOperation, err := a.acquireRepositoryOperationLock(input.Repository)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()
	change, err := a.prepareRepositoryFileChange(
		ctx,
		input.AuthInput,
		input.Repository,
		input.Ref,
		input.Body.ExpectedCommit,
	)
	if err != nil {
		return nil, err
	}
	entry, err := change.rootTree.FindEntry(cleanPath)
	if err != nil {
		return nil, huma.Error404NotFound("file not found", err)
	}
	if !entry.Mode.IsFile() {
		return nil, huma.Error400BadRequest("path is not a file")
	}
	if _, err = change.rootTree.FindEntry(newPath); err == nil {
		return nil, huma.Error409Conflict("destination path already exists")
	} else if !repositoryTreePathMissing(err) {
		return nil, huma.Error500InternalServerError("could not inspect destination path", err)
	}
	removedTreeHash, removed, _, err := removeRepositoryTreeEntry(
		change.repository,
		change.rootTree,
		strings.Split(cleanPath, "/"),
	)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not remove original file", err)
	}
	removedTree, err := change.repository.TreeObject(removedTreeHash)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load renamed repository tree", err)
	}
	treeHash, err := addRepositoryTreeEntry(
		change.repository,
		removedTree,
		strings.Split(newPath, "/"),
		removed,
	)
	if err != nil {
		return nil, huma.Error409Conflict("could not create renamed file", err)
	}
	return a.commitRepositoryFileChange(
		input.AuthInput,
		change,
		treeHash,
		newPath,
		cleanPath,
		"renamed",
		input.Body.Message,
		"Rename "+cleanPath+" to "+newPath,
	)
}

type repositoryFileChange struct {
	repository *git.Repository
	parsed     repopath.Repository
	branchName string
	branchRef  *plumbing.Reference
	parent     *object.Commit
	rootTree   *object.Tree
}

func (a API) acquireRepositoryOperationLock(value string) (func() error, error) {
	repository, err := parseRepositoryPath(value)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	return a.acquireRepositoryOperationLocks(repository)
}

func (a API) acquireRepositoryOperationLocks(
	repositories ...repopath.Repository,
) (func() error, error) {
	release, err := lockmgr.Process.Acquire(
		lockmgr.RepositoryRequests(a.Storage.Root, repositories, lockmgr.Exclusive)...,
	)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"could not lock repository operations",
			err,
		)
	}
	return func() error {
		release()
		return nil
	}, nil
}

func (a API) acquireGroupOperationLocks(groups ...string) (func() error, error) {
	release, err := lockmgr.Process.Acquire(
		lockmgr.GroupRequests(a.Storage.Root, groups, lockmgr.Exclusive)...,
	)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not lock group operations", err)
	}
	return func() error {
		release()
		return nil
	}, nil
}

func (a API) prepareRepositoryFileChange(
	ctx context.Context,
	credentials AuthInput,
	repositoryPath string,
	ref string,
	expectedCommit string,
) (*repositoryFileChange, error) {
	repository, parsed, err := a.openRepository(
		ctx,
		credentials,
		repositoryPath,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
	}
	branchName, branchRef, parent, err := resolveBranch(repository, ref)
	if err != nil {
		return nil, huma.Error404NotFound("branch not found", err)
	}
	if !gitformat.IsSHA256OID(expectedCommit) {
		return nil, huma.Error400BadRequest(
			"expectedCommit must be a complete lowercase SHA-256 commit hash",
		)
	}
	if branchRef.Hash().String() != expectedCommit {
		return nil, huma.Error409Conflict("branch changed since the file view was loaded")
	}
	rootTree, err := parent.Tree()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load repository tree", err)
	}
	return &repositoryFileChange{
		repository: repository,
		parsed:     parsed,
		branchName: branchName,
		branchRef:  branchRef,
		parent:     parent,
		rootTree:   rootTree,
	}, nil
}

func validatedRepositoryFileContent(value string) ([]byte, error) {
	content := []byte(value)
	if len(content) > maxEditableBlobSize {
		return nil, huma.Error413RequestEntityTooLarge(
			fmt.Sprintf("file exceeds the %d-byte editing limit", maxEditableBlobSize),
		)
	}
	if !utf8.Valid(content) || containsBinaryData(content) {
		return nil, huma.Error400BadRequest("only UTF-8 text files can be edited")
	}
	return content, nil
}

func repositoryTreePathMissing(err error) bool {
	return errors.Is(err, object.ErrEntryNotFound) ||
		errors.Is(err, object.ErrDirectoryNotFound)
}

func (a API) commitRepositoryFileChange(
	credentials AuthInput,
	change *repositoryFileChange,
	treeHash plumbing.Hash,
	path string,
	previousPath string,
	operation string,
	requestedMessage string,
	defaultMessage string,
) (*updateRepositoryFileOutput, error) {
	message := strings.TrimSpace(requestedMessage)
	if message == "" {
		message = defaultMessage
	}
	author, err := a.credentialUsername(credentials)
	if err != nil {
		return nil, huma.Error401Unauthorized("valid credentials are required")
	}
	signature := object.Signature{
		Name:  author,
		Email: author + "@localhost",
		When:  time.Now().UTC(),
	}
	commit := &object.Commit{
		Author:       signature,
		Committer:    signature,
		Message:      message + "\n",
		TreeHash:     treeHash,
		ParentHashes: []plumbing.Hash{change.parent.Hash},
	}
	encodedCommit := change.repository.Storer.NewEncodedObject()
	if err = commit.Encode(encodedCommit); err != nil {
		return nil, huma.Error500InternalServerError("could not encode file commit", err)
	}
	commitHash, err := change.repository.Storer.SetEncodedObject(encodedCommit)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not store file commit", err)
	}
	updated := plumbing.NewHashReference(change.branchRef.Name(), commitHash)
	if err = change.repository.Storer.CheckAndSetReference(updated, change.branchRef); err != nil {
		return nil, huma.Error409Conflict("branch changed while the file was being saved", err)
	}
	a.scheduleBuild(change.parsed, change.branchName, commitHash)

	output := &updateRepositoryFileOutput{}
	output.Body.Repository = change.parsed.Full()
	output.Body.Branch = change.branchName
	output.Body.Path = path
	output.Body.PreviousPath = previousPath
	output.Body.Operation = operation
	output.Body.Commit = commitHash.String()
	output.Body.PreviousCommit = change.parent.Hash.String()
	output.Body.Message = message
	return output, nil
}

func storeRepositoryBlob(repository *git.Repository, content []byte) (plumbing.Hash, error) {
	blob := repository.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(content)))
	writer, err := blob.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err = writer.Write(content); err != nil {
		_ = writer.Close()
		return plumbing.ZeroHash, err
	}
	if err = writer.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return repository.Storer.SetEncodedObject(blob)
}

func replaceRepositoryTreeBlob(
	repository *git.Repository,
	tree *object.Tree,
	parts []string,
	blobHash plumbing.Hash,
) (plumbing.Hash, error) {
	entries := append([]object.TreeEntry(nil), tree.Entries...)
	for index := range entries {
		if entries[index].Name != parts[0] {
			continue
		}
		if len(parts) == 1 {
			if !mergeableTextMode(entries[index].Mode) {
				return plumbing.ZeroHash, fmt.Errorf("path is not an editable file")
			}
			entries[index].Hash = blobHash
		} else {
			if entries[index].Mode != filemode.Dir {
				return plumbing.ZeroHash, fmt.Errorf("path component %q is not a directory", parts[0])
			}
			subtree, err := repository.TreeObject(entries[index].Hash)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			subtreeHash, err := replaceRepositoryTreeBlob(repository, subtree, parts[1:], blobHash)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			entries[index].Hash = subtreeHash
		}
		sort.Sort(object.TreeEntrySorter(entries))
		updatedTree := &object.Tree{Entries: entries}
		encodedTree := repository.Storer.NewEncodedObject()
		if err := updatedTree.Encode(encodedTree); err != nil {
			return plumbing.ZeroHash, err
		}
		return repository.Storer.SetEncodedObject(encodedTree)
	}
	return plumbing.ZeroHash, fmt.Errorf("path component %q was not found", parts[0])
}

func addRepositoryTreeEntry(
	repository *git.Repository,
	tree *object.Tree,
	parts []string,
	entry object.TreeEntry,
) (plumbing.Hash, error) {
	if len(parts) == 0 || parts[0] == "" {
		return plumbing.ZeroHash, errors.New("file path is empty")
	}
	entries := append([]object.TreeEntry(nil), tree.Entries...)
	found := -1
	for index := range entries {
		if entries[index].Name == parts[0] {
			found = index
			break
		}
	}
	if len(parts) == 1 {
		if found >= 0 {
			return plumbing.ZeroHash, fmt.Errorf("path %q already exists", parts[0])
		}
		entry.Name = parts[0]
		entries = append(entries, entry)
		return storeRepositoryTree(repository, entries)
	}

	var subtree *object.Tree
	if found >= 0 {
		if entries[found].Mode != filemode.Dir {
			return plumbing.ZeroHash, fmt.Errorf("path component %q is not a directory", parts[0])
		}
		var err error
		subtree, err = repository.TreeObject(entries[found].Hash)
		if err != nil {
			return plumbing.ZeroHash, err
		}
	} else {
		subtree = &object.Tree{}
	}
	subtreeHash, err := addRepositoryTreeEntry(repository, subtree, parts[1:], entry)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if found >= 0 {
		entries[found].Hash = subtreeHash
	} else {
		entries = append(entries, object.TreeEntry{
			Name: parts[0],
			Mode: filemode.Dir,
			Hash: subtreeHash,
		})
	}
	return storeRepositoryTree(repository, entries)
}

func removeRepositoryTreeEntry(
	repository *git.Repository,
	tree *object.Tree,
	parts []string,
) (plumbing.Hash, object.TreeEntry, bool, error) {
	if len(parts) == 0 || parts[0] == "" {
		return plumbing.ZeroHash, object.TreeEntry{}, false, errors.New("file path is empty")
	}
	entries := append([]object.TreeEntry(nil), tree.Entries...)
	found := -1
	for index := range entries {
		if entries[index].Name == parts[0] {
			found = index
			break
		}
	}
	if found < 0 {
		return plumbing.ZeroHash, object.TreeEntry{}, false, fmt.Errorf(
			"path component %q was not found",
			parts[0],
		)
	}
	if len(parts) == 1 {
		if !entries[found].Mode.IsFile() {
			return plumbing.ZeroHash, object.TreeEntry{}, false, errors.New(
				"path is not a file",
			)
		}
		removed := entries[found]
		entries = append(entries[:found], entries[found+1:]...)
		hash, err := storeRepositoryTree(repository, entries)
		return hash, removed, len(entries) == 0, err
	}
	if entries[found].Mode != filemode.Dir {
		return plumbing.ZeroHash, object.TreeEntry{}, false, fmt.Errorf(
			"path component %q is not a directory",
			parts[0],
		)
	}
	subtree, err := repository.TreeObject(entries[found].Hash)
	if err != nil {
		return plumbing.ZeroHash, object.TreeEntry{}, false, err
	}
	subtreeHash, removed, subtreeEmpty, err := removeRepositoryTreeEntry(
		repository,
		subtree,
		parts[1:],
	)
	if err != nil {
		return plumbing.ZeroHash, object.TreeEntry{}, false, err
	}
	if subtreeEmpty {
		entries = append(entries[:found], entries[found+1:]...)
	} else {
		entries[found].Hash = subtreeHash
	}
	hash, err := storeRepositoryTree(repository, entries)
	return hash, removed, len(entries) == 0, err
}

func storeRepositoryTree(
	repository *git.Repository,
	entries []object.TreeEntry,
) (plumbing.Hash, error) {
	sort.Sort(object.TreeEntrySorter(entries))
	updatedTree := &object.Tree{Entries: entries}
	encodedTree := repository.Storer.NewEncodedObject()
	if err := updatedTree.Encode(encodedTree); err != nil {
		return plumbing.ZeroHash, err
	}
	return repository.Storer.SetEncodedObject(encodedTree)
}
