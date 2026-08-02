package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/runner"
	"github.com/go-git/go-git/v5/plumbing"
)

type runnerClaimBody struct {
	RunnerID string `json:"runnerId" minLength:"1" maxLength:"100"`
}

type runnerClaimInput struct {
	Authorization string `header:"Authorization" hidden:"true"`
	Body          runnerClaimBody
}

type runnerClaimOutput struct {
	Body struct {
		Lease *runner.Lease `json:"lease,omitempty"`
	}
}

type runnerJobBody struct {
	RunnerID   string `json:"runnerId" minLength:"1" maxLength:"100"`
	Repository string `json:"repository" minLength:"3" maxLength:"1000"`
	ID         string `json:"id" minLength:"1" maxLength:"100"`
}

type runnerJobInput struct {
	Authorization string `header:"Authorization" hidden:"true"`
	Body          runnerJobBody
}

type runnerHeartbeatOutput struct {
	Body struct {
		LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
		Canceled       bool      `json:"canceled,omitempty"`
	}
}

type runnerLogBody struct {
	RunnerID   string `json:"runnerId" minLength:"1" maxLength:"100"`
	Repository string `json:"repository" minLength:"3" maxLength:"1000"`
	ID         string `json:"id" minLength:"1" maxLength:"100"`
	Offset     int64  `json:"offset" minimum:"0"`
	Content    []byte `json:"content" maxLength:"65536"`
}

type runnerLogInput struct {
	Authorization string `header:"Authorization" hidden:"true"`
	Body          runnerLogBody
}

type runnerLogOutput struct {
	Body struct {
		Offset int64 `json:"offset"`
	}
}

type runnerCompleteBody struct {
	RunnerID   string `json:"runnerId" minLength:"1" maxLength:"100"`
	Repository string `json:"repository" minLength:"3" maxLength:"1000"`
	ID         string `json:"id" minLength:"1" maxLength:"100"`
	Error      string `json:"error,omitempty" maxLength:"4096"`
}

type runnerCompleteInput struct {
	Authorization string `header:"Authorization" hidden:"true"`
	Body          runnerCompleteBody
}

type runnerCompleteOutput struct {
	Body struct {
		Build runner.Job `json:"build"`
	}
}

func registerRunnerAPI(api huma.API, service API) {
	if service.Coordinator == nil {
		return
	}
	huma.Register(api, runnerProtected(huma.Operation{
		OperationID: "claim-runner-job",
		Method:      http.MethodPost,
		Path:        "/api/runner/jobs/claim",
		Summary:     "Claim the oldest available build",
		Tags:        []string{"Runner"},
	}), service.claimRunnerJob)

	huma.Register(api, runnerProtected(huma.Operation{
		OperationID: "heartbeat-runner-job",
		Method:      http.MethodPost,
		Path:        "/api/runner/jobs/heartbeat",
		Summary:     "Extend a build lease",
		Tags:        []string{"Runner"},
	}), service.heartbeatRunnerJob)

	huma.Register(api, runnerProtected(huma.Operation{
		OperationID: "append-runner-job-log",
		Method:      http.MethodPost,
		Path:        "/api/runner/jobs/log",
		Summary:     "Append output to a build log",
		Tags:        []string{"Runner"},
	}), service.appendRunnerJobLog)

	huma.Register(api, runnerProtected(huma.Operation{
		OperationID: "complete-runner-job",
		Method:      http.MethodPost,
		Path:        "/api/runner/jobs/complete",
		Summary:     "Complete a leased build",
		Tags:        []string{"Runner"},
	}), service.completeRunnerJob)
}

func runnerProtected(operation huma.Operation) huma.Operation {
	operation.Security = []map[string][]string{{"runnerAuth": {}}}
	return operation
}

func (a API) claimRunnerJob(
	_ context.Context,
	input *runnerClaimInput,
) (*runnerClaimOutput, error) {
	if err := a.authorizeRunner(input.Authorization); err != nil {
		return nil, err
	}
	lease, err := a.Coordinator.Claim(input.Body.RunnerID)
	if err != nil {
		return nil, huma.Error500InternalServerError("could not claim build", err)
	}
	output := &runnerClaimOutput{}
	output.Body.Lease = lease
	return output, nil
}

