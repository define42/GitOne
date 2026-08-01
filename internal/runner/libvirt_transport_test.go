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
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
	"golang.org/x/crypto/ssh"
)

type recordingLibvirtGuestSSH struct {
	mu            sync.Mutex
	commands      []string
	forgotten     []string
	identities    map[string]string
	run           func(context.Context, io.Reader, io.Writer, io.Writer, string) error
	authorizedKey string
	identityErr   error
}

func (s *recordingLibvirtGuestSSH) CreateIdentity(name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.identityErr != nil {
		return "", s.identityErr
	}
	if s.identities == nil {
		s.identities = make(map[string]string)
	}
	if _, exists := s.identities[name]; exists {
		return "", fmt.Errorf("identity already exists for %q", name)
	}
	key := s.authorizedKey
	if key == "" {
		key = "ssh-ed25519 AAAATEST gitone-test-" + name
	}
	s.identities[name] = key
	return key, nil
}

func (s *recordingLibvirtGuestSSH) Run(
	ctx context.Context,
	_ vmInstance,
	input io.Reader,
	output io.Writer,
	errorOutput io.Writer,
	command string,
) error {
	s.mu.Lock()
	s.commands = append(s.commands, command)
	run := s.run
	s.mu.Unlock()
	if run == nil {
		return nil
	}
	return run(ctx, input, output, errorOutput, command)
}

func (s *recordingLibvirtGuestSSH) ForgetVM(name string) {
	s.mu.Lock()
	delete(s.identities, name)
	s.forgotten = append(s.forgotten, name)
	s.mu.Unlock()
}

func (s *recordingLibvirtGuestSSH) hasIdentity(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.identities[name]
	return exists
}

func (s *recordingLibvirtGuestSSH) identityCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.identities)
}

func (s *recordingLibvirtGuestSSH) lastCommand() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.commands) == 0 {
		return ""
	}
	return s.commands[len(s.commands)-1]
}

func TestRunSSHDoesNotExposeRemoteCommand(t *testing.T) {
	const secret = "TOP_SECRET_BUILD_VALUE"
	guestSSH := &recordingLibvirtGuestSSH{
		run: func(_ context.Context, _ io.Reader, _ io.Writer, errorOutput io.Writer, _ string) error {
			_, _ = io.WriteString(errorOutput, "remote shell diagnostic")
			return errors.New("exit status 17")
		},
	}
	provider := &libvirtRPCProvider{
		config: LibvirtConfig{
			SSHUser:       "core",
			DockerCommand: "docker",
		},
		guestSSH: guestSSH,
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
	if guestSSH.lastCommand() != remoteCommand {
		t.Fatalf("native SSH command = %q, want %q", guestSSH.lastCommand(), remoteCommand)
	}
}

func TestRunSSHPreservesControlledTransportDiagnostic(t *testing.T) {
	guestSSH := &recordingLibvirtGuestSSH{
		run: func(context.Context, io.Reader, io.Writer, io.Writer, string) error {
			return &libvirtSSHTransportError{message: "VM SSH host key changed"}
		},
	}
	provider := &libvirtRPCProvider{guestSSH: guestSSH}
	instance := vmInstance{Name: "owned-warm-vm", Address: "192.0.2.20"}
	err := provider.runSSH(context.Background(), instance, nil, io.Discard, io.Discard, "secret")
	if err == nil || !strings.Contains(err.Error(), "VM SSH host key changed") {
		t.Fatalf("controlled SSH transport diagnostic = %v", err)
	}
}

func TestNativeLibvirtSSHGeneratesFreshInMemoryEd25519Identity(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	firstKey, err := transport.CreateIdentity("warm-vm-1")
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := transport.CreateIdentity("warm-vm-2")
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(firstKey))
	if err != nil {
		t.Fatalf("parse generated authorized key: %v", err)
	}
	if publicKey.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("generated key type = %q, want %q", publicKey.Type(), ssh.KeyAlgoED25519)
	}
	if firstKey == secondKey {
		t.Fatal("two VMs reused a runner SSH identity")
	}
	if _, err = transport.CreateIdentity("warm-vm-1"); err == nil {
		t.Fatal("duplicate VM SSH identity was accepted")
	}
	transport.ForgetVM("warm-vm-1")
	replacementKey, err := transport.CreateIdentity("warm-vm-1")
	if err != nil {
		t.Fatalf("recreate forgotten VM identity: %v", err)
	}
	if replacementKey == firstKey {
		t.Fatal("recreated VM identity reused its previous key")
	}
}

