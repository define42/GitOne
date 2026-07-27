package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	pathpkg "path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	maxBrowsableBlobSize   = 10 * 1024 * 1024
	maxHighlightedBlobSize = 1024 * 1024
	maxEditableBlobSize    = 1024 * 1024
)

type repositoryBranchesInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
}

type createRepositoryBranchInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Branch     string `path:"branch" doc:"URL-encoded name for the new branch"`
	From       string `query:"from" doc:"Existing branch from which the new branch is created"`
}

type compareRepositoryBranchesInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Base       string `query:"base" required:"true" doc:"Target branch receiving the merge"`
	Head       string `query:"head" required:"true" doc:"Source branch being compared"`
}

type mergeRepositoryBranchesBody struct {
	Target  string `json:"target" minLength:"1" doc:"Branch receiving the merge"`
	Source  string `json:"source" minLength:"1" doc:"Branch merged into the target"`
	Message string `json:"message,omitempty" doc:"Optional merge commit message"`
}

type mergeRepositoryBranchesInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Body       mergeRepositoryBranchesBody
}

type repositoryBrowserRefInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch, tag, hash, or HEAD"`
}

type repositoryBrowserPathInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch, tag, hash, or HEAD"`
	Path       string `path:"path" doc:"URL-encoded path inside the repository"`
}

type updateRepositoryFileBody struct {
	Content        string `json:"content" maxLength:"1048576" doc:"Complete UTF-8 file contents"`
	Message        string `json:"message,omitempty" maxLength:"500" doc:"Optional commit message"`
	ExpectedCommit string `json:"expectedCommit" minLength:"40" maxLength:"40" doc:"Branch tip commit shown when editing began"`
}

type updateRepositoryFileInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch receiving the commit"`
	Path       string `path:"path" doc:"URL-encoded path inside the repository"`
	Body       updateRepositoryFileBody
}

type repositoryCommitsInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch, tag, hash, or HEAD"`
	Limit      int    `query:"limit" minimum:"1" maximum:"100" default:"20"`
}

type repositoryCommitDiffInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Commit     string `path:"commit" doc:"Complete commit hash"`
}

type repositoryTreeEntry struct {
	Name string `json:"name" doc:"Entry name"`
	Path string `json:"path" doc:"Full path inside the repository"`
	Type string `json:"type" enum:"file,directory,submodule"`
	Mode string `json:"mode" doc:"Git file mode"`
	Hash string `json:"hash" doc:"Git object hash"`
	Size int64  `json:"size,omitempty" doc:"File size in bytes"`
}

type repositoryBranch struct {
	Name   string `json:"name" doc:"Branch name"`
	Commit string `json:"commit" doc:"Commit hash at the branch tip"`
}

type repositoryBranchesOutput struct {
	Body struct {
		Repository    string             `json:"repository"`
		DefaultBranch string             `json:"defaultBranch"`
		Branches      []repositoryBranch `json:"branches"`
	}
}

type createRepositoryBranchOutput struct {
	Body struct {
		Repository string `json:"repository"`
		Name       string `json:"name"`
		From       string `json:"from"`
		Commit     string `json:"commit"`
	}
}

type repositoryTreeOutput struct {
	Body struct {
		Repository string                `json:"repository"`
		Ref        string                `json:"ref"`
		Commit     string                `json:"commit"`
		Path       string                `json:"path"`
		Entries    []repositoryTreeEntry `json:"entries"`
	}
}

type repositoryBlobOutput struct {
	Body struct {
		Repository      string `json:"repository"`
		Ref             string `json:"ref"`
		Commit          string `json:"commit"`
		Path            string `json:"path"`
		Hash            string `json:"hash"`
		Size            int64  `json:"size"`
		Encoding        string `json:"encoding" enum:"utf-8,base64"`
		Content         string `json:"content"`
		Language        string `json:"language,omitempty"`
		HighlightedHTML string `json:"highlightedHtml,omitempty"`
		CanEdit         bool   `json:"canEdit" doc:"Whether this file and reference can be edited by the authenticated user"`
	}
}

