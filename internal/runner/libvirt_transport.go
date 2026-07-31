package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type libvirtCommandRunner interface {
	LookPath(string) (string, error)
	Run(
		context.Context,
		string,
		[]string,
		io.Reader,
		io.Writer,
		io.Writer,
	) error
}

type systemLibvirtCommandRunner struct{}

func (systemLibvirtCommandRunner) LookPath(command string) (string, error) {
	return exec.LookPath(command)
}

func (systemLibvirtCommandRunner) Run(
	ctx context.Context,
	command string,
	arguments []string,
	input io.Reader,
	output io.Writer,
	errorOutput io.Writer,
) error {
	process := exec.CommandContext(ctx, command, arguments...)
	process.Stdin = input
	process.Stdout = output
	process.Stderr = errorOutput
	process.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	process.WaitDelay = 5 * time.Second
	return process.Run()
}

type libvirtCommandError struct {
	Command string
	Err     error
	Stderr  string
}

func (e *libvirtCommandError) Error() string {
	if e.Stderr == "" {
		return fmt.Sprintf("%s: %v", e.Command, e.Err)
	}
	return fmt.Sprintf("%s: %v: %s", e.Command, e.Err, e.Stderr)
}

func (e *libvirtCommandError) Unwrap() error {
	return e.Err
}

func runLibvirtCommandCapture(
	ctx context.Context,
	runner libvirtCommandRunner,
	command string,
	arguments ...string,
) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := runner.Run(ctx, command, arguments, nil, &stdout, &stderr)
	if err != nil {
		return strings.TrimSpace(stdout.String()), &libvirtCommandError{
			Command: commandSummary(command, arguments),
			Err:     err,
			Stderr:  strings.TrimSpace(stderr.String()),
		}
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runLibvirtCommand(
	ctx context.Context,
	runner libvirtCommandRunner,
	command string,
	arguments []string,
	summary string,
	input io.Reader,
	output io.Writer,
	errorOutput io.Writer,
) error {
	if output == nil {
		output = io.Discard
	}
	if errorOutput == nil {
		errorOutput = io.Discard
	}
	err := runner.Run(ctx, command, arguments, input, output, errorOutput)
	if err == nil {
		return nil
	}
	return &libvirtCommandError{
		Command: summary,
		Err:     err,
	}
}

func commandSummary(command string, arguments []string) string {
	parts := []string{filepath.Base(command)}
	for _, argument := range arguments {
		if strings.Contains(strings.ToLower(argument), "password") {
			parts = append(parts, "<redacted>")
			continue
		}
		parts = append(parts, argument)
	}
	return strings.Join(parts, " ")
}

func (p *virshVMProvider) sshArguments(instance vmInstance, remoteCommand string) []string {
	return []string{
		"-p", strconv.Itoa(p.config.SSHPort),
		"-i", p.config.SSHKeyPath,
		"-o", "BatchMode=yes",
		"-o", "IdentitiesOnly=yes",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + p.instanceKnownHostsPath(instance),
		"-o", "HostKeyAlias=" + instance.Name,
		"-o", "ConnectTimeout=5",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		p.config.SSHUser + "@" + instance.Address,
		remoteCommand,
	}
}

func (p *virshVMProvider) runSSH(
	ctx context.Context,
	instance vmInstance,
	input io.Reader,
	output io.Writer,
	errorOutput io.Writer,
	remoteCommand string,
) error {
	if net.ParseIP(instance.Address) == nil {
		return fmt.Errorf("invalid VM address %q", instance.Address)
	}
	err := runLibvirtCommand(
		ctx,
		p.runner,
		p.config.SSHCommand,
		p.sshArguments(instance, remoteCommand),
		"ssh "+instance.Name,
		input,
		output,
		errorOutput,
	)
	if err == nil {
		return nil
	}
	var commandErr *libvirtCommandError
	if errors.As(err, &commandErr) {
		var processErr error
		if ctxErr := ctx.Err(); ctxErr != nil {
			processErr = ctxErr
		} else {
			processErr = errors.New("remote command failed")
		}
		return &libvirtCommandError{
			Command: "ssh " + instance.Name,
			Err:     processErr,
		}
	}
	return fmt.Errorf("ssh %s: %w", instance.Name, err)
}

func (p *virshVMProvider) verifyGuestReady(ctx context.Context, instance vmInstance) error {
	command := shellJoin([]string{p.config.DockerCommand, "info", "--format", "{{.ServerVersion}}"})
	return p.runSSH(ctx, instance, nil, io.Discard, io.Discard, command)
}

func (p *virshVMProvider) Execute(
	ctx context.Context,
	instance vmInstance,
	request ExecuteRequest,
	output io.Writer,
) error {
	if !p.ownsInstance(instance) {
		return errors.New("refusing to execute on an unmanaged VM")
	}
	if !validJobID(request.Job.ID) {
		return errors.New("invalid build ID")
	}
	if err := request.Config.Validate(); err != nil {
		return fmt.Errorf("validate build configuration: %w", err)
	}
	info, err := os.Stat(request.Directory)
	if err != nil {
		return fmt.Errorf("inspect build directory: %w", err)
	}
	if !info.IsDir() {
		return errors.New("build directory must be a directory")
	}
	if output == nil {
		output = io.Discard
	}

	workspace := "/var/tmp/gitone-" + request.Job.ID
	containerName := "gitone-" + request.Job.ID
	uploadErr := p.uploadBuildDirectory(ctx, instance, request.Directory, workspace)
	if uploadErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("upload build context: %w", ctxErr)
		}
		cleanupErr := p.cleanupGuestBuild(instance, workspace, containerName)
		return errors.Join(fmt.Errorf("upload build context: %w", uploadErr), cleanupErr)
	}

	arguments := p.dockerRunArguments(request, workspace, containerName)
	runErr := p.runSSH(
		ctx,
		instance,
		nil,
		output,
		output,
		shellJoin(arguments),
	)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("KVM container build: %w", ctxErr)
	}
	cleanupErr := p.cleanupGuestBuild(instance, workspace, containerName)
	if runErr != nil {
		runErr = fmt.Errorf("KVM container build: %w", runErr)
	}
	return errors.Join(runErr, cleanupErr)
}