func createNativeSSHIdentity(
	t *testing.T,
	transport *nativeLibvirtSSH,
	instanceName string,
) ssh.PublicKey {
	t.Helper()
	authorizedKey, err := transport.CreateIdentity(instanceName)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, _, _, _, err := ssh.ParseAuthorizedKey([]byte(authorizedKey))
	if err != nil {
		t.Fatalf("parse generated authorized key: %v", err)
	}
	return publicKey
}

func TestNativeLibvirtSSHConcurrentIdentityCreationHasSingleWinner(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 32
	start := make(chan struct{})
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, createErr := transport.CreateIdentity("warm-vm-1")
			results <- createErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	winners := 0
	for createErr := range results {
		if createErr == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent VM identity winners = %d, want 1", winners)
	}
}

func TestNativeLibvirtSSHUsesOnlyTheAssignedVMIdentity(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	firstPublicKey := createNativeSSHIdentity(t, transport, "warm-vm-1")
	_ = createNativeSSHIdentity(t, transport, "warm-vm-2")
	server := newTestSSHServer(t, firstPublicKey, func(_ string, _ ssh.Channel) {})
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Address())
	if err != nil {
		t.Fatal(err)
	}
	transport.port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if err = transport.Run(
		context.Background(),
		vmInstance{Name: "warm-vm-2", Address: host},
		nil,
		io.Discard,
		io.Discard,
		"wrong-identity",
	); err == nil {
		t.Fatal("VM 2 authenticated to a server that accepts only VM 1's runner key")
	}
	if err = transport.Run(
		context.Background(),
		vmInstance{Name: "warm-vm-1", Address: host},
		nil,
		io.Discard,
		io.Discard,
		"right-identity",
	); err != nil {
		t.Fatalf("VM 1 did not use its assigned runner key: %v", err)
	}
}

func TestNativeLibvirtSSHPinsHostKeyInMemoryPerVM(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	firstHostKey := newTestSSHSigner(t).PublicKey()
	secondHostKey := newTestSSHSigner(t).PublicKey()
	callback := transport.hostKeyCallback("warm-vm-1")
	if err = callback("ignored", nil, firstHostKey); err != nil {
		t.Fatalf("pin first host key: %v", err)
	}
	if err = callback("ignored", nil, firstHostKey); err != nil {
		t.Fatalf("accept pinned host key: %v", err)
	}
	if err = callback("ignored", nil, secondHostKey); err == nil ||
		!strings.Contains(err.Error(), "host key changed") {
		t.Fatalf("changed host-key error = %v", err)
	}
	transport.ForgetVM("warm-vm-1")
	if err = callback("ignored", nil, secondHostKey); err != nil {
		t.Fatalf("accept host key after VM destruction: %v", err)
	}
}

