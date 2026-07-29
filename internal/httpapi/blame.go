package httpapi

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/danielgtaylor/huma/v2"
	git "github.com/go-git/go-git/v5"
)

func (a API) readRepositoryBlame(
	ctx context.Context,
	input *repositoryBrowserPathInput,
) (*repositoryBlameOutput, error) {
	repository, parsed, err := a.openBrowsableRepository(
		ctx,
		input.AuthInput,
		input.Repository,
	)
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
	if file.Size > maxEditableBlobSize {
		return nil, huma.Error413RequestEntityTooLarge(
			fmt.Sprintf("file exceeds the %d-byte blame limit", maxEditableBlobSize),
		)
	}
	if _, isLFS := repositoryLFSPointer(&file.Blob); isLFS {
		return nil, huma.Error400BadRequest("Git LFS files cannot be blamed")
	}
	contents, err := file.Contents()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not read file contents", err)
	}
	if !utf8.ValidString(contents) || containsBinaryData([]byte(contents)) {
		return nil, huma.Error400BadRequest("binary files cannot be blamed")
	}

	output := &repositoryBlameOutput{}
	output.Body.Repository = parsed.Full()
	output.Body.Ref = input.Ref
	output.Body.Commit = commit.Hash.String()
	output.Body.Path = cleanPath
	output.Body.Lines = []repositoryBlameLine{}
	fileLines, err := file.Lines()
	if err != nil {
		return nil, huma.Error500InternalServerError("could not split file into lines", err)
	}
	if len(fileLines) == 0 {
		return output, nil
	}
	result, err := git.Blame(commit, cleanPath)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not calculate file blame", err)
	}
	output.Body.Lines = make([]repositoryBlameLine, 0, len(result.Lines))
	for index, line := range result.Lines {
		output.Body.Lines = append(output.Body.Lines, repositoryBlameLine{
			Number:   index + 1,
			Text:     line.Text,
			Commit:   line.Hash.String(),
			Author:   line.AuthorName,
			Email:    line.Author,
			Authored: line.Date,
		})
	}
	return output, nil
}