type updateRepositoryFileOutput struct {
	Body struct {
		Repository     string `json:"repository"`
		Branch         string `json:"branch"`
		Path           string `json:"path"`
		Commit         string `json:"commit"`
		PreviousCommit string `json:"previousCommit"`
		Message        string `json:"message"`
	}
}

type repositoryCommitInfo struct {
	Hash      string    `json:"hash"`
	Author    string    `json:"author"`
	Email     string    `json:"email"`
	Authored  time.Time `json:"authored"`
	Committer string    `json:"committer"`
	Committed time.Time `json:"committed"`
	Message   string    `json:"message"`
}

type repositoryCommitsOutput struct {
	Body struct {
		Repository string                 `json:"repository"`
		Ref        string                 `json:"ref"`
		Commits    []repositoryCommitInfo `json:"commits"`
	}
}

type repositoryCommitDiffOutput struct {
	Body struct {
		Repository string                     `json:"repository"`
		Commit     string                     `json:"commit"`
		Parent     string                     `json:"parent,omitempty"`
		Files      []repositoryComparisonFile `json:"files"`
	}
}

type repositoryComparisonFile struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Status    string `json:"status" enum:"added,deleted,modified,renamed"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	Patch     string `json:"patch,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

type compareRepositoryBranchesOutput struct {
	Body struct {
		Repository string                     `json:"repository"`
		Base       string                     `json:"base"`
		Head       string                     `json:"head"`
		BaseCommit string                     `json:"baseCommit"`
		HeadCommit string                     `json:"headCommit"`
		MergeBase  string                     `json:"mergeBase,omitempty"`
		Ahead      int                        `json:"ahead"`
		Behind     int                        `json:"behind"`
		Mergeable  bool                       `json:"mergeable"`
		CanMerge   bool                       `json:"canMerge"`
		Conflicts  []string                   `json:"conflicts"`
		Files      []repositoryComparisonFile `json:"files"`
	}
}

type mergeRepositoryBranchesOutput struct {
	Body struct {
		Repository string `json:"repository"`
		Target     string `json:"target"`
		Source     string `json:"source"`
		Commit     string `json:"commit"`
		Strategy   string `json:"strategy" enum:"already-up-to-date,fast-forward,merge-commit"`
	}
}

func registerRepositoryBrowser(api huma.API, service API) {
	huma.Register(api, protected(huma.Operation{
		OperationID: "list-repository-branches",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/branches",
		Summary:     "List repository branches",
		Tags:        []string{"Repository browser"},
	}), service.listRepositoryBranches)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-repository-branch",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/branches/{branch}",
		Summary:       "Create a repository branch",
		Tags:          []string{"Repository browser"},
		DefaultStatus: http.StatusCreated,
	}), service.createRepositoryBranch)

	huma.Register(api, protected(huma.Operation{
		OperationID: "compare-repository-branches",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/compare",
		Summary:     "Compare two repository branches",
		Tags:        []string{"Repository browser"},
	}), service.compareRepositoryBranches)

	huma.Register(api, protected(huma.Operation{
		OperationID: "merge-repository-branches",
		Method:      http.MethodPost,
		Path:        "/api/repositories/{repository}/merges",
		Summary:     "Merge a source branch into a target branch",
		Tags:        []string{"Repository browser"},
	}), service.mergeRepositoryBranches)

	huma.Register(api, protected(huma.Operation{
		OperationID: "list-repository-root",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/tree/{ref}",
		Summary:     "List a repository root",
		Tags:        []string{"Repository browser"},
	}), service.listRepositoryRoot)

	huma.Register(api, protected(huma.Operation{
		OperationID: "list-repository-directory",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/tree/{ref}/{path}",
		Summary:     "List a repository directory",
		Tags:        []string{"Repository browser"},
	}), service.listRepositoryDirectory)

	huma.Register(api, protected(huma.Operation{
		OperationID: "read-repository-file",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/blob/{ref}/{path}",
		Summary:     "Read a file from a repository",
		Tags:        []string{"Repository browser"},
	}), service.readRepositoryBlob)

	huma.Register(api, protected(huma.Operation{
		OperationID: "update-repository-file",
		Method:      http.MethodPut,
		Path:        "/api/repositories/{repository}/files/{ref}/{path}",
		Summary:     "Update a file and commit it to a branch",
		Tags:        []string{"Repository browser"},
	}), service.updateRepositoryFile)

	huma.Register(api, protected(huma.Operation{
		OperationID: "list-repository-commits",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/commits/{ref}",
		Summary:     "List repository commits",
		Tags:        []string{"Repository browser"},
	}), service.listRepositoryCommits)

	huma.Register(api, protected(huma.Operation{
		OperationID: "read-repository-commit-diff",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/commits/{commit}/diff",
		Summary:     "Read the changes introduced by a commit",
		Tags:        []string{"Repository browser"},
	}), service.readRepositoryCommitDiff)
}

