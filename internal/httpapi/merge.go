package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	fdiff "github.com/go-git/go-git/v5/plumbing/format/diff"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitstorage "github.com/go-git/go-git/v5/storage"
	"github.com/sergi/go-diff/diffmatchpatch"
)

const (
	maxAutomaticMergeBlobSize       = 10 * 1024 * 1024
	maxComparisonPatchSize          = 1024 * 1024
	maxComparisonPatchBytes         = 8 * 1024 * 1024
	maxComparisonBlobBytes    int64 = 2 * 1024 * 1024
	maxComparisonFiles              = 1000
	maxComparisonRenameFiles        = 50
)

type treeComparison struct {
	Files     []repositoryComparisonFile
	Truncated bool
}

type treeComparisonLimits struct {
	files          int
	filePatchBytes int
	patchBytes     int
	blobBytes      int64
}

func defaultTreeComparisonLimits() treeComparisonLimits {
	return treeComparisonLimits{
		files:          maxComparisonFiles,
		filePatchBytes: maxComparisonPatchSize,
		patchBytes:     maxComparisonPatchBytes,
		blobBytes:      maxComparisonBlobBytes,
	}
}

func (a API) compareRepositoryBranches(
	ctx context.Context,
	input *compareRepositoryBranchesInput,
) (*compareRepositoryBranchesOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
	if err != nil {
		return nil, err
	}
	baseName, baseRef, baseCommit, err := resolveBranch(repository, input.Base)
	if err != nil {
		return nil, huma.Error404NotFound("base branch not found", err)
	}
	headName, _, headCommit, err := resolveBranch(repository, input.Head)
	if err != nil {
		return nil, huma.Error404NotFound("head branch not found", err)
	}
	if baseName == headName {
		return nil, huma.Error400BadRequest("choose two different branches")
	}

	ahead, behind, err := commitDifference(ctx, repository, baseCommit, headCommit)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, huma.Error500InternalServerError("could not compare commit history", err)
	}
	mergeBase, mergeable, conflicts, err := assessBranchMerge(repository, baseCommit, headCommit)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not assess branch merge", err)
	}

	diffBase := baseCommit
	if mergeBase != nil {
		diffBase = mergeBase
	}
	fromTree, err := diffBase.Tree()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load comparison base", err)
	}
	toTree, err := headCommit.Tree()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load comparison head", err)
	}
	comparison, err := compareTrees(ctx, fromTree, toTree)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not create branch diff", err)
	}

	_, canMergeErr := a.authorizeRepository(
		ctx,
		input.AuthInput,
		parsed,
		control.RoleDeveloper,
	)
	output := &compareRepositoryBranchesOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Base = baseName
	output.Body.Head = headName
	output.Body.BaseCommit = baseRef.Hash().String()
	output.Body.HeadCommit = headCommit.Hash.String()
	if mergeBase != nil {
		output.Body.MergeBase = mergeBase.Hash.String()
	}
	output.Body.Ahead = ahead
	output.Body.Behind = behind
	output.Body.Mergeable = mergeable
	output.Body.CanMerge = canMergeErr == nil
	output.Body.Conflicts = conflicts
	output.Body.Files = comparison.Files
	output.Body.Truncated = comparison.Truncated
	return output, nil
}

