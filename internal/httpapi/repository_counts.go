package httpapi

import (
	"context"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/issue"
	"github.com/define42/GitOne/internal/review"
)

type repositoryOpenCountsInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
}

type repositoryOpenCountsOutput struct {
	Body struct {
		Repository    string `json:"repository"`
		MergeRequests int    `json:"mergeRequests" doc:"Number of open merge requests"`
		Issues        int    `json:"issues" doc:"Number of open issues"`
	}
}

func (a API) getRepositoryOpenCounts(
	ctx context.Context,
	input *repositoryOpenCountsInput,
) (*repositoryOpenCountsOutput, error) {
	_, parsed, err := a.openBrowsableRepository(ctx, input.AuthInput, input.Repository)
	if err != nil {
		return nil, err
	}

	mergeRequests, err := a.reviewStore().List(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not count merge requests", err)
	}
	issues, err := a.issueStore().List(parsed)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not count issues", err)
	}

	output := &repositoryOpenCountsOutput{}
	output.Body.Repository = parsed.Full()
	for _, mergeRequest := range mergeRequests {
		if mergeRequest.State == review.StateOpen {
			output.Body.MergeRequests++
		}
	}
	for _, record := range issues {
		if record.State == issue.StateOpen {
			output.Body.Issues++
		}
	}
	return output, nil
}
