package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
)

type ExecuteRequest struct {
	Job       Job
	Directory string
	Config    repoconfig.BuildConfig
}

type Executor interface {
	Run(context.Context, ExecuteRequest, io.Writer) error
}

// ContainerExecutor runs each build in an ephemeral Docker-compatible container.
type ContainerExecutor struct {
	Command string
}

func (e ContainerExecutor) Run(
	ctx context.Context,
	request ExecuteRequest,
	output io.Writer,
) error {
	command := e.Command
	if command == "" {
		command = "docker"
	}
	if !validJobID(request.Job.ID) {
		return errors.New("invalid build ID")
	}
	containerName := "gitone-" + request.Job.ID
	environment := map[string]string{
		"CI":                   "true",
		"CI_COMMIT_BRANCH":     request.Job.Branch,
		"CI_COMMIT_SHA":        request.Job.Commit,
		"CI_PROJECT_PATH":      request.Job.Repository,
		"GITONE_BUILD_ID":      request.Job.ID,
		"GITONE_REPOSITORY":    request.Job.Repository,
		"GITONE_COMMIT_SHA":    request.Job.Commit,
		"GITONE_COMMIT_BRANCH": request.Job.Branch,
	}
	for name, value := range request.Config.Environment {
		environment[name] = value
	}
	names := make([]string, 0, len(environment))
	for name := range environment {
		names = append(names, name)
	}
	sort.Strings(names)

	arguments := []string{
		"run",
		"--rm",
		"--init",
		"--name", containerName,
		"--label", "gitone.build=" + request.Job.ID,
		"--workdir", "/workspace",
		"--volume", request.Directory + ":/workspace",
		"--entrypoint", "/bin/sh",
	}
	for _, name := range names {
		arguments = append(arguments, "--env", name+"="+environment[name])
	}
	arguments = append(
		arguments,
		request.Config.Image,
		"-ec",
		strings.Join(request.Config.Script, "\n"),
	)
	process := exec.CommandContext(ctx, command, arguments...)
	process.Stdout = output
	process.Stderr = output
	if err := process.Run(); err != nil {
		if ctx.Err() != nil {
			cleanupContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cleanup := exec.CommandContext(cleanupContext, command, "rm", "--force", containerName)
			_ = cleanup.Run()
			return fmt.Errorf("container build: %w", ctx.Err())
		}
		return fmt.Errorf("container build: %w", err)
	}
	return nil
}