func (a API) mergeRepositoryBranches(
	ctx context.Context,
	input *mergeRepositoryBranchesInput,
) (*mergeRepositoryBranchesOutput, error) {
	parsed, err := parseRepositoryPath(input.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	releaseOperationLock, err := a.acquireRepositoryOperationLocks(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not lock repository operations", err)
	}
	defer func() {
		_ = releaseOperationLock()
	}()
	releaseRepositoryLock, err := a.reviewStore().AcquireMergeLock(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not lock repository merge", err)
	}
	defer func() {
		_ = releaseRepositoryLock()
	}()
	principal, err := a.authorizeRepository(
		ctx,
		input.AuthInput,
		parsed,
		control.RoleDeveloper,
	)
	if err != nil {
		return nil, err
	}
	author := principal.Name
	repositoryPath, err := a.Storage.GitPath(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not resolve repository", err)
	}
	repository, err := git.PlainOpen(repositoryPath)
	if err != nil {
		return nil, huma.Error404NotFound("repository not found", err)
	}

	merged, err := a.mergeRepositoryBranchesAtSource(
		repository,
		parsed,
		input.Body.Target,
		input.Body.Source,
		author,
		input.Body.Message,
		"",
		nil,
	)
	if err != nil {
		return nil, err
	}

	output := &mergeRepositoryBranchesOutput{}
	output.Body.Repository = merged.Repository
	output.Body.Target = merged.Target
	output.Body.Source = merged.Source
	output.Body.Commit = merged.Commit
	output.Body.Strategy = merged.Strategy
	return output, nil
}

type repositoryMergeResult struct {
	Repository     string
	Target         string
	Source         string
	PreviousTarget string
	Commit         string
	Strategy       string
}

type mergeNotAppliedError struct {
	cause error
}

func (e *mergeNotAppliedError) Error() string {
	return e.cause.Error()
}

func (e *mergeNotAppliedError) Unwrap() error {
	return e.cause
}

func targetReferenceUpdateError(err error) error {
	cause := huma.Error409Conflict(
		"target branch changed while it was being merged",
		err,
	)
	if errors.Is(err, gitstorage.ErrReferenceHasChanged) {
		return &mergeNotAppliedError{cause: cause}
	}
	return cause
}

func (a API) mergeRepositoryBranchesAtSource(
	repository *git.Repository,
	parsed repopath.Repository,
	target string,
	source string,
	author string,
	message string,
	expectedSourceCommit string,
	beforeTargetUpdate func(repositoryMergeResult) error,
) (repositoryMergeResult, error) {
	targetName, targetRef, targetCommit, err := resolveBranch(repository, target)
	if err != nil {
		return repositoryMergeResult{}, huma.Error404NotFound("target branch not found", err)
	}
	sourceName, sourceRef, sourceCommit, err := resolveBranch(repository, source)
	if err != nil {
		return repositoryMergeResult{}, huma.Error404NotFound("source branch not found", err)
	}
	if targetName == sourceName {
		return repositoryMergeResult{}, huma.Error400BadRequest(
			"source and target branches must be different",
		)
	}
	if expectedSourceCommit != "" && expectedSourceCommit != sourceCommit.Hash.String() {
		return repositoryMergeResult{}, huma.Error409Conflict(
			"source branch changed since it was approved",
		)
	}

	result := repositoryMergeResult{
		Repository:     parsed.Full(),
		Target:         targetName,
		Source:         sourceName,
		PreviousTarget: targetRef.Hash().String(),
	}

	sourceIsAncestor, err := sourceCommit.IsAncestor(targetCommit)
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not inspect branch history",
			err,
		)
	}
	if sourceIsAncestor {
		result.Commit = targetCommit.Hash.String()
		result.Strategy = "already-up-to-date"
		if beforeTargetUpdate != nil {
			if err = beforeTargetUpdate(result); err != nil {
				return repositoryMergeResult{}, err
			}
		}
		return result, nil
	}
	targetIsAncestor, err := targetCommit.IsAncestor(sourceCommit)
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not inspect branch history",
			err,
		)
	}
	if targetIsAncestor {
		result.Commit = sourceRef.Hash().String()
		result.Strategy = "fast-forward"
		if beforeTargetUpdate != nil {
			if err = beforeTargetUpdate(result); err != nil {
				return repositoryMergeResult{}, err
			}
		}
		updated := plumbing.NewHashReference(targetRef.Name(), sourceRef.Hash())
		if err = repository.Storer.CheckAndSetReference(updated, targetRef); err != nil {
			return repositoryMergeResult{}, targetReferenceUpdateError(err)
		}
		a.scheduleBuild(parsed, targetName, sourceRef.Hash())
		return result, nil
	}

	bases, err := targetCommit.MergeBase(sourceCommit)
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not find a merge base",
			err,
		)
	}
	if len(bases) != 1 {
		return repositoryMergeResult{}, huma.Error409Conflict(
			"branches do not have a single merge base",
		)
	}
	baseTree, err := bases[0].Tree()
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not load merge base",
			err,
		)
	}
	targetTree, err := targetCommit.Tree()
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not load target branch",
			err,
		)
	}
	sourceTree, err := sourceCommit.Tree()
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not load source branch",
			err,
		)
	}

	_, conflicts, err := mergeTrees(repository, baseTree, targetTree, sourceTree, "", false)
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not assess branch merge",
			err,
		)
	}
	if len(conflicts) > 0 {
		return repositoryMergeResult{}, huma.Error409Conflict(
			"branches have merge conflicts: " + strings.Join(conflicts, ", "),
		)
	}
	treeHash, conflicts, err := mergeTrees(repository, baseTree, targetTree, sourceTree, "", true)
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not merge branch trees",
			err,
		)
	}
	if len(conflicts) > 0 {
		return repositoryMergeResult{}, huma.Error409Conflict(
			"branches have merge conflicts: " + strings.Join(conflicts, ", "),
		)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = fmt.Sprintf("Merge branch '%s' into %s", sourceName, targetName)
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
		ParentHashes: []plumbing.Hash{targetCommit.Hash, sourceCommit.Hash},
	}
	encodedCommit := &plumbing.MemoryObject{}
	if err = commit.Encode(encodedCommit); err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not encode merge commit",
			err,
		)
	}
	commitHash, err := repository.Storer.SetEncodedObject(encodedCommit)
	if err != nil {
		return repositoryMergeResult{}, huma.Error500InternalServerError(
			"could not store merge commit",
			err,
		)
	}
	result.Commit = commitHash.String()
	result.Strategy = "merge-commit"
	if beforeTargetUpdate != nil {
		if err = beforeTargetUpdate(result); err != nil {
			return repositoryMergeResult{}, err
		}
	}
	updated := plumbing.NewHashReference(targetRef.Name(), commitHash)
	if err = repository.Storer.CheckAndSetReference(updated, targetRef); err != nil {
		return repositoryMergeResult{}, targetReferenceUpdateError(err)
	}
	a.scheduleBuild(parsed, targetName, commitHash)
	return result, nil
}