func (a API) listRepositoryBranches(ctx context.Context, input *repositoryBranchesInput) (*repositoryBranchesOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, input.Authorization, input.Repository)
	if err != nil {
		return nil, err
	}
	iterator, err := repository.Branches()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not read repository branches", err)
	}
	defer iterator.Close()

	branches := make([]repositoryBranch, 0)
	err = iterator.ForEach(func(reference *plumbing.Reference) error {
		branches = append(branches, repositoryBranch{
			Name:   reference.Name().Short(),
			Commit: reference.Hash().String(),
		})
		return nil
	})
	if err != nil {
		return nil, huma.Error500InternalServerError("could not iterate over repository branches", err)
	}
	sort.Slice(branches, func(left, right int) bool {
		return branches[left].Name < branches[right].Name
	})

	output := &repositoryBranchesOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.DefaultBranch = "main"
	output.Body.Branches = branches
	return output, nil
}

func (a API) createRepositoryBranch(ctx context.Context, input *createRepositoryBranchInput) (*createRepositoryBranchOutput, error) {
	repository, parsed, err := a.openRepository(ctx, input.Authorization, input.Repository, control.RoleWrite)
	if err != nil {
		return nil, err
	}
	branchName, err := validatedBranchReference(input.Branch)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid new branch name", err)
	}
	sourceName, err := validatedBranchReference(input.From)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid source branch name", err)
	}
	if _, err = repository.Reference(branchName, false); err == nil {
		return nil, huma.Error409Conflict("branch already exists")
	} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, huma.Error500InternalServerError("could not check branch", err)
	}
	source, err := repository.Reference(sourceName, true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, huma.Error404NotFound("source branch not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("could not read source branch", err)
	}
	if _, err = repository.CommitObject(source.Hash()); err != nil {
		return nil, huma.Error409Conflict("source branch does not point to a commit", err)
	}
	if err = repository.Storer.SetReference(plumbing.NewHashReference(branchName, source.Hash())); err != nil {
		return nil, huma.Error409Conflict("could not create branch", err)
	}

	output := &createRepositoryBranchOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Name = input.Branch
	output.Body.From = input.From
	output.Body.Commit = source.Hash().String()
	return output, nil
}

func (a API) listRepositoryRoot(ctx context.Context, input *repositoryBrowserRefInput) (*repositoryTreeOutput, error) {
	return a.listRepositoryTree(ctx, input.Authorization, input.Repository, input.Ref, "")
}

func (a API) listRepositoryDirectory(ctx context.Context, input *repositoryBrowserPathInput) (*repositoryTreeOutput, error) {
	return a.listRepositoryTree(ctx, input.Authorization, input.Repository, input.Ref, input.Path)
}

