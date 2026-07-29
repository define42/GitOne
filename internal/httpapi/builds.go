package httpapi

import (
	"context"
	"errors"
	"log"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/runner"
	"github.com/go-git/go-git/v5/plumbing"
)

type repositoryBuildsInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
}

type repositoryBuildInput struct {
	AuthInput
	Repository string `path:"repository" doc:"URL-encoded full group and repository path"`
	ID         string `path:"id" doc:"Build ID"`
}

type repositoryBuildsOutput struct {
	Body struct {
		Repository string       `json:"repository"`
		Builds     []runner.Job `json:"builds"`
	}
}

type repositoryBuildOutput struct {
	Body struct {
		Build runner.Job `json:"build"`
		Log   string     `json:"log"`
	}
}

func registerBuildAPI(api huma.API, service API) {
	huma.Register(api, protected(huma.Operation{
		OperationID: "list-repository-builds",
		Method:      "GET",
		Path:        "/api/repositories/{repository}/builds",
		Summary:     "List repository builds",
		Tags:        []string{"Builds"},
	}), service.listRepositoryBuilds)

	huma.Register(api, protected(huma.Operation{
		OperationID: "get-repository-build",
		Method:      "GET",
		Path:        "/api/repositories/{repository}/builds/{id}",
		Summary:     "Get a repository build and its log",
		Tags:        []string{"Builds"},
	}), service.getRepositoryBuild)
}

func (a API) listRepositoryBuilds(
	ctx context.Context,
	input *repositoryBuildsInput,
) (*repositoryBuildsOutput, error) {
	repository, err := parseRepositoryPath(input.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if _, err = a.authorizeRepository(ctx, input.AuthInput, repository, control.RoleRead); err != nil {
		return nil, err
	}
	if a.Builds == nil {
		return nil, huma.Error503ServiceUnavailable("build runner is not enabled")
	}
	builds, err := a.Builds.List(repository)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not list builds", err)
	}
	output := &repositoryBuildsOutput{}
	output.Body.Repository = repository.Full()
	output.Body.Builds = builds
	return output, nil
}

func (a API) getRepositoryBuild(
	ctx context.Context,
	input *repositoryBuildInput,
) (*repositoryBuildOutput, error) {
	repository, err := parseRepositoryPath(input.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	if _, err = a.authorizeRepository(ctx, input.AuthInput, repository, control.RoleRead); err != nil {
		return nil, err
	}
	if a.Builds == nil {
		return nil, huma.Error503ServiceUnavailable("build runner is not enabled")
	}
	build, err := a.Builds.Get(repository, input.ID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, huma.Error404NotFound("build not found")
	}
	if err != nil {
		return nil, huma.Error400BadRequest("could not read build", err)
	}
	log, err := a.Builds.Log(repository, input.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not read build log", err)
	}
	output := &repositoryBuildOutput{}
	output.Body.Build = build
	output.Body.Log = log
	return output, nil
}

func (a API) scheduleBuild(
	repository repopath.Repository,
	branch string,
	commit plumbing.Hash,
) {
	if a.Scheduler == nil {
		return
	}
	schedule := a.Scheduler.Schedule
	if locked, ok := a.Scheduler.(runner.LockedScheduler); ok {
		schedule = locked.ScheduleLocked
	}
	if _, err := schedule(repository, branch, commit); err != nil {
		log.Printf(
			"could not schedule build for %s@%s: %v",
			repository.Full(),
			commit,
			err,
		)
	}
}