func resolveBranch(
	repository *git.Repository,
	value string,
) (string, *plumbing.Reference, *object.Commit, error) {
	name, err := validatedBranchReference(value)
	if err != nil {
		return "", nil, nil, err
	}
	reference, err := repository.Reference(name, false)
	if err != nil {
		return "", nil, nil, err
	}
	commit, err := repository.CommitObject(reference.Hash())
	if err != nil {
		return "", nil, nil, err
	}
	return name.Short(), reference, commit, nil
}

func assessBranchMerge(
	repository *git.Repository,
	target *object.Commit,
	source *object.Commit,
) (*object.Commit, bool, []string, error) {
	if source.Hash == target.Hash {
		return target, true, []string{}, nil
	}
	sourceIsAncestor, err := source.IsAncestor(target)
	if err != nil {
		return nil, false, nil, err
	}
	if sourceIsAncestor {
		return source, true, []string{}, nil
	}
	targetIsAncestor, err := target.IsAncestor(source)
	if err != nil {
		return nil, false, nil, err
	}
	if targetIsAncestor {
		return target, true, []string{}, nil
	}
	bases, err := target.MergeBase(source)
	if err != nil {
		return nil, false, nil, err
	}
	if len(bases) != 1 {
		return nil, false, []string{"No single merge base"}, nil
	}
	baseTree, err := bases[0].Tree()
	if err != nil {
		return nil, false, nil, err
	}
	targetTree, err := target.Tree()
	if err != nil {
		return nil, false, nil, err
	}
	sourceTree, err := source.Tree()
	if err != nil {
		return nil, false, nil, err
	}
	_, conflicts, err := mergeTrees(repository, baseTree, targetTree, sourceTree, "", false)
	if err != nil {
		return nil, false, nil, err
	}
	return bases[0], len(conflicts) == 0, conflicts, nil
}

