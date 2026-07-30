package httpapi

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func (a API) readRepositoryCommitDiff(
	ctx context.Context,
	input *repositoryCommitDiffInput,
) (*repositoryCommitDiffOutput, error) {
	repository, parsed, err := a.openRepository(
		ctx,
		input.AuthInput,
		input.Repository,
		control.RoleRead,
	)
	if err != nil {
		return nil, err
	}
	if !plumbing.IsHash(input.Commit) {
		return nil, huma.Error400BadRequest("commit must be a complete commit hash")
	}
	commit, err := repository.CommitObject(plumbing.NewHash(input.Commit))
	if err != nil {
		return nil, huma.Error404NotFound("commit not found", err)
	}
	toTree, err := commit.Tree()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not load commit tree", err)
	}
	fromTree := &object.Tree{}
	parentHash := ""
	if commit.NumParents() > 0 {
		parent, parentErr := commit.Parent(0)
		if parentErr != nil {
			return nil, huma.Error500InternalServerError("could not load commit parent", parentErr)
		}
		fromTree, err = parent.Tree()
		if err != nil {
			return nil, huma.Error500InternalServerError("could not load parent tree", err)
		}
		parentHash = parent.Hash.String()
	}
	files, filesTruncated, err := compareTrees(ctx, fromTree, toTree)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not create commit diff", err)
	}

	output := &repositoryCommitDiffOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Commit = commit.Hash.String()
	output.Body.Parent = parentHash
	output.Body.Files = files
	output.Body.FilesTruncated = filesTruncated
	return output, nil
}
