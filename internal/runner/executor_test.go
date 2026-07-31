package runner

import (
	"bytes"
	"context"
	"errors"
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
			Repository: "engineering/api",
			Branch:     "main",
			Commit:     strings.Repeat("1", 40),
		},
		Directory: workspace,
		Config: repoconfig.BuildConfig{
			Image: "alpine:3.22",
			Script: []string{
				`test "$CI_PROJECT_PATH" = "engineering/api"`,
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
		Config: repoconfig.BuildConfig{
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