func commitDifference(
	ctx context.Context,
	repository *git.Repository,
	base *object.Commit,
	head *object.Commit,
) (int, int, error) {
	baseCommits, err := reachableCommits(ctx, repository, base.Hash)
	if err != nil {
		return 0, 0, err
	}
	headCommits, err := reachableCommits(ctx, repository, head.Hash)
	if err != nil {
		return 0, 0, err
	}
	return commitSetDifference(ctx, baseCommits, headCommits)
}

func commitSetDifference(
	ctx context.Context,
	baseCommits map[plumbing.Hash]struct{},
	headCommits map[plumbing.Hash]struct{},
) (int, int, error) {
	ahead := 0
	for hash := range headCommits {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		if _, exists := baseCommits[hash]; !exists {
			ahead++
		}
	}
	behind := 0
	for hash := range baseCommits {
		if err := ctx.Err(); err != nil {
			return 0, 0, err
		}
		if _, exists := headCommits[hash]; !exists {
			behind++
		}
	}
	return ahead, behind, nil
}

func reachableCommits(
	ctx context.Context,
	repository *git.Repository,
	start plumbing.Hash,
) (map[plumbing.Hash]struct{}, error) {
	seen := map[plumbing.Hash]struct{}{start: {}}
	pending := []plumbing.Hash{start}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		commit, err := repository.CommitObject(hash)
		if err != nil {
			return nil, err
		}
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		for _, parent := range commit.ParentHashes {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if _, exists := seen[parent]; exists {
				continue
			}
			if len(seen) >= maximumCommitHistoryWalk {
				return nil, errCommitHistoryWalkLimit
			}
			seen[parent] = struct{}{}
			pending = append(pending, parent)
		}
	}
	return seen, nil
}

var errCommitHistoryWalkLimit = errors.New("commit history exceeds maximum walk")

func compareTrees(
	ctx context.Context,
	from *object.Tree,
	to *object.Tree,
) (treeComparison, error) {
	return compareTreesWithLimits(ctx, from, to, defaultTreeComparisonLimits())
}

