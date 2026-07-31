package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
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
		CanManage  bool         `json:"canManage"`
	}
}

type repositoryBuildOutput struct {
	Body struct {
		Build runner.Job `json:"build"`
		Log   string     `json:"log"`
	}
}

type repositoryBuildMutationOutput struct {
	Body struct {
		Build runner.Job `json:"build"`
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

	huma.Register(api, protected(huma.Operation{
		OperationID:   "rerun-repository-build",
		Method:        http.MethodPost,
		Path:          "/api/repositories/{repository}/builds/{id}/rerun",
		Summary:       "Rerun a completed repository build",
		Tags:          []string{"Builds"},
		DefaultStatus: http.StatusCreated,
	}), service.rerunRepositoryBuild)

	huma.Register(api, protected(huma.Operation{
		OperationID: "cancel-repository-build",
		Method:      http.MethodPost,
		Path:        "/api/repositories/{repository}/builds/{id}/cancel",
		Summary:     "Cancel a queued or running repository build",
		Tags:        []string{"Builds"},
	}), service.cancelRepositoryBuild)
}

func (a API) listRepositoryBuilds(
	ctx context.Context,
	input *repositoryBuildsInput,
) (*repositoryBuildsOutput, error) {
	repository, err := parseRepositoryPath(input.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	principal, err := a.authorizeRepository(
		ctx,
		input.AuthInput,
		repository,
		control.RoleRead,
	)
	if err != nil {
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
	output.Body.CanManage = a.Coordinator != nil && principal.Role.Allows(control.RoleDeveloper)
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

func (a API) rerunRepositoryBuild(
	ctx context.Context,
	input *repositoryBuildInput,
) (*repositoryBuildMutationOutput, error) {
	repository, err := a.mutableBuildRepository(ctx, input)
	if err != nil {
		return nil, err
	}
	build, err := a.Coordinator.Rerun(repository, input.ID)
	if errors.Is(err, runner.ErrBuildNotFound) {
		return nil, huma.Error404NotFound("build not found")
	}
	if errors.Is(err, runner.ErrBuildNotRerunnable) {
		return nil, huma.Error409Conflict(err.Error())
	}
	if err != nil {
		return nil, huma.Error409Conflict("could not rerun build", err)
	}
	output := &repositoryBuildMutationOutput{}
	output.Body.Build = build
	return output, nil
}

func (a API) cancelRepositoryBuild(
	ctx context.Context,
	input *repositoryBuildInput,
) (*repositoryBuildMutationOutput, error) {
	repository, err := a.mutableBuildRepository(ctx, input)
	if err != nil {
		return nil, err
	}
	build, err := a.Coordinator.Cancel(repository, input.ID)
	if errors.Is(err, runner.ErrBuildNotFound) {
		return nil, huma.Error404NotFound("build not found")
	}
	if errors.Is(err, runner.ErrBuildNotCancelable) {
		return nil, huma.Error409Conflict(err.Error())
	}
	if err != nil {
		return nil, huma.Error409Conflict("could not cancel build", err)
	}
	output := &repositoryBuildMutationOutput{}
	output.Body.Build = build
	return output, nil
}

func (a API) mutableBuildRepository(
	ctx context.Context,
	input *repositoryBuildInput,
) (repopath.Repository, error) {
	repository, err := parseRepositoryPath(input.Repository)
	if err != nil {
		return repopath.Repository{}, huma.Error400BadRequest(err.Error())
	}
	if _, err = a.authorizeRepository(
		ctx,
		input.AuthInput,
		repository,
		control.RoleDeveloper,
	); err != nil {
		return repopath.Repository{}, err
	}
	if a.Coordinator == nil {
		return repopath.Repository{}, huma.Error503ServiceUnavailable(
			"remote build runner is not enabled",
		)
	}
	return repository, nil
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
