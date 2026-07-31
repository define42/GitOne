package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
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
	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/lfs"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

const (
	maxBrowsableBlobSize     = 10 * 1024 * 1024
	maxHighlightedBlobSize   = 1024 * 1024
	maxEditableBlobSize      = 1024 * 1024
	maximumCommitHistoryWalk = 10_000
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

type updateRepositoryDefaultBranchBody struct {
	Branch string `json:"branch" minLength:"1" doc:"Existing branch that becomes the repository default"`
}

type updateRepositoryDefaultBranchInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Body       updateRepositoryDefaultBranchBody
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

type repositoryArchiveInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch, tag, hash, or HEAD"`
	Format     string `query:"format" enum:"zip,tar.gz" default:"zip" doc:"Archive format"`
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

type createRepositoryFileBody struct {
	Content        string `json:"content" maxLength:"1048576" doc:"Complete UTF-8 file contents"`
	Message        string `json:"message,omitempty" maxLength:"500" doc:"Optional commit message"`
	ExpectedCommit string `json:"expectedCommit" minLength:"40" maxLength:"40" doc:"Current branch tip commit"`
}

type createRepositoryFileInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch receiving the commit"`
	Path       string `path:"path" doc:"URL-encoded new path inside the repository"`
	Body       createRepositoryFileBody
}

type deleteRepositoryFileBody struct {
	Message        string `json:"message,omitempty" maxLength:"500" doc:"Optional commit message"`
	ExpectedCommit string `json:"expectedCommit" minLength:"40" maxLength:"40" doc:"Current branch tip commit"`
}

type deleteRepositoryFileInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch receiving the commit"`
	Path       string `path:"path" doc:"URL-encoded file path inside the repository"`
	Body       deleteRepositoryFileBody
}

type renameRepositoryFileBody struct {
	NewPath        string `json:"newPath" minLength:"1" doc:"New path inside the repository"`
	Message        string `json:"message,omitempty" maxLength:"500" doc:"Optional commit message"`
	ExpectedCommit string `json:"expectedCommit" minLength:"40" maxLength:"40" doc:"Current branch tip commit"`
}

type renameRepositoryFileInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch receiving the commit"`
	Path       string `path:"path" doc:"URL-encoded existing file path inside the repository"`
	Body       renameRepositoryFileBody
}

type repositoryCommitsInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	Ref        string `path:"ref" doc:"Git branch, tag, hash, or HEAD"`
	Page       int    `query:"page" minimum:"1" maximum:"1000000" default:"1" doc:"One-based result page"`
	PerPage    int    `query:"perPage" minimum:"1" maximum:"100" default:"50" doc:"Commits returned per page"`
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
	LFS  bool   `json:"lfs,omitempty" doc:"Whether the file is stored with Git LFS"`
}

type repositoryBranch struct {
	Name   string `json:"name" doc:"Branch name"`
	Commit string `json:"commit" doc:"Commit hash at the branch tip"`
}

type repositoryBranchesOutput struct {
	Body struct {
		Repository    string             `json:"repository"`
		DefaultBranch string             `json:"defaultBranch" doc:"Existing branch selected by symbolic HEAD; empty when HEAD is detached, tag-backed, or missing"`
		DefaultRef    string             `json:"defaultRef" doc:"Reference used when no branch is explicitly requested; empty only for repositories without a browsable commit"`
		CanWrite      bool               `json:"canWrite" doc:"Whether the authenticated user can create branches and merge requests"`
		CanManage     bool               `json:"canManage" doc:"Whether the authenticated user can change repository settings"`
		Branches      []repositoryBranch `json:"branches"`
	}
}

type updateRepositoryDefaultBranchOutput struct {
	Body struct {
		Repository    string `json:"repository"`
		DefaultBranch string `json:"defaultBranch"`
		DefaultRef    string `json:"defaultRef"`
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
		CanEdit    bool                  `json:"canEdit" doc:"Whether files can be changed on the selected reference"`
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
		CanManage       bool   `json:"canManage" doc:"Whether this file can be renamed or deleted on the selected reference"`
		LFS             bool   `json:"lfs,omitempty" doc:"Whether content was resolved from Git LFS storage"`
		LFSOID          string `json:"lfsOid,omitempty" doc:"SHA-256 object ID for resolved Git LFS content"`
	}
}