func compareTreesWithLimits(
	ctx context.Context,
	from *object.Tree,
	to *object.Tree,
	limits treeComparisonLimits,
) (treeComparison, error) {
	changes, err := object.DiffTreeWithOptions(ctx, from, to, nil)
	if err != nil {
		return treeComparison{}, err
	}
	contentRenames, err := comparisonChangesFitBudget(
		changes,
		maxComparisonRenameFiles,
		limits.blobBytes,
	)
	if err != nil {
		return treeComparison{}, err
	}
	diffOptions := &object.DiffTreeOptions{
		DetectRenames:    true,
		RenameScore:      object.DefaultDiffTreeOptions.RenameScore,
		RenameLimit:      uint(limits.files),
		OnlyExactRenames: !contentRenames,
	}
	if contentRenames {
		diffOptions.RenameLimit = maxComparisonRenameFiles
	}
	changes, err = object.DetectRenames(changes, diffOptions)
	if err != nil {
		return treeComparison{}, err
	}
	result := treeComparison{
		Files: make(
			[]repositoryComparisonFile,
			0,
			min(len(changes), limits.files),
		),
	}
	patchBytes := 0
	for _, change := range changes {
		if len(result.Files) >= limits.files || patchBytes >= limits.patchBytes {
			result.Truncated = true
			break
		}
		fromFile, toFile, err := change.Files()
		if err != nil {
			return treeComparison{}, err
		}
		file := repositoryComparisonFile{Status: "modified"}
		switch {
		case fromFile == nil:
			file.Path = change.To.Name
			file.Status = "added"
		case toFile == nil:
			file.Path = change.From.Name
			file.Status = "deleted"
		default:
			file.Path = change.To.Name
			if change.From.Name != change.To.Name {
				file.OldPath = change.From.Name
				file.Status = "renamed"
			}
		}

		if comparisonBlobTooLarge(fromFile, toFile, limits.blobBytes) {
			file.Binary, err = comparisonFilesBinary(fromFile, toFile)
			if err != nil {
				return treeComparison{}, err
			}
			if !file.Binary {
				file.Truncated = true
				result.Truncated = true
			}
			result.Files = append(result.Files, file)
			continue
		}

		patch, err := change.PatchContext(ctx)
		if err != nil {
			return treeComparison{}, err
		}
		for _, filePatch := range patch.FilePatches() {
			file.Binary = file.Binary || filePatch.IsBinary()
			for _, chunk := range filePatch.Chunks() {
				switch chunk.Type() {
				case fdiff.Add:
					file.Additions += diffLineCount(chunk.Content())
				case fdiff.Delete:
					file.Deletions += diffLineCount(chunk.Content())
				case fdiff.Equal:
					continue
				}
			}
		}
		if !file.Binary {
			encoded := patch.String()
			available := min(
				limits.filePatchBytes,
				limits.patchBytes-patchBytes,
			)
			if len(encoded) > available {
				file.Patch = strings.Clone(encoded[:available])
				file.Truncated = true
				result.Truncated = true
			} else {
				file.Patch = encoded
			}
			patchBytes += len(file.Patch)
		}
		result.Files = append(result.Files, file)
	}
	if len(result.Files) < len(changes) {
		result.Truncated = true
	}
	return result, nil
}

func comparisonChangesFitBudget(
	changes object.Changes,
	maximumFiles int,
	maximumBytes int64,
) (bool, error) {
	if len(changes) > maximumFiles {
		return false, nil
	}
	total := int64(0)
	for _, change := range changes {
		from, to, err := change.Files()
		if err != nil {
			return false, err
		}
		for _, file := range []*object.File{from, to} {
			if file == nil {
				continue
			}
			if file.Size > maximumBytes-total {
				return false, nil
			}
			total += file.Size
		}
	}
	return true, nil
}

func comparisonBlobTooLarge(
	from *object.File,
	to *object.File,
	maximum int64,
) bool {
	total := int64(0)
	for _, file := range []*object.File{from, to} {
		if file == nil {
			continue
		}
		if file.Size > maximum-total {
			return true
		}
		total += file.Size
	}
	return false
}

func comparisonFilesBinary(from *object.File, to *object.File) (bool, error) {
	for _, file := range []*object.File{from, to} {
		if file == nil {
			continue
		}
		binary, err := file.IsBinary()
		if err != nil {
			return false, err
		}
		if binary {
			return true, nil
		}
	}
	return false, nil
}

func diffLineCount(content string) int {
	if content == "" {
		return 0
	}
	count := strings.Count(content, "\n")
	if !strings.HasSuffix(content, "\n") {
		count++
	}
	return count
}