func (a API) listRepositoryTree(ctx context.Context, authorization, repositoryPath, ref, treePath string) (*repositoryTreeOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, authorization, repositoryPath)
	if err != nil {
		return nil, err
	}
	commit, err := resolveBrowserCommit(repository, ref)
	if err != nil {
		return nil, huma.Error404NotFound("Git reference not found", err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load Git tree", err)
	}
	cleanPath, err := cleanRepositoryPath(treePath)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid repository path", err)
	}
	if cleanPath != "" {
		tree, err = tree.Tree(cleanPath)
		if err != nil {
			return nil, huma.Error404NotFound("directory not found", err)
		}
	}

	entries := make([]repositoryTreeEntry, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		entryPath := entry.Name
		if cleanPath != "" {
			entryPath = cleanPath + "/" + entry.Name
		}
		item := repositoryTreeEntry{
			Name: entry.Name,
			Path: entryPath,
			Type: repositoryEntryType(entry),
			Mode: entry.Mode.String(),
			Hash: entry.Hash.String(),
		}
		if item.Type == "file" {
			if blob, blobErr := repository.BlobObject(entry.Hash); blobErr == nil {
				item.Size = blob.Size
			}
		}
		entries = append(entries, item)
	}

	output := &repositoryTreeOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Ref = ref
	output.Body.Commit = commit.Hash.String()
	output.Body.Path = cleanPath
	output.Body.Entries = entries
	return output, nil
}

func (a API) readRepositoryBlob(ctx context.Context, input *repositoryBrowserPathInput) (*repositoryBlobOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, input.Authorization, input.Repository)
	if err != nil {
		return nil, err
	}
	commit, err := resolveBrowserCommit(repository, input.Ref)
	if err != nil {
		return nil, huma.Error404NotFound("Git reference not found", err)
	}
	cleanPath, err := cleanRepositoryPath(input.Path)
	if err != nil || cleanPath == "" {
		return nil, huma.Error400BadRequest("invalid file path", err)
	}
	file, err := commit.File(cleanPath)
	if err != nil {
		return nil, huma.Error404NotFound("file not found", err)
	}
	if file.Blob.Size > maxBrowsableBlobSize {
		return nil, huma.Error413RequestEntityTooLarge(
			fmt.Sprintf("file exceeds the %d-byte browsing limit", maxBrowsableBlobSize),
		)
	}
	reader, err := file.Blob.Reader()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not open Git blob", err)
	}
	defer reader.Close()
	content, err := io.ReadAll(io.LimitReader(reader, maxBrowsableBlobSize+1))
	if err != nil {
		return nil, huma.Error500InternalServerError("could not read Git blob", err)
	}

	encoding := "utf-8"
	encodedContent := string(content)
	if !utf8.Valid(content) || containsBinaryData(content) {
		encoding = "base64"
		encodedContent = base64.StdEncoding.EncodeToString(content)
	}

	output := &repositoryBlobOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Ref = input.Ref
	output.Body.Commit = commit.Hash.String()
	output.Body.Path = cleanPath
	output.Body.Hash = file.Blob.Hash.String()
	output.Body.Size = file.Blob.Size
	output.Body.Encoding = encoding
	output.Body.Content = encodedContent
	if encoding == "utf-8" {
		output.Body.HighlightedHTML, output.Body.Language = highlightRepositoryBlob(
			cleanPath,
			encodedContent,
		)
		branchName, branchRef, _, branchErr := resolveBranch(repository, input.Ref)
		_, writeErr := a.authorize(ctx, input.Authorization, parsed.Group(), control.RoleWrite)
		output.Body.CanEdit = branchErr == nil &&
			branchName == input.Ref &&
			branchRef.Hash() == commit.Hash &&
			writeErr == nil &&
			file.Blob.Size <= maxEditableBlobSize &&
			mergeableTextMode(file.Mode)
	}
	return output, nil
}

func highlightRepositoryBlob(path, content string) (string, string) {
	if len(content) > maxHighlightedBlobSize {
		return "", ""
	}
	lexer := lexers.Match(path)
	if lexer == nil || strings.EqualFold(lexer.Config().Name, "plaintext") {
		return "", ""
	}
	language := lexer.Config().Name
	iterator, err := chroma.Coalesce(lexer).Tokenise(nil, content)
	if err != nil {
		return "", ""
	}
	style := styles.Get("github-dark")
	if style == nil {
		style = styles.Fallback
	}
	formatter := chroma.RecoveringFormatter(chromahtml.New(
		chromahtml.TabWidth(4),
	))
	var highlighted bytes.Buffer
	if err = formatter.Format(&highlighted, style, iterator); err != nil {
		return "", ""
	}
	return highlighted.String(), language
}

