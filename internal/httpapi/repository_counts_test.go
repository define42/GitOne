package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/issue"
	"github.com/define42/GitOne/internal/review"
)

func TestRepositoryOpenCounts(t *testing.T) {
	fixture := newIssueAPIFixture(t)
	ctx := context.Background()

	closedIssue := fixture.create(t, fixture.bob, "Closed issue")
	fixture.create(t, fixture.bob, "Open issue")
	closedIssueState := string(issue.StateClosed)
	if _, err := fixture.service.updateIssue(ctx, &updateIssueInput{
		IssueInput: IssueInput{
			AuthInput:  fixture.bob,
			Repository: fixture.path.Full(),
			ID:         closedIssue.ID,
		},
		Body: updateIssueBody{State: &closedIssueState},
	}); err != nil {
		t.Fatal(err)
	}

	fixture.service.Reviews = review.NewStore(fixture.service.Storage.Root)
	openMergeRequest := repositoryCountMergeRequest("open-branch")
	if err := fixture.service.Reviews.Create(fixture.path, &openMergeRequest); err != nil {
		t.Fatal(err)
	}
	closedMergeRequest := repositoryCountMergeRequest("closed-branch")
	if err := fixture.service.Reviews.Create(fixture.path, &closedMergeRequest); err != nil {
		t.Fatal(err)
	}
	closedAt := time.Now().UTC()
	if _, err := fixture.service.Reviews.Update(
		fixture.path,
		closedMergeRequest.ID,
		func(request *review.MergeRequest) error {
			request.State = review.StateClosed
			request.ClosedBy = "alice"
			request.ClosedAt = &closedAt
			request.UpdatedAt = closedAt
			return nil
		},
	); err != nil {
		t.Fatal(err)
	}

	output, err := fixture.service.getRepositoryOpenCounts(
		ctx,
		&repositoryOpenCountsInput{
			AuthInput:  fixture.carol,
			Repository: fixture.path.Full(),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if output.Body.Repository != fixture.path.Full() ||
		output.Body.MergeRequests != 1 || output.Body.Issues != 1 {
		t.Fatalf("unexpected repository open counts: %#v", output.Body)
	}
}

func repositoryCountMergeRequest(source string) review.MergeRequest {
	return review.MergeRequest{
		Title:       "Count this merge request",
		Target:      "main",
		Source:      source,
		Author:      "alice",
		BaseCommit:  "1111111111111111111111111111111111111111",
		HeadCommit:  "2222222222222222222222222222222222222222",
		Description: "Used to verify repository navigation counts.",
	}
}