func mergeTrees(
	repository *git.Repository,
	base *object.Tree,
	target *object.Tree,
	source *object.Tree,
	prefix string,
	persist bool,
) (plumbing.Hash, []string, error) {
	baseEntries := treeEntries(base)
	targetEntries := treeEntries(target)
	sourceEntries := treeEntries(source)
	names := map[string]struct{}{}
	for name := range baseEntries {
		names[name] = struct{}{}
	}
	for name := range targetEntries {
		names[name] = struct{}{}
	}
	for name := range sourceEntries {
		names[name] = struct{}{}
	}
	orderedNames := make([]string, 0, len(names))
	for name := range names {
		orderedNames = append(orderedNames, name)
	}
	sort.Strings(orderedNames)

	entries := make([]object.TreeEntry, 0, len(orderedNames))
	conflicts := make([]string, 0)
	for _, name := range orderedNames {
		baseEntry := baseEntries[name]
		targetEntry := targetEntries[name]
		sourceEntry := sourceEntries[name]
		entryPath := name
		if prefix != "" {
			entryPath = prefix + "/" + name
		}

		switch {
		case entriesEqual(targetEntry, sourceEntry):
			if targetEntry != nil {
				entries = append(entries, *targetEntry)
			}
			continue
		case entriesEqual(targetEntry, baseEntry):
			if sourceEntry != nil {
				entries = append(entries, *sourceEntry)
			}
			continue
		case entriesEqual(sourceEntry, baseEntry):
			if targetEntry != nil {
				entries = append(entries, *targetEntry)
			}
			continue
		}

		if targetEntry != nil &&
			sourceEntry != nil &&
			targetEntry.Mode == filemode.Dir &&
			sourceEntry.Mode == filemode.Dir &&
			(baseEntry == nil || baseEntry.Mode == filemode.Dir) {
			var baseTree *object.Tree
			var err error
			if baseEntry != nil {
				baseTree, err = repository.TreeObject(baseEntry.Hash)
				if err != nil {
					return plumbing.ZeroHash, nil, err
				}
			}
			targetTree, err := repository.TreeObject(targetEntry.Hash)
			if err != nil {
				return plumbing.ZeroHash, nil, err
			}
			sourceTree, err := repository.TreeObject(sourceEntry.Hash)
			if err != nil {
				return plumbing.ZeroHash, nil, err
			}
			hash, nestedConflicts, err := mergeTrees(
				repository,
				baseTree,
				targetTree,
				sourceTree,
				entryPath,
				persist,
			)
			if err != nil {
				return plumbing.ZeroHash, nil, err
			}
			conflicts = append(conflicts, nestedConflicts...)
			entries = append(entries, object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: hash})
			continue
		}

		mergedEntry, clean, err := mergeFileEntries(
			repository,
			baseEntry,
			targetEntry,
			sourceEntry,
			persist,
		)
		if err != nil {
			return plumbing.ZeroHash, nil, err
		}
		if !clean {
			conflicts = append(conflicts, entryPath)
			continue
		}
		mergedEntry.Name = name
		entries = append(entries, mergedEntry)
	}

	sort.Sort(object.TreeEntrySorter(entries))
	tree := &object.Tree{Entries: entries}
	encodedTree := &plumbing.MemoryObject{}
	if err := tree.Encode(encodedTree); err != nil {
		return plumbing.ZeroHash, nil, err
	}
	hash := encodedTree.Hash()
	if persist && len(conflicts) == 0 {
		var err error
		hash, err = repository.Storer.SetEncodedObject(encodedTree)
		if err != nil {
			return plumbing.ZeroHash, nil, err
		}
	}
	return hash, conflicts, nil
}

func treeEntries(tree *object.Tree) map[string]*object.TreeEntry {
	entries := map[string]*object.TreeEntry{}
	if tree == nil {
		return entries
	}
	for index := range tree.Entries {
		entry := tree.Entries[index]
		entries[entry.Name] = &entry
	}
	return entries
}

func entriesEqual(left, right *object.TreeEntry) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Mode == right.Mode && left.Hash == right.Hash
}