type updateRepositoryFileOutput struct {
	Body struct {
		Repository     string `json:"repository"`
		Branch         string `json:"branch"`
		Path           string `json:"path"`
		PreviousPath   string `json:"previousPath,omitempty"`
		Operation      string `json:"operation" enum:"created,updated,deleted,renamed"`
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
		Repository  string                 `json:"repository"`
		Ref         string                 `json:"ref"`
		Page        int                    `json:"page"`
		PerPage     int                    `json:"perPage"`
		Total       *int                   `json:"total,omitempty" doc:"Exact total when the end of history was reached"`
		TotalPages  *int                   `json:"totalPages,omitempty" doc:"Exact page count when the end of history was reached"`
		HasPrevious bool                   `json:"hasPrevious"`
		HasNext     bool                   `json:"hasNext"`
		Commits     []repositoryCommitInfo `json:"commits"`
	}
}

type repositoryBlameLine struct {
	Number   int       `json:"number"`
	Text     string    `json:"text"`
	Commit   string    `json:"commit"`
	Author   string    `json:"author"`
	Email    string    `json:"email"`
	Authored time.Time `json:"authored"`
}

type repositoryBlameOutput struct {
	Body struct {
		Repository string                `json:"repository"`
		Ref        string                `json:"ref"`
		Commit     string                `json:"commit"`
		Path       string                `json:"path"`
		Lines      []repositoryBlameLine `json:"lines"`
	}
}

type repositoryCommitDiffOutput struct {
	Body struct {
		Repository string                     `json:"repository"`
		Commit     string                     `json:"commit"`
		Parent     string                     `json:"parent,omitempty"`
		Files      []repositoryComparisonFile `json:"files"`
		Truncated  bool                       `json:"truncated,omitempty"`
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
		Truncated  bool                       `json:"truncated,omitempty"`
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
		OperationID: "update-repository-default-branch",
		Method:      http.MethodPut,
		Path:        "/api/repositories/{repository}/default-branch",
		Summary:     "Change the repository default branch",
		Tags:        []string{"Repository browser"},
	}), service.updateRepositoryDefaultBranch)

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
		OperationID: "download-repository-archive",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/archives/{ref}",
		Summary:     "Download a repository reference as an archive",
		Tags:        []string{"Repository browser"},
	}), service.downloadRepositoryArchive)

	huma.Register(api, protected(huma.Operation{
		OperationID:   "create-repository-file",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/files/{ref}/{path}",
		Summary:       "Create a file and commit it to a branch",
		Tags:          []string{"Repository browser"},
		DefaultStatus: http.StatusCreated,
	}), service.createRepositoryFile)

	huma.Register(api, protected(huma.Operation{
		OperationID: "update-repository-file",
		Method:      http.MethodPut,
		Path:        "/api/repositories/{repository}/files/{ref}/{path}",
		Summary:     "Update a file and commit it to a branch",
		Tags:        []string{"Repository browser"},
	}), service.updateRepositoryFile)

	huma.Register(api, protected(huma.Operation{
		OperationID: "delete-repository-file",
		Method:      http.MethodDelete,
		Path:        "/api/repositories/{repository}/files/{ref}/{path}",
		Summary:     "Delete a file and commit the change to a branch",
		Tags:        []string{"Repository browser"},
	}), service.deleteRepositoryFile)

	huma.Register(api, protected(huma.Operation{
		OperationID: "rename-repository-file",
		Method:      http.MethodPatch,
		Path:        "/api/repositories/{repository}/files/{ref}/{path}",
		Summary:     "Rename a file and commit the change to a branch",
		Tags:        []string{"Repository browser"},
	}), service.renameRepositoryFile)

	huma.Register(api, protected(huma.Operation{
		OperationID: "read-repository-file-blame",
		Method:      http.MethodGet,
		Path:        "/api/repositories/{repository}/blame/{ref}/{path}",
		Summary:     "Show line-by-line file attribution",
		Tags:        []string{"Repository browser"},
	}), service.readRepositoryBlame)

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
	repository, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
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
	defaultBranch, defaultRef := repositoryDefaultReferences(repository, branches)

	output := &repositoryBranchesOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.DefaultBranch = defaultBranch
	output.Body.DefaultRef = defaultRef
	_, writeErr := a.authorizeRepository(ctx, input.AuthInput, parsed, control.RoleDeveloper)
	output.Body.CanWrite = writeErr == nil
	_, manageErr := a.authorizeRepository(ctx, input.AuthInput, parsed, control.RoleMaintainer)
	output.Body.CanManage = manageErr == nil
	output.Body.Branches = branches
	return output, nil
}

