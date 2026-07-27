package httpapi

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func (a API) updateRepositoryFile(
	ctx context.Context,
	input *updateRepositoryFileInput,
) (*updateRepositoryFileOutput, error) {
	repository, parsed, err := a.openRepository(
		ctx,
		input.Authorization,
		input.Repository,
		control.RoleWrite,
	)
	if err != nil {
		return nil, err
	}
	branchName, branchRef, parent, err := resolveBranch(repository, input.Ref)
	if err != nil {
		return nil, huma.Error404NotFound("branch not found", err)
	}
	if !plumbing.IsHash(input.Body.ExpectedCommit) {
		return nil, huma.Error400BadRequest("expectedCommit must be a complete commit hash")
	}
	if !strings.EqualFold(branchRef.Hash().String(), input.Body.ExpectedCommit) {
		return nil, huma.Error409Conflict("branch changed since the file was opened")
	}

	cleanPath, err := cleanRepositoryPath(input.Path)
	if err != nil || cleanPath == "" {
		return nil, huma.Error400BadRequest("invalid file path", err)
	}
	content := []byte(input.Body.Content)
	if len(content) > maxEditableBlobSize {
		return nil, huma.Error413RequestEntityTooLarge(
			fmt.Sprintf("file exceeds the %d-byte editing limit", maxEditableBlobSize),
		)
	}
	if !utf8.Valid(content) || containsBinaryData(content) {
		return nil, huma.Error400BadRequest("only UTF-8 text files can be edited")
	}

	rootTree, err := parent.Tree()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load repository tree", err)
	}
	entry, err := rootTree.FindEntry(cleanPath)
	if err != nil {
		return nil, huma.Error404NotFound("file not found", err)
	}
	if !mergeableTextMode(entry.Mode) {
		return nil, huma.Error400BadRequest("only regular or executable files can be edited")
	}

	blobHash, err := storeRepositoryBlob(repository, content)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not store file contents", err)
	}
	treeHash, err := replaceRepositoryTreeBlob(
		repository,
		rootTree,
		strings.Split(cleanPath, "/"),
		blobHash,
	)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not update repository tree", err)
	}

	message := strings.TrimSpace(input.Body.Message)
	if message == "" {
		message = "Update " + cleanPath
	}
	author, _, err := basicCredentials(input.Authorization)
	if err != nil {
		return nil, huma.Error401Unauthorized("valid HTTP Basic credentials are required")
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
		ParentHashes: []plumbing.Hash{parent.Hash},
	}
	encodedCommit := &plumbing.MemoryObject{}
	if err = commit.Encode(encodedCommit); err != nil {
		return nil, huma.Error500InternalServerError("could not encode file commit", err)
	}
	commitHash, err := repository.Storer.SetEncodedObject(encodedCommit)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not store file commit", err)
	}
	updated := plumbing.NewHashReference(branchRef.Name(), commitHash)
	if err = repository.Storer.CheckAndSetReference(updated, branchRef); err != nil {
		return nil, huma.Error409Conflict("branch changed while the file was being saved", err)
	}

	output := &updateRepositoryFileOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Branch = branchName
	output.Body.Path = cleanPath
	output.Body.Commit = commitHash.String()
	output.Body.PreviousCommit = parent.Hash.String()
	output.Body.Message = message
	return output, nil
}

func storeRepositoryBlob(repository *git.Repository, content []byte) (plumbing.Hash, error) {
	blob := &plumbing.MemoryObject{}
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
		encodedTree := &plumbing.MemoryObject{}
		if err := updatedTree.Encode(encodedTree); err != nil {
			return plumbing.ZeroHash, err
		}
		return repository.Storer.SetEncodedObject(encodedTree)
	}
	return plumbing.ZeroHash, fmt.Errorf("path component %q was not found", parts[0])
}