func mergeFileEntries(
	repository *git.Repository,
	base *object.TreeEntry,
	target *object.TreeEntry,
	source *object.TreeEntry,
	persist bool,
) (object.TreeEntry, bool, error) {
	if target == nil || source == nil ||
		target.Mode == filemode.Dir || source.Mode == filemode.Dir ||
		(base != nil && base.Mode == filemode.Dir) {
		return object.TreeEntry{}, false, nil
	}
	baseMode := filemode.Empty
	baseHash := plumbing.ZeroHash
	if base != nil {
		baseMode = base.Mode
		baseHash = base.Hash
	}
	mode, clean := mergeFileMode(baseMode, target.Mode, source.Mode)
	if !clean {
		return object.TreeEntry{}, false, nil
	}
	switch {
	case target.Hash == source.Hash:
		return object.TreeEntry{Mode: mode, Hash: target.Hash}, true, nil
	case base != nil && target.Hash == baseHash:
		return object.TreeEntry{Mode: mode, Hash: source.Hash}, true, nil
	case base != nil && source.Hash == baseHash:
		return object.TreeEntry{Mode: mode, Hash: target.Hash}, true, nil
	}
	if !mergeableTextMode(target.Mode) ||
		!mergeableTextMode(source.Mode) ||
		(base != nil && !mergeableTextMode(base.Mode)) {
		return object.TreeEntry{}, false, nil
	}

	baseContent := []byte{}
	var err error
	if base != nil {
		baseContent, err = readMergeBlob(repository, base.Hash)
		if err != nil {
			if errors.Is(err, errUnmergeableBlob) {
				return object.TreeEntry{}, false, nil
			}
			return object.TreeEntry{}, false, err
		}
	}
	targetContent, err := readMergeBlob(repository, target.Hash)
	if err != nil {
		if errors.Is(err, errUnmergeableBlob) {
			return object.TreeEntry{}, false, nil
		}
		return object.TreeEntry{}, false, err
	}
	sourceContent, err := readMergeBlob(repository, source.Hash)
	if err != nil {
		if errors.Is(err, errUnmergeableBlob) {
			return object.TreeEntry{}, false, nil
		}
		return object.TreeEntry{}, false, err
	}
	merged, clean := mergeTextLines(string(baseContent), string(targetContent), string(sourceContent))
	if !clean {
		return object.TreeEntry{}, false, nil
	}
	blob := &plumbing.MemoryObject{}
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(merged)))
	writer, err := blob.Writer()
	if err != nil {
		return object.TreeEntry{}, false, err
	}
	if _, err = io.WriteString(writer, merged); err != nil {
		_ = writer.Close()
		return object.TreeEntry{}, false, err
	}
	if err = writer.Close(); err != nil {
		return object.TreeEntry{}, false, err
	}
	hash := blob.Hash()
	if persist {
		hash, err = repository.Storer.SetEncodedObject(blob)
		if err != nil {
			return object.TreeEntry{}, false, err
		}
	}
	return object.TreeEntry{Mode: mode, Hash: hash}, true, nil
}

func mergeFileMode(
	base filemode.FileMode,
	target filemode.FileMode,
	source filemode.FileMode,
) (filemode.FileMode, bool) {
	switch {
	case target == source:
		return target, true
	case target == base:
		return source, true
	case source == base:
		return target, true
	default:
		return filemode.Empty, false
	}
}

func mergeableTextMode(mode filemode.FileMode) bool {
	return mode == filemode.Regular ||
		mode == filemode.Deprecated ||
		mode == filemode.Executable
}

var errUnmergeableBlob = errors.New("blob cannot be merged automatically")

func readMergeBlob(repository *git.Repository, hash plumbing.Hash) ([]byte, error) {
	blob, err := repository.BlobObject(hash)
	if err != nil {
		return nil, err
	}
	if blob.Size > maxAutomaticMergeBlobSize {
		return nil, errUnmergeableBlob
	}
	reader, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = reader.Close()
	}()
	content, err := io.ReadAll(io.LimitReader(reader, maxAutomaticMergeBlobSize+1))
	if err != nil {
		return nil, err
	}
	if len(content) > maxAutomaticMergeBlobSize ||
		!utf8.Valid(content) ||
		containsBinaryData(content) {
		return nil, errUnmergeableBlob
	}
	return content, nil
}

type lineEdit struct {
	start       int
	end         int
	replacement []string
}

