package runner

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

type libvirtGuestCommandError struct {
	Command string
	Err     error
}

func (e *libvirtGuestCommandError) Error() string {
	return fmt.Sprintf("%s: %v", e.Command, e.Err)
}

func (e *libvirtGuestCommandError) Unwrap() error {
	return e.Err
}

type libvirtGuestSSH interface {
	AuthorizedKey() string
	Run(context.Context, vmInstance, io.Reader, io.Writer, io.Writer, string) error
	ForgetHost(string)
}

var errLibvirtSSHHostKeyChanged = errors.New("libvirt SSH host key changed")

type libvirtSSHTransportError struct {
	message string
}

func (e *libvirtSSHTransportError) Error() string {
	return e.message
}

type nativeLibvirtSSH struct {
	user              string
	port              int
	signer            ssh.Signer
	connectTimeout    time.Duration
	keepAliveInterval time.Duration
	keepAliveCountMax int

	hostKeysMu sync.Mutex
	hostKeys   map[string][]byte
}

func newNativeLibvirtSSH(user string, port int) (*nativeLibvirtSSH, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate in-memory libvirt SSH identity: %w", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		return nil, fmt.Errorf("create in-memory libvirt SSH signer: %w", err)
	}
	return &nativeLibvirtSSH{
		user:              user,
		port:              port,
		signer:            signer,
		connectTimeout:    5 * time.Second,
		keepAliveInterval: 15 * time.Second,
		keepAliveCountMax: 3,
		hostKeys:          make(map[string][]byte),
	}, nil
}

func (s *nativeLibvirtSSH) AuthorizedKey() string {
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.signer.PublicKey())))
}

func (s *nativeLibvirtSSH) Run(
	ctx context.Context,
	instance vmInstance,
	input io.Reader,
	output io.Writer,
	errorOutput io.Writer,
	remoteCommand string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	client, err := s.dial(ctx, instance)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	keepAliveContext, stopKeepAlive := context.WithCancel(ctx)
	defer stopKeepAlive()
	go s.keepAlive(keepAliveContext, client)

	stopOnCancel := context.AfterFunc(ctx, func() { _ = client.Close() })
	defer stopOnCancel()
	session, err := client.NewSession()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return &libvirtSSHTransportError{message: "open VM SSH session failed"}
	}
	defer func() { _ = session.Close() }()
	if output == nil {
		output = io.Discard
	}
	if errorOutput == nil {
		errorOutput = io.Discard
	}
	streamMutex := &sync.Mutex{}
	session.Stdin = input
	session.Stdout = synchronizedWriter{mutex: streamMutex, writer: output}
	session.Stderr = synchronizedWriter{mutex: streamMutex, writer: errorOutput}
	if err = session.Run(remoteCommand); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	return nil
}

type synchronizedWriter struct {
	mutex  *sync.Mutex
	writer io.Writer
}

func (w synchronizedWriter) Write(contents []byte) (int, error) {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	return w.writer.Write(contents)
}

func (s *nativeLibvirtSSH) keepAlive(ctx context.Context, client *ssh.Client) {
	interval := s.keepAliveInterval
	if interval <= 0 || s.keepAliveCountMax <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		response := make(chan error, 1)
		go func() {
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			response <- err
		}()
		responseDeadline := time.NewTimer(interval * time.Duration(s.keepAliveCountMax))
		select {
		case <-ctx.Done():
			if !responseDeadline.Stop() {
				<-responseDeadline.C
			}
			return
		case err := <-response:
			if !responseDeadline.Stop() {
				<-responseDeadline.C
			}
			if err != nil {
				_ = client.Close()
				return
			}
		case <-responseDeadline.C:
			_ = client.Close()
			return
		}
	}
}

