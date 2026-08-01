package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
)

func TestContainerExecutorWithDocker(t *testing.T) {
	if os.Getenv("GITONE_RUNNER_DOCKER_TEST") != "1" {
		t.Skip("set GITONE_RUNNER_DOCKER_TEST=1 to run the Docker executor test")
	}
	workspace := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(workspace, "source.txt"),
		[]byte("source from workspace\n"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	request := ExecuteRequest{
		Job: Job{
			ID:         "build-test",
			Name:       "test",
			Repository: "engineering/api",
			Branch:     "main",
			Commit:     strings.Repeat("1", 40),
		},
		Directory: workspace,
		Config: repoconfig.JobConfig{
			Image: "alpine:3.22",
			Script: []string{
				`test "$CI_PROJECT_PATH" = "engineering/api"`,
				`test "$CI_JOB_NAME" = "test"`,
				"cat source.txt",
			},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var output bytes.Buffer
	if err := (ContainerExecutor{}).Run(ctx, request, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `$ test "$CI_PROJECT_PATH" = "engineering/api"`+"\n") ||
		!strings.Contains(output.String(), "$ cat source.txt\nsource from workspace\n") {
		t.Fatalf("unexpected container output: %q", output.String())
	}
}

func TestRenderedBuildScriptLogsCommandsWithoutExpandingMarkers(t *testing.T) {
	commands := []string{
		`printf 'value=%s\n' "$EXAMPLE"`,
		"false",
		"echo skipped",
	}
	process := exec.Command("/bin/sh", "-ec", renderBuildScript(commands))
	process.Env = append(os.Environ(), "EXAMPLE=expanded-value")
	output, err := process.CombinedOutput()
	if err == nil {
		t.Fatal("failing build script succeeded")
	}
	want := "$ printf 'value=%s\\n' \"$EXAMPLE\"\n" +
		"value=expanded-value\n" +
		"$ false\n"
	if string(output) != want {
		t.Fatalf("build script output = %q, want %q", output, want)
	}
}

func TestRenderedBuildScriptMarksMultilineCommands(t *testing.T) {
	command := "printf 'first\\n'\nprintf 'second\\n'"
	output, err := exec.Command("/bin/sh", "-ec", renderBuildScript([]string{command})).CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	want := "$ printf 'first\\n'\n" +
		"  printf 'second\\n'\n" +
		"first\nsecond\n"
	if string(output) != want {
		t.Fatalf("multiline build script output = %q, want %q", output, want)
	}
}

func TestContainerExecutorRemovesTimedOutContainer(t *testing.T) {
	if os.Getenv("GITONE_RUNNER_DOCKER_TEST") != "1" {
		t.Skip("set GITONE_RUNNER_DOCKER_TEST=1 to run the Docker executor test")
	}
	request := ExecuteRequest{
		Job: Job{
			ID:         "build-timeout-test",
			Repository: "engineering/api",
			Branch:     "main",
			Commit:     strings.Repeat("2", 40),
		},
		Directory: t.TempDir(),
		Config: repoconfig.JobConfig{
			Image:  "alpine:3.22",
			Script: []string{"sleep 30"},
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var output bytes.Buffer
	err := (ContainerExecutor{}).Run(ctx, request, &output)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed-out build error = %v", err)
	}
	list := exec.Command(
		"docker",
		"ps",
		"--all",
		"--filter", "name=^/gitone-"+request.Job.ID+"$",
		"--format", "{{.Names}}",
	)
	containers, listErr := list.Output()
	if listErr != nil {
		t.Fatal(listErr)
	}
	if strings.TrimSpace(string(containers)) != "" {
		t.Fatalf("timed-out container still exists: %s", containers)
	}
}

func TestContainerExecutorBuildsDeterministicCommand(t *testing.T) {
	directory := t.TempDir()
	argumentsPath := filepath.Join(directory, "arguments")
	commandPath := filepath.Join(directory, "container-command")
	command := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(argumentsPath) + "\n"
	if err := os.WriteFile(commandPath, []byte(command), 0o750); err != nil {
		t.Fatal(err)
	}
	request := ExecuteRequest{
		Job: Job{
			ID:         "build-42",
			Name:       "unit-test",
			Repository: "engineering/api",
			Branch:     "main",
			Commit:     strings.Repeat("a", 40),
		},
		Directory: directory,
		Config: repoconfig.JobConfig{
			Image:       "example.invalid/build:latest",
			Script:      []string{"go test ./..."},
			Environment: map[string]string{"Z_LAST": "z", "A_FIRST": "a"},
		},
	}
	if err := (ContainerExecutor{Command: commandPath}).Run(
		context.Background(),
		request,
		io.Discard,
	); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(argumentsPath)
	if err != nil {
		t.Fatal(err)
	}
	arguments := strings.Split(strings.TrimSpace(string(contents)), "\n")
	joined := strings.Join(arguments, "\n")
	for _, expected := range []string{
		"run\n--rm\n--init",
		"--name\ngitone-build-42",
		"--volume\n" + directory + ":/workspace",
		"A_FIRST=a",
		"Z_LAST=z",
		"example.invalid/build:latest\n-ec\nprintf '%s\\n' '$ go test ./...'",
	} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("container arguments do not contain %q:\n%s", expected, joined)
		}
	}
	if strings.Index(joined, "A_FIRST=a") > strings.Index(joined, "Z_LAST=z") {
		t.Fatalf("environment arguments are not sorted:\n%s", joined)
	}
}

func TestContainerExecutorReportsCommandFailure(t *testing.T) {
	commandPath := filepath.Join(t.TempDir(), "failing-container-command")
	if err := os.WriteFile(commandPath, []byte("#!/bin/sh\nexit 17\n"), 0o750); err != nil {
		t.Fatal(err)
	}
	err := (ContainerExecutor{Command: commandPath}).Run(
		context.Background(),
		ExecuteRequest{
			Job:    Job{ID: "build-failure"},
			Config: repoconfig.JobConfig{Image: "image"},
		},
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "container build") {
		t.Fatalf("command failure = %v", err)
	}
}

func TestContainerExecutorCleansUpAfterContextCancellation(t *testing.T) {
	commandPath := filepath.Join(t.TempDir(), "blocking-container-command")
	command := "#!/bin/sh\nif [ \"$1\" = rm ]; then exit 0; fi\nexec sleep 10\n"
	if err := os.WriteFile(commandPath, []byte(command), 0o750); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := (ContainerExecutor{Command: commandPath}).Run(
		ctx,
		ExecuteRequest{
			Job:    Job{ID: "build-canceled"},
			Config: repoconfig.JobConfig{Image: "image"},
		},
		io.Discard,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled build error = %v", err)
	}
}

func TestContainerExecutorRejectsTraversalBuildID(t *testing.T) {
	err := (ContainerExecutor{}).Run(
		context.Background(),
		ExecuteRequest{Job: Job{ID: "../escape"}},
		io.Discard,
	)
	if err == nil || !strings.Contains(err.Error(), "invalid build ID") {
		t.Fatalf("invalid build ID error = %v", err)
	}
}