func mergeTextLines(base, target, source string) (string, bool) {
	if target == source {
		return target, true
	}
	if target == base {
		return source, true
	}
	if source == base {
		return target, true
	}
	targetEdits := textLineEdits(base, target)
	sourceEdits := textLineEdits(base, source)
	combined := append([]lineEdit(nil), targetEdits...)
	for _, sourceEdit := range sourceEdits {
		duplicate := false
		for _, targetEdit := range targetEdits {
			if editsEqual(targetEdit, sourceEdit) {
				duplicate = true
				break
			}
			if editsOverlap(targetEdit, sourceEdit) {
				return "", false
			}
		}
		if !duplicate {
			combined = append(combined, sourceEdit)
		}
	}
	sort.SliceStable(combined, func(left, right int) bool {
		if combined[left].start != combined[right].start {
			return combined[left].start < combined[right].start
		}
		return combined[left].end < combined[right].end
	})

	lines := splitTextLines(base)
	var merged strings.Builder
	position := 0
	for _, edit := range combined {
		if edit.start < position || edit.start > len(lines) || edit.end > len(lines) {
			return "", false
		}
		for _, line := range lines[position:edit.start] {
			merged.WriteString(line)
		}
		for _, line := range edit.replacement {
			merged.WriteString(line)
		}
		position = edit.end
	}
	for _, line := range lines[position:] {
		merged.WriteString(line)
	}
	return merged.String(), true
}

func textLineEdits(base, version string) []lineEdit {
	dmp := diffmatchpatch.New()
	baseRunes, versionRunes, lineArray := dmp.DiffLinesToRunes(base, version)
	diffs := dmp.DiffMainRunes(baseRunes, versionRunes, false)
	edits := make([]lineEdit, 0)
	position := 0
	var pending *lineEdit
	flush := func() {
		if pending != nil {
			edits = append(edits, *pending)
			pending = nil
		}
	}
	for _, diff := range diffs {
		runes := []rune(diff.Text)
		switch diff.Type {
		case diffmatchpatch.DiffEqual:
			flush()
			position += len(runes)
		case diffmatchpatch.DiffDelete:
			if pending == nil {
				pending = &lineEdit{start: position, end: position}
			}
			pending.end += len(runes)
			position += len(runes)
		case diffmatchpatch.DiffInsert:
			if pending == nil {
				pending = &lineEdit{start: position, end: position}
			}
			// DiffLinesToRunes does not encode line indexes as their raw rune
			// values once an index reaches the UTF-16 surrogate range. Use the
			// paired decoder instead of indexing lineArray with the encoded runes.
			decoded := dmp.DiffCharsToLines([]diffmatchpatch.Diff{diff}, lineArray)
			pending.replacement = append(
				pending.replacement,
				splitTextLines(decoded[0].Text)...,
			)
		}
	}
	flush()
	return edits
}

func splitTextLines(value string) []string {
	if value == "" {
		return nil
	}
	lines := make([]string, 0, strings.Count(value, "\n")+1)
	for len(value) > 0 {
		index := strings.IndexByte(value, '\n')
		if index < 0 {
			lines = append(lines, value)
			break
		}
		lines = append(lines, value[:index+1])
		value = value[index+1:]
	}
	return lines
}

func editsEqual(left, right lineEdit) bool {
	if left.start != right.start ||
		left.end != right.end ||
		len(left.replacement) != len(right.replacement) {
		return false
	}
	for index := range left.replacement {
		if left.replacement[index] != right.replacement[index] {
			return false
		}
	}
	return true
}

func editsOverlap(left, right lineEdit) bool {
	leftInsert := left.start == left.end
	rightInsert := right.start == right.end
	switch {
	case leftInsert && rightInsert:
		return left.start == right.start
	case leftInsert:
		return left.start > right.start && left.start < right.end
	case rightInsert:
		return right.start > left.start && right.start < left.end
	default:
		return left.start < right.end && right.start < left.end
	}
}