func TestNativeLibvirtSSHConcurrentFirstHostKeyHasSingleWinner(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	hostKeys := []ssh.PublicKey{
		newTestSSHSigner(t).PublicKey(),
		newTestSSHSigner(t).PublicKey(),
	}
	start := make(chan struct{})
	results := make(chan error, len(hostKeys))
	for _, hostKey := range hostKeys {
		go func() {
			<-start
			results <- transport.hostKeyCallback("warm-vm-1")("ignored", nil, hostKey)
		}()
	}
	close(start)
	accepted := 0
	rejected := 0
	for range hostKeys {
		if err = <-results; err == nil {
			accepted++
		} else {
			rejected++
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatalf("concurrent first host keys: accepted %d, rejected %d", accepted, rejected)
	}
}

func TestNativeLibvirtSSHStreamsCommandWithoutOpenSSHProcess(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := createNativeSSHIdentity(t, transport, "warm-vm-1")
	server := newTestSSHServer(t, publicKey, func(command string, channel ssh.Channel) {
		contents, readErr := io.ReadAll(channel)
		if readErr != nil {
			return
		}
		_, _ = fmt.Fprintf(channel, "stdout:%s:%s", command, contents)
		_, _ = fmt.Fprint(channel.Stderr(), "stderr")
	})
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Address())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	transport.port = port
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err = transport.Run(
		context.Background(),
		vmInstance{Name: "warm-vm-1", Address: host},
		strings.NewReader("archive"),
		&stdout,
		&stderr,
		"consume-stream",
	)
	if err != nil {
		t.Fatal(err)
	}
	if stdout.String() != "stdout:consume-stream:archive" || stderr.String() != "stderr" {
		t.Fatalf("native SSH streams = stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestNativeLibvirtSSHSerializesStdoutAndStderr(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := createNativeSSHIdentity(t, transport, "warm-vm-1")
	server := newTestSSHServer(t, publicKey, func(_ string, channel ssh.Channel) {
		var writers sync.WaitGroup
		writers.Add(2)
		go func() {
			defer writers.Done()
			for range 64 {
				_, _ = channel.Write([]byte("stdout"))
			}
		}()
		go func() {
			defer writers.Done()
			for range 64 {
				_, _ = channel.Stderr().Write([]byte("stderr"))
			}
		}()
		writers.Wait()
	})
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Address())
	if err != nil {
		t.Fatal(err)
	}
	transport.port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	output := &concurrencyDetectingWriter{}
	err = transport.Run(
		context.Background(),
		vmInstance{Name: "warm-vm-1", Address: host},
		nil,
		output,
		output,
		"concurrent-output",
	)
	if err != nil {
		t.Fatal(err)
	}
	if output.concurrent.Load() {
		t.Fatal("stdout and stderr wrote to the build log concurrently")
	}
	if output.bytes.Load() == 0 {
		t.Fatal("native SSH did not forward command output")
	}
}

func TestNativeLibvirtSSHCancelsSilentHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	_ = createNativeSSHIdentity(t, transport, "warm-vm-1")
	host, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	transport.port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- transport.Run(
			ctx,
			vmInstance{Name: "warm-vm-1", Address: host},
			nil,
			io.Discard,
			io.Discard,
			"never-started",
		)
	}()
	connection := <-accepted
	cancel()
	defer func() { _ = connection.Close() }()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("silent-handshake cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH handshake did not stop after cancellation")
	}
}

func TestNativeLibvirtSSHCancelsHungCommand(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := createNativeSSHIdentity(t, transport, "warm-vm-1")
	started := make(chan struct{})
	release := make(chan struct{})
	server := newTestSSHServer(t, publicKey, func(_ string, _ ssh.Channel) {
		close(started)
		<-release
	})
	defer server.Close()
	defer close(release)
	host, portText, err := net.SplitHostPort(server.Address())
	if err != nil {
		t.Fatal(err)
	}
	transport.port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- transport.Run(
			ctx,
			vmInstance{Name: "warm-vm-1", Address: host},
			nil,
			io.Discard,
			io.Discard,
			"hang",
		)
	}()
	<-started
	cancel()
	select {
	case err = <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("hung-command cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH command did not stop after cancellation")
	}
}

func TestNativeLibvirtSSHClosesConnectionWhenKeepAliveIsUnanswered(t *testing.T) {
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := createNativeSSHIdentity(t, transport, "warm-vm-1")
	transport.keepAliveInterval = 10 * time.Millisecond
	transport.keepAliveCountMax = 2
	started := make(chan struct{})
	release := make(chan struct{})
	server := newTestSSHServer(t, publicKey, func(_ string, _ ssh.Channel) {
		close(started)
		<-release
	})
	server.ignoreGlobalRequests.Store(true)
	defer server.Close()
	defer close(release)
	host, portText, err := net.SplitHostPort(server.Address())
	if err != nil {
		t.Fatal(err)
	}
	transport.port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		result <- transport.Run(
			context.Background(),
			vmInstance{Name: "warm-vm-1", Address: host},
			nil,
			io.Discard,
			io.Discard,
			"wait-for-keepalive",
		)
	}()
	<-started
	select {
	case err = <-result:
		if err == nil {
			t.Fatal("unanswered SSH keepalive did not fail the command")
		}
	case <-time.After(time.Second):
		t.Fatal("unanswered SSH keepalive did not close the connection")
	}
}