func (a API) listRepositoryCommits(ctx context.Context, input *repositoryCommitsInput) (*repositoryCommitsOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, input.Authorization, input.Repository)
	if err != nil {
		return nil, err
	}
	commit, err := resolveBrowserCommit(repository, input.Ref)
	if err != nil {
		return nil, huma.Error404NotFound("Git reference not found", err)
	}
	iterator, err := repository.Log(&git.LogOptions{From: commit.Hash})
	if err != nil {
		return nil, huma.Error500InternalServerError("could not read commit history", err)
	}
	defer iterator.Close()

	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	commits := make([]repositoryCommitInfo, 0, limit)
	stop := errors.New("commit limit reached")
	err = iterator.ForEach(func(current *object.Commit) error {
		if len(commits) >= limit {
			return stop
		}
		commits = append(commits, repositoryCommitInfo{
			Hash:      current.Hash.String(),
			Author:    current.Author.Name,
			Email:     current.Author.Email,
			Authored:  current.Author.When,
			Committer: current.Committer.Name,
			Committed: current.Committer.When,
			Message:   current.Message,
		})
		return nil
	})
	if err != nil && !errors.Is(err, stop) {
		return nil, huma.Error500InternalServerError("could not iterate over commits", err)
	}

	output := &repositoryCommitsOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Ref = input.Ref
	output.Body.Commits = commits
	return output, nil
}

func (a API) openBrowsableRepository(ctx context.Context, authorization, value string) (*git.Repository, repopath.Repository, error) {
	return a.openRepository(ctx, authorization, value, control.RoleRead)
}

func (a API) openRepository(ctx context.Context, authorization, value string, role control.Role) (*git.Repository, repopath.Repository, error) {
	parsed, err := parseRepositoryPath(value)
	if err != nil {
		return nil, repopath.Repository{}, huma.Error400BadRequest(err.Error())
	}
	if _, err = a.authorize(ctx, authorization, parsed.Group(), role); err != nil {
		return nil, repopath.Repository{}, err
	}
	repositoryPath, err := a.Storage.GitPath(parsed)
	if err != nil {
		return nil, repopath.Repository{}, huma.Error400BadRequest(err.Error())
	}
	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		return nil, repopath.Repository{}, huma.Error404NotFound("repository not found", err)
	}
	return repository, parsed, nil
}

func validatedBranchReference(value string) (plumbing.ReferenceName, error) {
	if value == "" || value != strings.TrimSpace(value) {
		return "", plumbing.ErrInvalidReferenceName
	}
	reference := plumbing.NewBranchReferenceName(value)
	if err := reference.Validate(); err != nil {
		return "", err
	}
	return reference, nil
}

func resolveBrowserCommit(repository *git.Repository, ref string) (*object.Commit, error) {
	for _, candidate := range []string{ref, "refs/heads/" + ref, "refs/tags/" + ref} {
		hash, err := repository.ResolveRevision(plumbing.Revision(candidate))
		if err != nil {
			continue
		}
		commit, err := repository.CommitObject(*hash)
		if err == nil {
			return commit, nil
		}
	}
	return nil, fmt.Errorf("revision %q does not exist", ref)
}

func cleanRepositoryPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "/")
	if value == "" {
		return "", nil
	}
	cleaned := pathpkg.Clean(value)
	if cleaned == "." {
		return "", nil
	}
	if cleaned == ".." ||
		strings.HasPrefix(cleaned, "../") ||
		strings.HasPrefix(cleaned, "/") ||
		strings.ContainsRune(cleaned, '\x00') {
		return "", fmt.Errorf("path traversal is not allowed")
	}
	return cleaned, nil
}

func repositoryEntryType(entry object.TreeEntry) string {
	switch {
	case entry.Mode == filemode.Submodule:
		return "submodule"
	case entry.Mode.IsFile():
		return "file"
	default:
		return "directory"
	}
}

func containsBinaryData(content []byte) bool {
	checkLength := min(len(content), 8*1024)
	for _, value := range content[:checkLength] {
		if value == 0 {
			return true
		}
	}
	return false
}