func repositoryDefaultReferences(
	repository *git.Repository,
	branches []repositoryBranch,
) (string, string) {
	available := make(map[string]struct{}, len(branches))
	for _, branch := range branches {
		available[branch.Name] = struct{}{}
	}

	if head, err := repository.Reference(plumbing.HEAD, false); err == nil &&
		head.Type() == plumbing.SymbolicReference &&
		head.Target().IsBranch() {
		name := head.Target().Short()
		if _, ok := available[name]; ok {
			if _, commitErr := resolveBrowserCommit(repository, name); commitErr == nil {
				return name, name
			}
		}
	}
	if _, err := resolveBrowserCommit(repository, plumbing.HEAD.Short()); err == nil {
		return "", plumbing.HEAD.Short()
	}
	if _, ok := available["main"]; ok {
		if _, err := resolveBrowserCommit(repository, "main"); err == nil {
			return "", "main"
		}
	}
	for _, branch := range branches {
		if _, err := resolveBrowserCommit(repository, branch.Name); err == nil {
			return "", branch.Name
		}
	}
	return "", ""
}

func (a API) updateRepositoryDefaultBranch(
	ctx context.Context,
	input *updateRepositoryDefaultBranchInput,
) (*updateRepositoryDefaultBranchOutput, error) {
	branch, err := validatedBranchReference(input.Body.Branch)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid default branch", err)
	}
	parsed, err := parseRepositoryPath(input.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if parsed.Name == "control" {
		return nil, huma.Error400BadRequest("the control repository default branch cannot be changed")
	}
	releaseOperation, err := a.acquireRepositoryOperationLocks(parsed)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = releaseOperation()
	}()

	repository, parsed, err := a.openRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleMaintainer,
	)
	if err != nil {
		return nil, err
	}
	reference, err := repository.Reference(branch, true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return nil, huma.Error404NotFound("default branch not found")
	}
	if err != nil {
		return nil, huma.Error500InternalServerError("could not read default branch", err)
	}
	if _, err = repository.CommitObject(reference.Hash()); err != nil {
		return nil, huma.Error409Conflict("default branch does not point to a commit", err)
	}
	if err = repository.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, branch),
	); err != nil {
		return nil, huma.Error409Conflict("could not change default branch", err)
	}

	output := &updateRepositoryDefaultBranchOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.DefaultBranch = input.Body.Branch
	output.Body.DefaultRef = input.Body.Branch
	return output, nil
}

func (a API) createRepositoryBranch(ctx context.Context, input *createRepositoryBranchInput) (*createRepositoryBranchOutput, error) {
	branchName, err := validatedBranchReference(input.Branch)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid new branch name", err)
	}
	sourceName, err := validatedBranchReference(input.From)
	if err != nil {
		return nil, huma.Error400BadRequest("invalid source branch name", err)
	}
	parsed, err := parseRepositoryPath(input.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperation, err := a.acquireRepositoryOperationLocks(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"could not lock repository operations",
			err,
		)
	}
	defer func() {
		_ = releaseOperation()
	}()
	repository, parsed, err := a.openRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
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
	a.scheduleBuild(parsed, input.Branch, source.Hash())

	output := &createRepositoryBranchOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Name = input.Branch
	output.Body.From = input.From
	output.Body.Commit = source.Hash().String()
	return output, nil
}