func TestRunSSHSanitizesRealServerExitSignal(t *testing.T) {
	const secret = "TOP_SECRET_REMOTE_COMMAND"
	transport, err := newNativeLibvirtSSH("core", 22)
	if err != nil {
		t.Fatal(err)
	}
	publicKey := createNativeSSHIdentity(t, transport, "warm-vm-1")
	server := newTestSSHServer(t, publicKey, func(_ string, channel ssh.Channel) {
		_, _ = channel.SendRequest("exit-signal", false, ssh.Marshal(struct {
			SignalName   string
			CoreDumped   bool
			ErrorMessage string
			LanguageTag  string
		}{"TERM", false, secret, "en"}))
	})
	server.sendExitStatus.Store(false)
	defer server.Close()
	host, portText, err := net.SplitHostPort(server.Address())
	if err != nil {
		t.Fatal(err)
	}
	transport.port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	provider := &libvirtRPCProvider{guestSSH: transport}
	err = provider.runSSH(
		context.Background(),
		vmInstance{Name: "warm-vm-1", Address: host},
		nil,
		io.Discard,
		io.Discard,
		"docker run --env TOKEN="+secret,
	)
	if err == nil {
		t.Fatal("exit signal unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "docker run") {
		t.Fatalf("SSH error exposed remote content: %v", err)
	}
}

type concurrencyDetectingWriter struct {
	active     atomic.Int32
	concurrent atomic.Bool
	bytes      atomic.Int64
}

func (w *concurrencyDetectingWriter) Write(contents []byte) (int, error) {
	if w.active.Add(1) != 1 {
		w.concurrent.Store(true)
	}
	time.Sleep(100 * time.Microsecond)
	w.bytes.Add(int64(len(contents)))
	w.active.Add(-1)
	return len(contents), nil
}

func TestLibvirtTransportErrorHelpers(t *testing.T) {
	cause := errors.New("failed")
	commandErr := &libvirtGuestCommandError{Command: "ssh test-vm", Err: cause}
	if !errors.Is(commandErr, cause) || commandErr.Error() != "ssh test-vm: failed" {
		t.Fatalf("command error = %v", commandErr)
	}
	transportErr := &libvirtSSHTransportError{message: "safe diagnostic"}
	if transportErr.Error() != "safe diagnostic" {
		t.Fatalf("transport error = %q", transportErr.Error())
	}
}

type timeoutTestError struct{}

func (timeoutTestError) Error() string { return "timeout" }
func (timeoutTestError) Timeout() bool { return true }

func TestSafeLibvirtSSHDialErrors(t *testing.T) {
	for _, test := range []struct {
		err  error
		want string
	}{
		{err: syscall.ECONNREFUSED, want: "VM SSH connection refused"},
		{err: syscall.ENETUNREACH, want: "VM SSH endpoint is unreachable"},
		{err: syscall.EHOSTUNREACH, want: "VM SSH endpoint is unreachable"},
		{err: timeoutTestError{}, want: "VM SSH connection timed out"},
		{err: errors.New("other"), want: "VM SSH connection failed"},
	} {
		if got := safeLibvirtSSHDialError(test.err); got != test.want {
			t.Errorf("safe dial error for %v = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestLibvirtProviderExecuteLifecycle(t *testing.T) {
	newRequest := func(directory string) ExecuteRequest {
		return ExecuteRequest{
			Job: Job{
				ID:         "build-7",
				Name:       "test",
				Repository: "group/repository",
				Branch:     "main",
				Commit:     strings.Repeat("7", 40),
			},
			Directory: directory,
			Config: repoconfig.JobConfig{
				Image:  "alpine:latest",
				Script: []string{"go test ./..."},
			},
		}
	}
	instance := vmInstance{
		Name:    "gitone-test-20260801120000-abcdef",
		Address: "192.0.2.20",
	}

	t.Run("success", func(t *testing.T) {
		var output bytes.Buffer
		guest := &recordingLibvirtGuestSSH{
			run: func(_ context.Context, input io.Reader, output io.Writer, _ io.Writer, command string) error {
				if input != nil {
					if _, err := io.Copy(io.Discard, input); err != nil {
						return err
					}
				}
				if strings.Contains(command, "'docker' 'run'") {
					_, _ = io.WriteString(output, "build output")
				}
				return nil
			},
		}
		provider := &libvirtRPCProvider{
			config:      LibvirtConfig{DockerCommand: "docker", CleanupTimeout: time.Second},
			ownerPrefix: "gitone-test",
			guestSSH:    guest,
		}
		err := provider.Execute(context.Background(), instance, newRequest(t.TempDir()), &output)
		if err != nil {
			t.Fatal(err)
		}
		if output.String() != "build output" || len(guest.commands) != 3 {
			t.Fatalf("execute output = %q, commands = %#v", output.String(), guest.commands)
		}
	})

	t.Run("upload failure is cleaned up", func(t *testing.T) {
		calls := 0
		guest := &recordingLibvirtGuestSSH{
			run: func(_ context.Context, input io.Reader, _ io.Writer, _ io.Writer, _ string) error {
				calls++
				if input != nil {
					_, _ = io.Copy(io.Discard, input)
					return errors.New("upload rejected")
				}
				return nil
			},
		}
		provider := &libvirtRPCProvider{
			config:      LibvirtConfig{DockerCommand: "docker", CleanupTimeout: time.Second},
			ownerPrefix: "gitone-test",
			guestSSH:    guest,
		}
		err := provider.Execute(context.Background(), instance, newRequest(t.TempDir()), nil)
		if err == nil || !strings.Contains(err.Error(), "upload build context") || calls != 2 {
			t.Fatalf("upload failure = %v, calls = %d", err, calls)
		}
	})

	t.Run("validates inputs", func(t *testing.T) {
		provider := &libvirtRPCProvider{ownerPrefix: "gitone-test"}
		request := newRequest(t.TempDir())
		if err := provider.Execute(context.Background(), vmInstance{Name: "unmanaged"}, request, nil); err == nil {
			t.Fatal("unmanaged instance was accepted")
		}
		request.Job.ID = "../invalid"
		if err := provider.Execute(context.Background(), instance, request, nil); err == nil {
			t.Fatal("invalid build ID was accepted")
		}
		request = newRequest(t.TempDir())
		request.Config.Image = ""
		if err := provider.Execute(context.Background(), instance, request, nil); err == nil {
			t.Fatal("invalid build config was accepted")
		}
		request = newRequest(filepath.Join(t.TempDir(), "missing"))
		if err := provider.Execute(context.Background(), instance, request, nil); err == nil {
			t.Fatal("missing build directory was accepted")
		}
		file := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(file, []byte("not a directory"), 0o640); err != nil {
			t.Fatal(err)
		}
		request = newRequest(file)
		if err := provider.Execute(context.Background(), instance, request, nil); err == nil {
			t.Fatal("regular build file was accepted")
		}
	})
}

func TestDockerRunArgumentsMatchContainerIsolationContract(t *testing.T) {
	provider := &libvirtRPCProvider{config: LibvirtConfig{DockerCommand: "/usr/bin/docker"}}
	request := ExecuteRequest{
		Job: Job{
			ID:         "build-1",
			Name:       "test",
			Repository: "group/project",
			Branch:     "main",
			Commit:     "0123456789abcdef",
		},
		Config: repoconfig.JobConfig{
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
		!containsString(environment, "CI_PROJECT_PATH=group/project") ||
		!containsString(environment, "CI_JOB_NAME=test") ||
		!containsString(environment, "GITONE_JOB_NAME=test") {
		t.Fatalf("Docker environment = %#v", environment)
	}
	wantSuffix := []string{
		"alpine:3.22",
		"-ec",
		renderBuildScript([]string{"printf '%s\\n' ready", "make test"}),
	}
	if !reflect.DeepEqual(arguments[len(arguments)-len(wantSuffix):], wantSuffix) {
		t.Fatalf("Docker argument suffix = %#v", arguments[len(arguments)-len(wantSuffix):])
	}
}

func TestUploadMakesWorkspaceUsableByNonRootImageUser(t *testing.T) {
	guestSSH := &recordingLibvirtGuestSSH{
		run: func(_ context.Context, input io.Reader, _ io.Writer, _ io.Writer, _ string) error {
			_, err := io.Copy(io.Discard, input)
			return err
		},
	}
	provider := &libvirtRPCProvider{
		config:   LibvirtConfig{SSHUser: "core"},
		guestSSH: guestSSH,
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
	remoteCommand := guestSSH.lastCommand()
	if !strings.Contains(remoteCommand, "chmod -R a+rwX -- '/var/tmp/gitone-build-1'") {
		t.Fatalf("upload did not make the workspace usable by an arbitrary image user: %q", remoteCommand)
	}
}

type testSSHServer struct {
	listener             net.Listener
	config               *ssh.ServerConfig
	handler              func(string, ssh.Channel)
	ignoreGlobalRequests atomic.Bool
	ignoredRequests      atomic.Int64
	sendExitStatus       atomic.Bool
	wait                 sync.WaitGroup
}

func newTestSSHServer(
	t *testing.T,
	authorizedKey ssh.PublicKey,
	handler func(string, ssh.Channel),
) *testSSHServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &testSSHServer{
		listener: listener,
		handler:  handler,
		config: &ssh.ServerConfig{
			PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
				if !bytes.Equal(key.Marshal(), authorizedKey.Marshal()) {
					return nil, errors.New("unauthorized test key")
				}
				return nil, nil
			},
		},
	}
	server.sendExitStatus.Store(true)
	server.config.AddHostKey(newTestSSHSigner(t))
	server.wait.Add(1)
	go server.serve()
	return server
}

func newTestSSHSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func (s *testSSHServer) Address() string {
	return s.listener.Addr().String()
}

func (s *testSSHServer) Close() {
	_ = s.listener.Close()
	s.wait.Wait()
}

func (s *testSSHServer) serve() {
	defer s.wait.Done()
	for {
		connection, err := s.listener.Accept()
		if err != nil {
			return
		}
		s.wait.Add(1)
		go func() {
			defer s.wait.Done()
			s.serveConnection(connection)
		}()
	}
}

func (s *testSSHServer) serveConnection(connection net.Conn) {
	serverConnection, channels, requests, err := ssh.NewServerConn(connection, s.config)
	if err != nil {
		_ = connection.Close()
		return
	}
	defer func() { _ = serverConnection.Close() }()
	if s.ignoreGlobalRequests.Load() {
		go func() {
			for range requests {
				s.ignoredRequests.Add(1)
			}
		}()
	} else {
		go ssh.DiscardRequests(requests)
	}
	for request := range channels {
		if request.ChannelType() != "session" {
			_ = request.Reject(ssh.UnknownChannelType, "session channel required")
			continue
		}
		channel, channelRequests, err := request.Accept()
		if err != nil {
			continue
		}
		for channelRequest := range channelRequests {
			if channelRequest.Type != "exec" {
				_ = channelRequest.Reply(false, nil)
				continue
			}
			var payload struct{ Command string }
			if err = ssh.Unmarshal(channelRequest.Payload, &payload); err != nil {
				_ = channelRequest.Reply(false, nil)
				continue
			}
			_ = channelRequest.Reply(true, nil)
			if s.handler != nil {
				s.handler(payload.Command, channel)
			}
			if s.sendExitStatus.Load() {
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			}
			_ = channel.Close()
			break
		}
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
