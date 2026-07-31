package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/define42/GitOne/internal/repoconfig"
)

type recordingLibvirtCommandRunner struct {
	command   string
	arguments []string
	run       func(io.Reader, io.Writer, io.Writer) error
}

func (r *recordingLibvirtCommandRunner) LookPath(command string) (string, error) {
	return command, nil
}

func (r *recordingLibvirtCommandRunner) Run(
	_ context.Context,
	command string,
	arguments []string,
	input io.Reader,
	output io.Writer,
	errorOutput io.Writer,
) error {
	r.command = command
	r.arguments = append([]string(nil), arguments...)
	if r.run == nil {
		return nil
	}
	return r.run(input, output, errorOutput)
}

func TestRunSSHDoesNotExposeRemoteCommand(t *testing.T) {
	const secret = "TOP_SECRET_BUILD_VALUE"
	runner := &recordingLibvirtCommandRunner{
		run: func(_ io.Reader, _ io.Writer, errorOutput io.Writer) error {
			_, _ = io.WriteString(errorOutput, "remote shell diagnostic")
			return errors.New("exit status 17")
		},
	}
	provider := &virshVMProvider{
		config: LibvirtConfig{
			SSHCommand:    "/usr/bin/ssh",
			SSHPort:       22,
			SSHKeyPath:    "/run/gitone/id_ed25519",
			SSHUser:       "core",
			DockerCommand: "docker",
			PoolPath:      "/run/gitone",
		},
		runner: runner,
	}
	instance := vmInstance{Name: "owned-warm-vm", Address: "192.0.2.20"}
	remoteCommand := "docker run --env TOKEN=" + secret + " image /bin/sh -ec 'echo secret'"
	err := provider.runSSH(context.Background(), instance, nil, io.Discard, io.Discard, remoteCommand)
	if err == nil {
		t.Fatal("runSSH succeeded unexpectedly")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "echo secret") ||
		strings.Contains(err.Error(), "remote shell diagnostic") {
		t.Fatalf("SSH error exposed remote content: %v", err)
	}
	if !strings.Contains(err.Error(), "ssh "+instance.Name) {
		t.Fatalf("SSH error does not identify the instance: %v", err)
	}
	if runner.command != "/usr/bin/ssh" || runner.arguments[len(runner.arguments)-1] != remoteCommand {
		t.Fatalf("SSH invocation = %q %#v", runner.command, runner.arguments)
	}
	wantKnownHosts := "UserKnownHostsFile=" + provider.instanceKnownHostsPath(instance)
	if !containsString(runner.arguments, wantKnownHosts) {
		t.Fatalf("SSH did not use per-instance known-hosts file %q: %#v", wantKnownHosts, runner.arguments)
	}
}

func TestRunLibvirtCommandDoesNotCaptureStreamedStderr(t *testing.T) {
	const stderrBytes = 4 << 20
	runner := &recordingLibvirtCommandRunner{
		run: func(_ io.Reader, _ io.Writer, errorOutput io.Writer) error {
			block := bytes.Repeat([]byte("x"), 4096)
			for written := 0; written < stderrBytes; written += len(block) {
				if _, err := errorOutput.Write(block); err != nil {
					return err
				}
			}
			return errors.New("exit status 1")
		},
	}
	err := runLibvirtCommand(
		context.Background(),
		runner,
		"ssh",
		[]string{"host", "remote command"},
		"ssh test-vm",
		nil,
		io.Discard,
		io.Discard,
	)
	if err == nil {
		t.Fatal("command succeeded unexpectedly")
	}
	if len(err.Error()) > 256 || strings.Contains(err.Error(), strings.Repeat("x", 32)) {
		t.Fatalf("command error retained streamed stderr: length=%d", len(err.Error()))
	}
}