func (a API) listRepositoryRoot(ctx context.Context, input *repositoryBrowserRefInput) (*repositoryTreeOutput, error) {
	return a.listRepositoryTree(ctx, input.AuthInput, input.Repository, input.Ref, "")
}

func (a API) listRepositoryDirectory(ctx context.Context, input *repositoryBrowserPathInput) (*repositoryTreeOutput, error) {
	return a.listRepositoryTree(ctx, input.AuthInput, input.Repository, input.Ref, input.Path)
}

func (a API) listRepositoryTree(
	ctx context.Context,
	credentials AuthInput,
	repositoryPath string,
	ref string,
	treePath string,
) (*repositoryTreeOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, credentials, repositoryPath)
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
				if pointer, ok := repositoryLFSPointer(blob); ok {
					item.LFS = true
					item.Size = pointer.Size
				}
			}
		}
		entries = append(entries, item)
	}

	output := &repositoryTreeOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Ref = ref
	output.Body.Commit = commit.Hash.String()
	output.Body.Path = cleanPath
	branchName, branchRef, _, branchErr := resolveBranch(repository, ref)
	_, writeErr := a.authorizeRepository(ctx, credentials, parsed, control.RoleDeveloper)
	output.Body.CanEdit = branchErr == nil &&
		branchName == ref &&
		branchRef.Hash() == commit.Hash &&
		writeErr == nil
	output.Body.Entries = entries
	return output, nil
}

func repositoryLFSPointer(blob *object.Blob) (lfs.Pointer, bool) {
	if blob.Size > lfs.MaxPointerSize {
		return lfs.Pointer{}, false
	}
	reader, err := blob.Reader()
	if err != nil {
		return lfs.Pointer{}, false
	}
	defer func() {
		_ = reader.Close()
	}()
	content, err := io.ReadAll(io.LimitReader(reader, lfs.MaxPointerSize+1))
	if err != nil {
		return lfs.Pointer{}, false
	}
	return lfs.ParsePointer(content)
}

