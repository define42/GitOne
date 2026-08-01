package runner

import (
	"context"
	"io"
	"strings"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
)

type ExecuteRequest struct {
	Job       Job
	Directory string
	Config    repoconfig.JobConfig
}

type Executor interface {
	Run(context.Context, ExecuteRequest, io.Writer) error
}

// ExecutorLifecycle lets an executor prepare shared resources before the
// remote runner starts claiming work and clean them up after its workers stop.
type ExecutorLifecycle interface {
	Start(context.Context) error
	Shutdown(context.Context) error
}

// ExecutorShutdownTimeoutProvider overrides the remote runner's default
// bounded shutdown window for executors with their own cleanup policy.
type ExecutorShutdownTimeoutProvider interface {
	ShutdownTimeout() time.Duration
}

// ExecutorReservation is a build executor backed by capacity reserved before
// a remote job is claimed. Release returns that capacity to its owner or tears
// it down after use.
type ExecutorReservation interface {
	Executor
	Release(context.Context) error
}

// ExecutorReleaseTimeoutProvider overrides the default bounded release window
// for a reserved execution environment.
type ExecutorReleaseTimeoutProvider interface {
	ReleaseTimeout() time.Duration
}

// AssignableExecutorReservation is notified once the remote runner has
// successfully claimed a job for the reserved capacity.
type AssignableExecutorReservation interface {
	ExecutorReservation
	Assign()
}

// ReservingExecutor reserves ready execution capacity before the remote runner
// asks the server for a job.
type ReservingExecutor interface {
	Reserve(context.Context) (ExecutorReservation, error)
}

// renderBuildScript writes each configured command to the build log before
// executing it. The displayed command is shell-quoted as data rather than
// traced with `set -x`, so environment values expanded by the shell are not
// accidentally included in the command marker.
func renderBuildScript(commands []string) string {
	var script strings.Builder
	for index, command := range commands {
		if index > 0 {
			script.WriteByte('\n')
		}
		display := "$ " + strings.ReplaceAll(command, "\n", "\n  ")
		script.WriteString("printf '%s\\n' ")
		script.WriteString(shellQuote(display))
		script.WriteByte('\n')
		script.WriteString(command)
	}
	return script.String()
}