func TestDockerRunArgumentsMatchContainerIsolationContract(t *testing.T) {
	provider := &virshVMProvider{config: LibvirtConfig{DockerCommand: "/usr/bin/docker"}}
	request := ExecuteRequest{
		Job: Job{
			ID:         "build-1",
			Repository: "group/project",
			Branch:     "main",
			Commit:     "0123456789abcdef",
		},
		Config: repoconfig.BuildConfig{
			Image:  "alpine:3.22",
			Script: []string{"printf '%s\\n' ready", "make test"},
			Environment: map[string]string{
				"Z_VALUE": "last",
				"A_VALUE": "first",
			},
		},
	}
	arguments := provider.dockerRunArguments(request, "/var/tmp/gitone-build-1", "gitone-build-1")
	wantPrefix := []string{
		"/usr/bin/docker", "run", "--rm", "--init",
		"--name", "gitone-build-1",
		"--label", "gitone.build=build-1",
		"--workdir", "/workspace",
		"--volume", "/var/tmp/gitone-build-1:/workspace",
		"--entrypoint", "/bin/sh",
	}
	if len(arguments) < len(wantPrefix) || !reflect.DeepEqual(arguments[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("Docker arguments = %#v", arguments)
	}
	var environment []string
	for index := len(wantPrefix); index+1 < len(arguments) && arguments[index] == "--env"; index += 2 {
		environment = append(environment, arguments[index+1])
	}
	if !sortStringsAreOrdered(environment) ||
		!containsString(environment, "A_VALUE=first") || !containsString(environment, "Z_VALUE=last") ||
		!containsString(environment, "CI_PROJECT_PATH=group/project") {
		t.Fatalf("Docker environment = %#v", environment)
	}
	wantSuffix := []string{"alpine:3.22", "-ec", "printf '%s\\n' ready\nmake test"}
	if !reflect.DeepEqual(arguments[len(arguments)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("Docker argument suffix = %#v", arguments[len(arguments)-len(wantSuffix):])
	}
}

func TestUploadMakesWorkspaceUsableByNonRootImageUser(t *testing.T) {
	runner := &recordingLibvirtCommandRunner{
		run: func(input io.Reader, _ io.Writer, _ io.Writer) error {
			_, err := io.Copy(io.Discard, input)
			return err
		},
	}
	provider := &virshVMProvider{
		config: LibvirtConfig{
			SSHCommand: "/usr/bin/ssh",
			SSHPort:    22,
			SSHKeyPath: "/run/gitone/id_ed25519",
			SSHUser:    "core",
			PoolPath:   "/run/gitone",
		},
		runner: runner,
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "source.txt"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	instance := vmInstance{Name: "owned-warm-vm", Address: "192.0.2.20"}
	if err := provider.uploadBuildDirectory(
		context.Background(),
		instance,
		directory,
		"/var/tmp/gitone-build-1",
	); err != nil {
		t.Fatal(err)
	}
	remoteCommand := runner.arguments[len(runner.arguments)-1]
	if !strings.Contains(remoteCommand, "chmod -R a+rwX -- '/var/tmp/gitone-build-1'") {
		t.Fatalf("upload did not make the workspace usable by an arbitrary image user: %q", remoteCommand)
	}
}

func TestWriteDirectoryTarPreservesFilesAndSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file.txt"), []byte("payload"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("nested/file.txt", filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks are unavailable: %v", err)
	}
	var archive bytes.Buffer
	if err := writeDirectoryTar(context.Background(), root, &archive); err != nil {
		t.Fatal(err)
	}
	entries := make(map[string]*tar.Header)
	contents := make(map[string]string)
	reader := tar.NewReader(bytes.NewReader(archive.Bytes()))
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		copyHeader := *header
		entries[header.Name] = &copyHeader
		data, readErr := io.ReadAll(reader)
		if readErr != nil {
			t.Fatal(readErr)
		}
		contents[header.Name] = string(data)
	}
	if entries["nested/"] == nil || entries["nested/file.txt"] == nil || entries["link"] == nil {
		t.Fatalf("tar entries = %#v", entries)
	}
	if contents["nested/file.txt"] != "payload" || entries["link"].Typeflag != tar.TypeSymlink ||
		entries["link"].Linkname != "nested/file.txt" {
		t.Fatalf("tar contents = %#v, link = %#v", contents, entries["link"])
	}
	if entries["nested/file.txt"].Uid != 0 || entries["nested/file.txt"].Gid != 0 {
		t.Fatalf("tar ownership was not normalized: %#v", entries["nested/file.txt"])
	}
}

func TestWriteDirectoryTarHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := writeDirectoryTar(ctx, t.TempDir(), io.Discard)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("writeDirectoryTar error = %v", err)
	}
}

func TestShellQuoteTreatsEveryArgumentAsData(t *testing.T) {
	got := shellJoin([]string{"docker", "", "a'b", "$(touch /tmp/never)"})
	want := "'docker' '' 'a'\"'\"'b' '$(touch /tmp/never)'"
	if got != want {
		t.Fatalf("shellJoin = %q, want %q", got, want)
	}
}

func sortStringsAreOrdered(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] > values[index] {
			return false
		}
	}
	return true
}