func (s *nativeLibvirtSSH) dial(ctx context.Context, instance vmInstance) (*ssh.Client, error) {
	address := net.JoinHostPort(instance.Address, strconv.Itoa(s.port))
	connectDeadline := time.Now().Add(s.connectTimeout)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(connectDeadline) {
		connectDeadline = deadline
	}
	dialer := net.Dialer{
		Deadline:  connectDeadline,
		KeepAlive: 15 * time.Second,
	}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, &libvirtSSHTransportError{message: safeLibvirtSSHDialError(err)}
	}
	closeOnCancel := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer closeOnCancel()
	defer func() {
		if connection != nil {
			_ = connection.Close()
		}
	}()

	if err = connection.SetDeadline(connectDeadline); err != nil {
		return nil, &libvirtSSHTransportError{message: "configure VM SSH connection deadline failed"}
	}
	clientConnection, channels, requests, err := ssh.NewClientConn(
		connection,
		address,
		&ssh.ClientConfig{
			User:            s.user,
			Auth:            []ssh.AuthMethod{ssh.PublicKeys(s.signer)},
			HostKeyCallback: s.hostKeyCallback(instance.Name),
		},
	)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		message := "VM SSH handshake or authentication failed"
		if errors.Is(err, errLibvirtSSHHostKeyChanged) {
			message = "VM SSH host key changed"
		} else if isTimeoutError(err) {
			message = "VM SSH handshake timed out"
		}
		return nil, &libvirtSSHTransportError{message: message}
	}
	if err = connection.SetDeadline(time.Time{}); err != nil {
		_ = clientConnection.Close()
		return nil, &libvirtSSHTransportError{message: "clear VM SSH connection deadline failed"}
	}
	if !closeOnCancel() {
		_ = clientConnection.Close()
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errors.New("SSH connection canceled during handshake")
	}
	connection = nil
	return ssh.NewClient(clientConnection, channels, requests), nil
}

func safeLibvirtSSHDialError(err error) string {
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return "VM SSH connection refused"
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return "VM SSH endpoint is unreachable"
	case isTimeoutError(err):
		return "VM SSH connection timed out"
	default:
		return "VM SSH connection failed"
	}
}

func isTimeoutError(err error) bool {
	var timeoutError interface{ Timeout() bool }
	return errors.As(err, &timeoutError) && timeoutError.Timeout()
}

func (s *nativeLibvirtSSH) hostKeyCallback(instanceName string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		presented := key.Marshal()
		s.hostKeysMu.Lock()
		defer s.hostKeysMu.Unlock()
		known, exists := s.hostKeys[instanceName]
		if !exists {
			s.hostKeys[instanceName] = bytes.Clone(presented)
			return nil
		}
		if !bytes.Equal(known, presented) {
			return fmt.Errorf("%w for VM %q", errLibvirtSSHHostKeyChanged, instanceName)
		}
		return nil
	}
}

func (s *nativeLibvirtSSH) ForgetHost(instanceName string) {
	s.hostKeysMu.Lock()
	delete(s.hostKeys, instanceName)
	s.hostKeysMu.Unlock()
}

func (p *libvirtRPCProvider) runSSH(
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
	if p.guestSSH == nil {
		return errors.New("libvirt guest SSH transport is not prepared")
	}
	err := p.guestSSH.Run(ctx, instance, input, output, errorOutput, remoteCommand)
	if err == nil {
		return nil
	}
	processErr := errors.New("remote command failed")
	if ctxErr := ctx.Err(); ctxErr != nil {
		processErr = ctxErr
	} else {
		var transportErr *libvirtSSHTransportError
		if errors.As(err, &transportErr) {
			processErr = errors.New(transportErr.message)
		}
	}
	return &libvirtGuestCommandError{
		Command: "ssh " + instance.Name,
		Err:     processErr,
	}
}

func (p *libvirtRPCProvider) verifyGuestReady(ctx context.Context, instance vmInstance) error {
	command := shellJoin([]string{p.config.DockerCommand, "info", "--format", "{{.ServerVersion}}"})
	return p.runSSH(ctx, instance, nil, io.Discard, io.Discard, command)
}

func (p *libvirtRPCProvider) Execute(
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

func (p *libvirtRPCProvider) dockerRunArguments(
	request ExecuteRequest,
	workspace string,
	containerName string,
) []string {
	environment := map[string]string{
		"CI":                   "true",
		"CI_JOB_NAME":          request.Job.Name,
		"CI_COMMIT_BRANCH":     request.Job.Branch,
		"CI_COMMIT_SHA":        request.Job.Commit,
		"CI_PROJECT_PATH":      request.Job.Repository,
		"GITONE_BUILD_ID":      request.Job.ID,
		"GITONE_JOB_NAME":      request.Job.Name,
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
		renderBuildScript(request.Config.Script),
	)
}

func (p *libvirtRPCProvider) uploadBuildDirectory(
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

func (p *libvirtRPCProvider) cleanupGuestBuild(
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