func (a API) readRepositoryBlob(ctx context.Context, input *repositoryBrowserPathInput) (*repositoryBlobOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
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
	if file.Size > maxBrowsableBlobSize {
		return nil, huma.Error413RequestEntityTooLarge(
			fmt.Sprintf("file exceeds the %d-byte browsing limit", maxBrowsableBlobSize),
		)
	}
	reader, err := file.Reader()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not open Git blob", err)
	}
	defer func() {
		_ = reader.Close()
	}()
	content, err := io.ReadAll(io.LimitReader(reader, maxBrowsableBlobSize+1))
	if err != nil {
		return nil, huma.Error500InternalServerError("could not read Git blob", err)
	}

	pointer, isLFS := lfs.ParsePointer(content)
	contentSize := file.Size
	if isLFS {
		if pointer.Size > maxBrowsableBlobSize {
			return nil, huma.Error413RequestEntityTooLarge(
				fmt.Sprintf("LFS object exceeds the %d-byte browsing limit", maxBrowsableBlobSize),
			)
		}
		lfsObject, openErr := lfs.OpenObject(a.Storage, parsed, pointer.OID)
		if errors.Is(openErr, os.ErrNotExist) {
			return nil, huma.Error404NotFound("LFS object not found", openErr)
		}
		if openErr != nil {
			return nil, huma.Error500InternalServerError("could not open LFS object", openErr)
		}
		defer func() {
			_ = lfsObject.Close()
		}()
		info, statErr := lfsObject.Stat()
		if statErr != nil {
			return nil, huma.Error500InternalServerError("could not inspect LFS object", statErr)
		}
		if info.Size() != pointer.Size {
			return nil, huma.Error500InternalServerError(
				"LFS object size does not match its pointer",
				fmt.Errorf("expected %d bytes, found %d", pointer.Size, info.Size()),
			)
		}
		content, err = io.ReadAll(io.LimitReader(lfsObject, maxBrowsableBlobSize+1))
		if err != nil {
			return nil, huma.Error500InternalServerError("could not read LFS object", err)
		}
		contentSize = pointer.Size
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
	output.Body.Size = contentSize
	output.Body.Encoding = encoding
	output.Body.Content = encodedContent
	output.Body.LFS = isLFS
	if isLFS {
		output.Body.LFSOID = pointer.OID
	}
	branchName, branchRef, _, branchErr := resolveBranch(repository, input.Ref)
	_, writeErr := a.authorizeRepository(
		ctx,
		input.AuthInput,
		parsed,
		control.RoleDeveloper,
	)
	output.Body.CanManage = branchErr == nil &&
		branchName == input.Ref &&
		branchRef.Hash() == commit.Hash &&
		file.Mode.IsFile() &&
		writeErr == nil
	if encoding == "utf-8" {
		output.Body.HighlightedHTML, output.Body.Language = highlightRepositoryBlob(
			cleanPath,
			encodedContent,
		)
		output.Body.CanEdit = output.Body.CanManage &&
			!isLFS &&
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repository, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
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

	page := input.Page
	if page == 0 {
		page = 1
	}
	perPage := input.PerPage
	if perPage == 0 {
		perPage = 50
	}
	if page < 1 || page > 1_000_000 || perPage < 1 || perPage > 100 {
		return nil, huma.Error400BadRequest("invalid commit history page")
	}
	offset := (page - 1) * perPage
	if offset > maximumCommitHistoryWalk-perPage-1 {
		return nil, huma.Error400BadRequest("commit history page is too deep")
	}
	commits := make([]repositoryCommitInfo, 0, perPage+1)
	seen := 0
	for seen < offset+perPage+1 {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		current, nextErr := iterator.Next()
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, huma.Error500InternalServerError(
				"could not iterate over commits",
				nextErr,
			)
		}
		index := seen
		seen++
		if index < offset {
			continue
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
	}
	hasNext := len(commits) > perPage
	if hasNext {
		commits = commits[:perPage]
	}

	output := &repositoryCommitsOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Ref = input.Ref
	output.Body.Page = page
	output.Body.PerPage = perPage
	output.Body.HasPrevious = page > 1
	output.Body.HasNext = hasNext
	output.Body.Commits = commits
	if !hasNext {
		total := seen
		totalPages := (total + perPage - 1) / perPage
		output.Body.Total = &total
		output.Body.TotalPages = &totalPages
	}
	return output, nil
}

func (a API) openBrowsableRepository(
	ctx context.Context,
	credentials AuthInput,
	value string,
) (*git.Repository, repopath.Repository, error) {
	return a.openRepository(ctx, credentials, value, control.RoleRead)
}

func (a API) openRepository(
	ctx context.Context,
	credentials AuthInput,
	value string,
	role control.Role,
) (*git.Repository, repopath.Repository, error) {
	parsed, err := parseRepositoryPath(value)
	if err != nil {
		return nil, repopath.Repository{}, huma.Error400BadRequest(err.Error())
	}
	visibility := ""
	if role == control.RoleRead && parsed.Name != "control" {
		if document, loadErr := a.Resolver.Controls.Load(ctx, parsed.Group()); loadErr == nil {
			visibility = document.Visibility
		}
	}
	switch visibility {
	case "public":
	case "internal":
		if _, authErr := a.authorizeInternal(ctx, credentials); authErr != nil {
			if _, repositoryAuthErr := a.authorizeRepository(ctx, credentials, parsed, role); repositoryAuthErr != nil {
				return nil, repopath.Repository{}, authErr
			}
		}
	default:
		_, authErr := a.authorizeRepository(ctx, credentials, parsed, role)
		if authErr != nil {
			return nil, repopath.Repository{}, authErr
		}
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

func (a API) authorizeInternal(ctx context.Context, credentials AuthInput) (auth.Principal, error) {
	return a.authenticateIdentity(ctx, credentials)
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