func (p *virshVMProvider) dockerRunArguments(
	request ExecuteRequest,
	workspace string,
	containerName string,
) []string {
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
		p.config.DockerCommand,
		"run",
		"--rm",
		"--init",
		"--name", containerName,
		"--label", "gitone.build=" + request.Job.ID,
		"--workdir", "/workspace",
		"--volume", workspace + ":/workspace",
		"--entrypoint", "/bin/sh",
	}
	for _, name := range names {
		arguments = append(arguments, "--env", name+"="+environment[name])
	}
	return append(
		arguments,
		request.Config.Image,
		"-ec",
		strings.Join(request.Config.Script, "\n"),
	)
}

func (p *virshVMProvider) uploadBuildDirectory(
	ctx context.Context,
	instance vmInstance,
	directory string,
	workspace string,
) error {
	archiveReader, archiveWriter := io.Pipe()
	archiveErrors := make(chan error, 1)
	go func() {
		err := writeDirectoryTar(ctx, directory, archiveWriter)
		archiveErrors <- err
		_ = archiveWriter.CloseWithError(err)
	}()

	remoteCommand := "umask 077; " + strings.Join([]string{
		"rm -rf -- " + shellQuote(workspace),
		"mkdir -p -- " + shellQuote(workspace),
		"tar -xf - -C " + shellQuote(workspace),
		"chmod -R a+rwX -- " + shellQuote(workspace),
	}, " && ")
	sshErr := p.runSSH(ctx, instance, archiveReader, io.Discard, io.Discard, remoteCommand)
	_ = archiveReader.CloseWithError(sshErr)
	archiveErr := <-archiveErrors
	return errors.Join(sshErr, archiveErr)
}

func (p *virshVMProvider) cleanupGuestBuild(
	instance vmInstance,
	workspace string,
	containerName string,
) error {
	timeout := p.config.CleanupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := shellJoin([]string{p.config.DockerCommand, "rm", "--force", containerName}) +
		" >/dev/null 2>&1 || true; rm -rf -- " + shellQuote(workspace)
	if err := p.runSSH(ctx, instance, nil, io.Discard, io.Discard, command); err != nil {
		return fmt.Errorf("clean guest build workspace: %w", err)
	}
	return nil
}

func writeDirectoryTar(ctx context.Context, root string, output io.Writer) error {
	root = filepath.Clean(root)
	archive := tar.NewWriter(output)
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe build path %q", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		var linkTarget string
		if info.Mode()&os.ModeSymlink != 0 {
			linkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, linkTarget)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Uid = 0
		header.Gid = 0
		header.Uname = ""
		header.Gname = ""
		if info.IsDir() {
			header.Name += "/"
		}
		if err = archive.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil
			}
			return fmt.Errorf("unsupported build file %q with mode %s", relative, info.Mode())
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(archive, &contextReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		return errors.Join(copyErr, closeErr)
	})
	return errors.Join(walkErr, archive.Close())
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

func shellJoin(arguments []string) string {
	quoted := make([]string, len(arguments))
	for index, argument := range arguments {
		quoted[index] = shellQuote(argument)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