func (a API) heartbeatRunnerJob(
	_ context.Context,
	input *runnerJobInput,
) (*runnerHeartbeatOutput, error) {
	if err := a.authorizeRunner(input.Authorization); err != nil {
		return nil, err
	}
	repository, err := parseRepositoryPath(input.Body.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	result, err := a.Coordinator.HeartbeatState(
		repository,
		input.Body.ID,
		input.Body.RunnerID,
	)
	if err != nil {
		return nil, huma.Error409Conflict("could not extend build lease", err)
	}
	output := &runnerHeartbeatOutput{}
	output.Body.LeaseExpiresAt = result.LeaseExpiresAt
	output.Body.Canceled = result.Canceled
	return output, nil
}

func (a API) appendRunnerJobLog(
	_ context.Context,
	input *runnerLogInput,
) (*runnerLogOutput, error) {
	if err := a.authorizeRunner(input.Authorization); err != nil {
		return nil, err
	}
	repository, err := parseRepositoryPath(input.Body.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	offset, err := a.Coordinator.AppendLog(
		repository,
		input.Body.ID,
		input.Body.RunnerID,
		input.Body.Offset,
		input.Body.Content,
	)
	if err != nil {
		return nil, huma.Error409Conflict("could not append build log", err)
	}
	output := &runnerLogOutput{}
	output.Body.Offset = offset
	return output, nil
}

func (a API) completeRunnerJob(
	_ context.Context,
	input *runnerCompleteInput,
) (*runnerCompleteOutput, error) {
	if err := a.authorizeRunner(input.Authorization); err != nil {
		return nil, err
	}
	repository, err := parseRepositoryPath(input.Body.Repository)
	if err != nil {
		return nil, huma.Error400BadRequest(err.Error())
	}
	job, err := a.Coordinator.Complete(
		repository,
		input.Body.ID,
		input.Body.RunnerID,
		input.Body.Error,
	)
	if err != nil {
		return nil, huma.Error409Conflict("could not complete build", err)
	}
	output := &runnerCompleteOutput{}
	output.Body.Build = job
	return output, nil
}

func (a API) runnerSource(response http.ResponseWriter, request *http.Request) {
	if err := a.authorizeRunner(request.Header.Get("Authorization")); err != nil {
		writeRunnerError(response, err)
		return
	}
	repository, err := parseRepositoryPath(request.URL.Query().Get("repository"))
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	job, err := a.Coordinator.SourceJob(
		repository,
		request.URL.Query().Get("id"),
		request.URL.Query().Get("runnerId"),
	)
	if err != nil {
		http.Error(response, err.Error(), http.StatusConflict)
		return
	}
	response.Header().Set("Content-Type", "application/gzip")
	response.Header().Set(
		"Content-Disposition",
		`attachment; filename="`+job.ID+`.tar.gz"`,
	)
	response.Header().Set("Cache-Control", "no-store")
	if err = runner.WriteSourceArchive(
		a.Storage,
		repository,
		plumbing.NewHash(job.Commit),
		response,
	); err != nil {
		return
	}
}

func (a API) authorizeRunner(authorization string) error {
	if a.Coordinator == nil || a.RunnerToken == "" {
		return huma.Error503ServiceUnavailable("remote runner API is not enabled")
	}
	if !strings.HasPrefix(authorization, "Bearer ") {
		return huma.Error401Unauthorized("runner authentication required")
	}
	provided := sha256.Sum256([]byte(strings.TrimPrefix(authorization, "Bearer ")))
	expected := sha256.Sum256([]byte(a.RunnerToken))
	if subtle.ConstantTimeCompare(provided[:], expected[:]) != 1 {
		return huma.Error401Unauthorized("invalid runner token")
	}
	return nil
}

func writeRunnerError(response http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	var apiError huma.StatusError
	if errors.As(err, &apiError) {
		status = apiError.GetStatus()
	}
	response.Header().Set("Content-Type", "application/problem+json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(map[string]any{
		"status": status,
		"title":  http.StatusText(status),
	})
}
