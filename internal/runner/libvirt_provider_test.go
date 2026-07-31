package runner

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type prepareVirshRunner struct {
	poolPath            string
	networkName         string
	kvm                 bool
	poolActive          bool
	concurrentPoolStart bool
	networkDefined      bool
	networkActive       bool
	networkWrong        bool
	concurrentStart     bool
	autostarted         bool
	poolTarget          string
	poolType            string
	verbs               []string
}

func (r *prepareVirshRunner) LookPath(command string) (string, error) {
	return "/mock/" + command, nil
}

func (r *prepareVirshRunner) Run(
	_ context.Context,
	_ string,
	arguments []string,
	_ io.Reader,
	output io.Writer,
	_ io.Writer,
) error {
	if len(arguments) < 3 || arguments[0] != "--connect" {
		return fmt.Errorf("unexpected virsh arguments %#v", arguments)
	}
	verb := arguments[2]
	r.verbs = append(r.verbs, verb)
	switch verb {
	case "domcapabilities":
		domainType := "qemu"
		if r.kvm {
			domainType = "kvm"
		}
		_, _ = fmt.Fprintf(output, "<domainCapabilities><domain>%s</domain></domainCapabilities>\n", domainType)
	case "pool-info":
		state := "inactive"
		if r.poolActive {
			state = "running"
		}
		_, _ = fmt.Fprintf(output, "Name: test-pool\nState: %s\n", state)
	case "pool-start":
		if r.concurrentPoolStart {
			r.poolActive = true
			return errors.New("storage pool is already active")
		}
		r.poolActive = true
	case "pool-dumpxml":
		target := r.poolTarget
		if target == "" {
			target = r.poolPath
		}
		poolType := r.poolType
		if poolType == "" {
			poolType = "dir"
		}
		_, _ = fmt.Fprintf(output, "<pool type='%s'><name>test-pool</name><target><path>%s</path></target></pool>\n", poolType, target)
	case "pool-refresh":
		// The verified base image is installed directly in this directory pool.
	case "vol-dumpxml":
		_, _ = io.WriteString(output, "<volume><name>flatcar-base.qcow2</name><capacity unit='bytes'>1073741824</capacity><target><format type='qcow2'/></target></volume>\n")
	case "vol-path":
		_, _ = fmt.Fprintln(output, filepath.Join(r.poolPath, "flatcar-base.qcow2"))
	case "list":
		// There are no stale domains in the Prepare fixture.
	case "vol-list":
		_, _ = io.WriteString(output, " Name   Path\n----------------\n")
	case "net-list":
		if r.networkDefined {
			_, _ = fmt.Fprintln(output, r.networkName)
		}
	case "net-define":
		r.networkDefined = true
	case "net-dumpxml":
		if r.networkWrong {
			_, _ = fmt.Fprintf(output, "<network><name>%s</name><forward mode='bridge'/></network>\n", r.networkName)
			break
		}
		contents, err := renderLibvirtNetworkXML(r.networkName)
		if err != nil {
			return err
		}
		_, _ = output.Write(contents)
	case "net-info":
		active := "no"
		if r.networkActive {
			active = "yes"
		}
		_, _ = fmt.Fprintf(output, "Name: %s\nActive: %s\n", r.networkName, active)
	case "net-start":
		if r.concurrentStart {
			r.networkActive = true
			return errors.New("network is already active")
		}
		r.networkActive = true
	case "net-autostart":
		r.autostarted = true
	default:
		return fmt.Errorf("unexpected Prepare virsh verb %q", verb)
	}
	return nil
}

type lifecycleVirshRunner struct {
	poolPath   string
	failVerb   string
	failError  error
	failStderr string
	failDelete bool

	domainName        string
	domainExists      bool
	domainRunning     bool
	volumeExists      bool
	domainDescription string
	domainDiskPath    string
	verbs             []string
	calls             [][]string
}

type readinessCommandRunner struct {
	state string
}

type readinessGuestSSH struct {
	err   error
	calls int
}

func (*readinessGuestSSH) AuthorizedKey() string {
	return "ssh-ed25519 AAAATEST readiness@test"
}

func (s *readinessGuestSSH) Run(
	_ context.Context,
	_ vmInstance,
	_ io.Reader,
	_ io.Writer,
	_ io.Writer,
	_ string,
) error {
	s.calls++
	return s.err
}

func (*readinessGuestSSH) ForgetHost(string) {}

func (r *readinessCommandRunner) LookPath(command string) (string, error) {
	return command, nil
}

func (r *readinessCommandRunner) Run(
	_ context.Context,
	command string,
	arguments []string,
	_ io.Reader,
	output io.Writer,
	_ io.Writer,
) error {
	if command == "virsh" {
		if len(arguments) < 3 || arguments[2] != "domstate" {
			return fmt.Errorf("unexpected readiness virsh arguments: %#v", arguments)
		}
		_, _ = fmt.Fprintln(output, r.state)
		return nil
	}
	return fmt.Errorf("unexpected readiness command %q", command)
}

func (r *lifecycleVirshRunner) LookPath(command string) (string, error) {
	return command, nil
}

func (r *lifecycleVirshRunner) Run(
	_ context.Context,
	command string,
	arguments []string,
	_ io.Reader,
	output io.Writer,
	errorOutput io.Writer,
) error {
	if command != "virsh" || len(arguments) < 3 || arguments[0] != "--connect" {
		return fmt.Errorf("unexpected command %q %#v", command, arguments)
	}
	verb := arguments[2]
	verbArguments := arguments[3:]
	r.verbs = append(r.verbs, verb)
	r.calls = append(r.calls, append([]string(nil), arguments...))
	if verb == r.failVerb {
		if r.failStderr != "" {
			_, _ = io.WriteString(errorOutput, r.failStderr)
		}
		if r.failError != nil {
			return r.failError
		}
		return fmt.Errorf("injected %s failure", verb)
	}
	switch verb {
	case "dominfo":
		if !r.domainExists {
			return errors.New("no domain with matching name")
		}
	case "dumpxml":
		description := r.domainDescription
		if description == "" {
			const instanceTailLength = len("-20060102150405-abcdef")
			if len(r.domainName) <= instanceTailLength {
				return fmt.Errorf("invalid fixture domain name %q", r.domainName)
			}
			description = "managed-by:gitone:" + r.domainName[:len(r.domainName)-instanceTailLength]
		}
		diskPath := r.domainDiskPath
		if diskPath == "" {
			diskPath = filepath.Join(r.poolPath, r.domainName+".qcow2")
		}
		_, _ = fmt.Fprintf(
			output,
			"<domain><description>%s</description><devices><disk device='disk'><source file='%s'/></disk></devices></domain>\n",
			description,
			diskPath,
		)
	case "vol-info":
		if !r.volumeExists {
			return errors.New("no storage vol with matching name")
		}
	case "list":
		if r.domainExists {
			_, _ = fmt.Fprintln(output, r.domainName)
		}
	case "domiflist":
		// An identity allocation only asks about domains returned by list.
	case "vol-list":
		_, _ = io.WriteString(output, " Name   Path\n----------------\n")
		if r.volumeExists {
			_, _ = fmt.Fprintf(
				output,
				" %s.qcow2   %s\n",
				r.domainName,
				filepath.Join(r.poolPath, r.domainName+".qcow2"),
			)
		}
	case "vol-create-as":
		volumeName := virshOptionValue(verbArguments, "--name")
		r.domainName = strings.TrimSuffix(volumeName, ".qcow2")
		r.volumeExists = true
		if err := os.WriteFile(filepath.Join(r.poolPath, volumeName), []byte("qcow2"), 0o600); err != nil {
			return err
		}
	case "vol-path":
		_, _ = fmt.Fprintln(output, filepath.Join(r.poolPath, verbArguments[0]))
	case "define":
		r.domainExists = true
	case "start":
		r.domainExists = true
		r.domainRunning = true
	case "domstate":
		if r.domainRunning {
			_, _ = io.WriteString(output, "running\n")
		} else {
			_, _ = io.WriteString(output, "shut off\n")
		}
	case "destroy":
		r.domainRunning = false
	case "undefine":
		r.domainExists = false
		r.domainRunning = false
	case "vol-delete":
		if r.failDelete {
			return errors.New("injected volume deletion failure")
		}
		r.volumeExists = false
		if err := os.Remove(filepath.Join(r.poolPath, verbArguments[0])); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	case "domifaddr", "net-dhcp-leases":
		// No lease forces the bounded readiness failure used by the test.
	default:
		return fmt.Errorf("unexpected virsh verb %q", verb)
	}
	return nil
}

func virshOptionValue(arguments []string, option string) string {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == option {
			return arguments[index+1]
		}
	}
	return ""
}

func newPrepareTestProvider(t *testing.T) (*virshVMProvider, *prepareVirshRunner) {
	t.Helper()
	poolPath := t.TempDir()
	if err := os.Chmod(poolPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(poolPath, "flatcar-base.qcow2"), []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	config := LibvirtConfig{
		RunnerID:        "prepare-test",
		URI:             "test:///system",
		PoolName:        "test-pool",
		PoolPath:        poolPath,
		BaseVolumeName:  "flatcar-base.qcow2",
		BaseImageSHA512: flatcarTestSHA512([]byte("base")),
		NetworkName:     "gitone-prepare-test",
		SSHUser:         "core",
		VirshCommand:    "virsh",
		DockerCommand:   "docker",
		SSHPort:         22,
		VCPUs:           1,
		MemoryMiB:       1024,
		DiskSizeGiB:     2,
		ReadyTimeout:    time.Second,
		CleanupTimeout:  time.Second,
	}
	provider, ok := newVirshVMProvider(config).(*virshVMProvider)
	if !ok {
		t.Fatal("production provider has an unexpected type")
	}
	runner := &prepareVirshRunner{
		poolPath:    poolPath,
		networkName: config.NetworkName,
		kvm:         true,
	}
	provider.runner = runner
	return provider, runner
}

func TestPrepareValidatesStorageAndCreatesPersistentNetwork(t *testing.T) {
	provider, runner := newPrepareTestProvider(t)
	if err := provider.Prepare(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !provider.prepared || provider.lockFile == nil || !runner.poolActive ||
		!runner.networkDefined || !runner.networkActive || !runner.autostarted {
		t.Fatalf("prepared provider = %#v, runner = %#v", provider, runner)
	}
	if !strings.HasPrefix(provider.publicKey, "ssh-ed25519 ") {
		t.Fatalf("derived SSH public key = %q", provider.publicKey)
	}
	for _, verb := range []string{
		"domcapabilities", "pool-info", "pool-start", "pool-dumpxml", "pool-refresh", "vol-dumpxml",
		"vol-path", "net-define", "net-dumpxml", "net-start", "net-autostart",
	} {
		if !containsString(runner.verbs, verb) {
			t.Fatalf("Prepare did not call %q: %#v", verb, runner.verbs)
		}
	}
	cleanupIndex, refreshIndex := -1, -1
	for index, verb := range runner.verbs {
		if verb == "vol-list" && cleanupIndex == -1 {
			cleanupIndex = index
		}
		if verb == "pool-refresh" && refreshIndex == -1 {
			refreshIndex = index
		}
	}
	if cleanupIndex == -1 || refreshIndex == -1 || cleanupIndex >= refreshIndex {
		t.Fatalf("stale resource cleanup must precede base-image provisioning: %#v", runner.verbs)
	}
	if err := provider.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.prepared || provider.lockFile != nil {
		t.Fatalf("Cleanup left provider active: %#v", provider)
	}
	if !runner.networkDefined || !runner.networkActive || !runner.autostarted {
		t.Fatal("Cleanup removed or disabled the persistent shared network")
	}
	callCount := len(runner.verbs)
	if err := provider.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.verbs) != callCount {
		t.Fatalf("idempotent Cleanup made extra libvirt calls: %#v", runner.verbs[callCount:])
	}
}

func TestPrepareAcceptsConcurrentNetworkStartFromAnotherPool(t *testing.T) {
	provider, runner := newPrepareTestProvider(t)
	runner.concurrentStart = true

	if err := provider.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare rejected a concurrently started network: %v", err)
	}
	if !runner.networkActive || !runner.autostarted {
		t.Fatalf("network state after concurrent start = %#v", runner)
	}
	netInfoCalls := 0
	for _, verb := range runner.verbs {
		if verb == "net-info" {
			netInfoCalls++
		}
	}
	if netInfoCalls != 2 {
		t.Fatalf("Prepare did not verify the failed network start: %#v", runner.verbs)
	}
	if err := provider.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareAcceptsConcurrentStoragePoolStartFromAnotherRunner(t *testing.T) {
	provider, runner := newPrepareTestProvider(t)
	runner.concurrentPoolStart = true

	if err := provider.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare rejected a concurrently started storage pool: %v", err)
	}
	poolInfoCalls := 0
	for _, verb := range runner.verbs {
		if verb == "pool-info" {
			poolInfoCalls++
		}
	}
	if poolInfoCalls != 2 {
		t.Fatalf("Prepare did not verify the failed storage-pool start: %#v", runner.verbs)
	}
	if err := provider.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareFailureReleasesOwnershipWithoutCleanupSideEffects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*prepareVirshRunner)
		want   string
	}{
		{
			name: "KVM unavailable",
			mutate: func(runner *prepareVirshRunner) {
				runner.kvm = false
			},
			want: "KVM",
		},
		{
			name: "wrong storage type",
			mutate: func(runner *prepareVirshRunner) {
				runner.poolType = "logical"
			},
			want: "directory pool",
		},
		{
			name: "wrong storage target",
			mutate: func(runner *prepareVirshRunner) {
				runner.poolTarget = filepath.Join(runner.poolPath, "other")
			},
			want: "targets",
		},
		{
			name: "wrong existing network",
			mutate: func(runner *prepareVirshRunner) {
				runner.networkDefined = true
				runner.networkWrong = true
			},
			want: "dedicated NAT network",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, runner := newPrepareTestProvider(t)
			test.mutate(runner)
			err := provider.Prepare(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Prepare error = %v, want containing %q", err, test.want)
			}
			if provider.prepared || provider.lockFile != nil {
				t.Fatalf("failed Prepare retained provider ownership: %#v", provider)
			}
			callCount := len(runner.verbs)
			if err = provider.Cleanup(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(runner.verbs) != callCount {
				t.Fatalf("Cleanup after failed Prepare touched libvirt: %#v", runner.verbs[callCount:])
			}
		})
	}
}

func flatcarTestSHA512(contents []byte) string {
	digest := sha512.Sum512(contents)
	return hex.EncodeToString(digest[:])
}

func newLifecycleTestProvider(poolPath string, runner libvirtCommandRunner) *virshVMProvider {
	runnerID := "rollback-test"
	return &virshVMProvider{
		config: LibvirtConfig{
			RunnerID:       runnerID,
			URI:            "test:///system",
			PoolName:       "test-pool",
			PoolPath:       poolPath,
			BaseVolumeName: "flatcar-base.qcow2",
			NetworkName:    "gitone-test",
			SSHUser:        "core",
			VirshCommand:   "virsh",
			DockerCommand:  "docker",
			SSHPort:        22,
			VCPUs:          1,
			MemoryMiB:      1024,
			DiskSizeGiB:    2,
			ReadyTimeout:   20 * time.Millisecond,
			CleanupTimeout: time.Second,
		},
		runner:      runner,
		ownerPrefix: libvirtOwnerPrefix("test:///system", "test-pool", runnerID),
		publicKey:   "ssh-ed25519 AAAATEST rollback@test",
		guestSSH:    &recordingLibvirtGuestSSH{},
		prepared:    true,
	}
}

func TestCreateRollsBackEveryProvisioningStage(t *testing.T) {
	tests := []struct {
		name     string
		failVerb string
		wantText string
	}{
		{name: "overlay creation", failVerb: "vol-create-as", wantText: "create VM qcow2 overlay"},
		{name: "overlay resolution", failVerb: "vol-path", wantText: "resolve VM volume path"},
		{name: "domain definition", failVerb: "define", wantText: "define KVM domain"},
		{name: "domain start", failVerb: "start", wantText: "start KVM domain"},
		{name: "guest readiness", wantText: "wait for KVM guest readiness"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			poolPath := t.TempDir()
			runner := &lifecycleVirshRunner{poolPath: poolPath, failVerb: test.failVerb}
			provider := newLifecycleTestProvider(poolPath, runner)
			instance, err := provider.Create(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("Create error = %v, want containing %q", err, test.wantText)
			}
			if instance.Name != "" {
				t.Fatalf("Create returned an already-cleaned partial instance: %#v", instance)
			}
			if runner.domainExists || runner.volumeExists {
				t.Fatalf("rollback left libvirt resources: %#v", runner)
			}
			if _, statErr := os.Stat(instance.IgnitionPath); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("rollback left Ignition %q: %v", instance.IgnitionPath, statErr)
			}
			if !containsString(runner.verbs, "dominfo") || !containsString(runner.verbs, "vol-info") {
				t.Fatalf("rollback verbs = %#v", runner.verbs)
			}
		})
	}
}

func TestCreateReturnsPartialInstanceWhenRollbackMustBeRetried(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleVirshRunner{
		poolPath:   poolPath,
		failVerb:   "vol-path",
		failDelete: true,
	}
	provider := newLifecycleTestProvider(poolPath, runner)
	instance, err := provider.Create(context.Background())
	if err == nil || !strings.Contains(err.Error(), "injected volume deletion failure") {
		t.Fatalf("Create error = %v", err)
	}
	if instance.Name == "" || !runner.volumeExists {
		t.Fatalf("partial instance/resource was not retained: %#v, runner=%#v", instance, runner)
	}
	runner.failDelete = false
	if err = provider.Destroy(context.Background(), instance); err != nil {
		t.Fatalf("retry Destroy: %v", err)
	}
	if runner.volumeExists {
		t.Fatal("retry Destroy left the volume behind")
	}
}

func TestProviderCheckReadyRequiresRunningDomainSSHAndDocker(t *testing.T) {
	poolPath := t.TempDir()
	runner := &readinessCommandRunner{state: "running"}
	guestSSH := &readinessGuestSSH{}
	provider := &virshVMProvider{
		config: LibvirtConfig{
			URI:           "test:///system",
			PoolPath:      poolPath,
			VirshCommand:  "virsh",
			SSHUser:       "core",
			SSHPort:       22,
			DockerCommand: "docker",
		},
		runner:      runner,
		ownerPrefix: libvirtOwnerPrefix("test:///system", "test-pool", "readiness-test"),
		guestSSH:    guestSSH,
	}
	name := provider.ownerPrefix + "-20260731120000-abcdef"
	instance := vmInstance{Name: name, Address: "192.0.2.10"}
	if err := provider.CheckReady(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
	if guestSSH.calls != 1 {
		t.Fatalf("readiness SSH probes = %d, want 1", guestSSH.calls)
	}
	runner.state = "shut off"
	if err := provider.CheckReady(context.Background(), instance); err == nil ||
		!strings.Contains(err.Error(), "not running") {
		t.Fatalf("stopped domain readiness error = %v", err)
	}
	if guestSSH.calls != 1 {
		t.Fatal("readiness check attempted SSH for a stopped domain")
	}
	runner.state = "running"
	guestSSH.err = errors.New("Docker unavailable")
	if err := provider.CheckReady(context.Background(), instance); err == nil ||
		!strings.Contains(err.Error(), "SSH and Docker") {
		t.Fatalf("guest health error = %v", err)
	}
}

func TestDestroyIsStrictlyOwnedAndIdempotent(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleVirshRunner{poolPath: poolPath}
	provider := newLifecycleTestProvider(poolPath, runner)
	if err := provider.Destroy(context.Background(), vmInstance{Name: "someone-elses-vm"}); err == nil {
		t.Fatal("Destroy accepted an unmanaged VM")
	}
	if len(runner.verbs) != 0 {
		t.Fatalf("unmanaged Destroy touched libvirt: %#v", runner.verbs)
	}
	name := provider.ownerPrefix + "-20260731120000-abcdef"
	instance := vmInstance{
		Name:         name,
		Address:      "192.0.2.50",
		MACAddress:   "52:54:00:01:02:03",
		VolumeName:   name + ".qcow2",
		IgnitionPath: filepath.Join(poolPath, name+".ign"),
	}
	if !provider.reserveInstanceIdentity(instance) {
		t.Fatal("could not reserve test VM identity")
	}
	runner.domainName = name
	runner.domainExists = true
	runner.domainRunning = true
	runner.volumeExists = true
	if err := os.WriteFile(filepath.Join(poolPath, instance.VolumeName), []byte("qcow2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(instance.IgnitionPath, []byte("ignition"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.Destroy(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
	if runner.domainExists || runner.domainRunning || runner.volumeExists {
		t.Fatalf("Destroy left libvirt state: %#v", runner)
	}
	for _, verb := range []string{"domstate", "destroy", "undefine", "vol-delete"} {
		if !containsString(runner.verbs, verb) {
			t.Fatalf("Destroy did not call %q: %#v", verb, runner.verbs)
		}
	}
	if _, err := os.Stat(instance.IgnitionPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Destroy left Ignition: %v", err)
	}
	guestSSH, ok := provider.guestSSH.(*recordingLibvirtGuestSSH)
	if !ok || !containsString(guestSSH.forgotten, instance.Name) {
		t.Fatalf("Destroy did not forget the in-memory SSH host key: %#v", provider.guestSSH)
	}
	if !provider.reserveInstanceIdentity(instance) {
		t.Fatal("successful Destroy did not release the VM identity")
	}
	provider.releaseInstanceIdentity(instance)
	removeLogs := false
	for _, call := range runner.calls {
		if len(call) > 3 && call[2] == "destroy" && containsString(call[3:], "--remove-logs") {
			removeLogs = true
			break
		}
	}
	if !removeLogs {
		t.Fatalf("Destroy did not request QEMU log cleanup: %#v", runner.calls)
	}
	if err := provider.Destroy(context.Background(), instance); err != nil {
		t.Fatalf("second Destroy: %v", err)
	}
	callCount := len(runner.verbs)
	badInstance := instance
	badInstance.VolumeName = "unrelated.qcow2"
	if err := provider.Destroy(context.Background(), badInstance); err == nil {
		t.Fatal("Destroy accepted a mismatched volume")
	}
	if len(runner.verbs) != callCount {
		t.Fatal("Destroy touched libvirt for a mismatched volume")
	}
}

func TestConcurrentInstanceIdentityReservationsAreExclusive(t *testing.T) {
	provider := &virshVMProvider{}
	instance := vmInstance{
		Name:       "gitone-test-20260731120000-abcdef",
		MACAddress: "52:54:00:ab:cd:ef",
	}
	const contenders = 32
	start := make(chan struct{})
	results := make(chan bool, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- provider.reserveInstanceIdentity(instance)
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	winners := 0
	for reserved := range results {
		if reserved {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent identity reservation winners = %d, want 1", winners)
	}

	sameMAC := instance
	sameMAC.Name = "gitone-test-20260731120001-123456"
	if provider.reserveInstanceIdentity(sameMAC) {
		t.Fatal("allocated MAC address was reserved under a second name")
	}
	provider.releaseInstanceIdentity(instance)
	if !provider.reserveInstanceIdentity(sameMAC) {
		t.Fatal("released MAC address could not be reserved again")
	}
}

func TestDestroyRetainsArtifactsUntilDomainIsGone(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleVirshRunner{
		poolPath:      poolPath,
		failVerb:      "destroy",
		domainExists:  true,
		domainRunning: true,
		volumeExists:  true,
	}
	provider := newLifecycleTestProvider(poolPath, runner)
	name := provider.ownerPrefix + "-20260731120000-abcdef"
	instance := vmInstance{
		Name:         name,
		MACAddress:   "52:54:00:01:02:04",
		VolumeName:   name + ".qcow2",
		IgnitionPath: filepath.Join(poolPath, name+".ign"),
	}
	if !provider.reserveInstanceIdentity(instance) {
		t.Fatal("could not reserve test VM identity")
	}
	runner.domainName = name
	if err := os.WriteFile(instance.IgnitionPath, []byte("ignition"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := provider.Destroy(context.Background(), instance); err == nil {
		t.Fatal("Destroy succeeded while the domain could not be stopped")
	}
	if !runner.volumeExists {
		t.Fatal("Destroy deleted an overlay still attached to a domain")
	}
	if _, err := os.Stat(instance.IgnitionPath); err != nil {
		t.Fatalf("Destroy deleted Ignition while the domain remained: %v", err)
	}
	if containsString(runner.verbs, "vol-delete") {
		t.Fatalf("Destroy attempted artifact deletion before domain removal: %#v", runner.verbs)
	}
	if provider.reserveInstanceIdentity(instance) {
		t.Fatal("failed Destroy released an identity whose resources remain")
	}
	provider.releaseInstanceIdentity(instance)
}

func TestDestroyDoesNotTreatGenericDomainLookupFailureAsAbsence(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleVirshRunner{
		poolPath:      poolPath,
		failVerb:      "dominfo",
		failError:     errors.New("failed to get domain 'vm': internal error: client socket is closed"),
		domainExists:  true,
		domainRunning: true,
		volumeExists:  true,
	}
	provider := newLifecycleTestProvider(poolPath, runner)
	name := provider.ownerPrefix + "-20260731120000-abcdef"
	instance := vmInstance{
		Name:         name,
		VolumeName:   name + ".qcow2",
		IgnitionPath: filepath.Join(poolPath, name+".ign"),
	}
	runner.domainName = name
	if err := os.WriteFile(instance.IgnitionPath, []byte("ignition"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := provider.Destroy(context.Background(), instance)
	if err == nil || !strings.Contains(err.Error(), "client socket is closed") {
		t.Fatalf("Destroy lookup error = %v", err)
	}
	if !runner.domainExists || !runner.volumeExists {
		t.Fatalf("Destroy changed resources after an ambiguous lookup failure: %#v", runner)
	}
	if containsString(runner.verbs, "vol-delete") {
		t.Fatalf("Destroy deleted a live volume after an ambiguous lookup failure: %#v", runner.verbs)
	}
	if _, err = os.Stat(instance.IgnitionPath); err != nil {
		t.Fatalf("Destroy deleted Ignition after an ambiguous lookup failure: %v", err)
	}
}

func TestLibvirtResourceAbsentRecognizesBareFailedDomainLookup(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "virsh stderr",
			err: &libvirtCommandError{
				Command: "virsh dominfo missing-vm",
				Err:     errors.New("exit status 1"),
				Stderr:  "error: failed to get domain 'missing-vm'",
			},
			want: true,
		},
		{
			name: "bare message without prefix",
			err:  errors.New("failed to get domain 'missing-vm'"),
			want: true,
		},
		{
			name: "transport failure suffix",
			err: &libvirtCommandError{
				Command: "virsh dominfo missing-vm",
				Err:     errors.New("exit status 1"),
				Stderr: "error: failed to get domain 'missing-vm': " +
					"internal error: client socket is closed",
			},
			want: false,
		},
		{
			name: "multiline follow-on failure",
			err: &libvirtCommandError{
				Command: "virsh dominfo missing-vm",
				Err:     errors.New("exit status 1"),
				Stderr: "error: failed to get domain 'missing-vm'\n" +
					"error: internal error: client socket is closed",
			},
			want: false,
		},
		{
			name: "matching command text only",
			err: &libvirtCommandError{
				Command: "virsh domain not found",
				Err:     errors.New("exit status 1"),
				Stderr:  "error: permission denied",
			},
			want: false,
		},
		{
			name: "empty domain",
			err:  errors.New("error: failed to get domain ''"),
			want: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := libvirtResourceAbsent(test.err); got != test.want {
				t.Fatalf("libvirtResourceAbsent(%q) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestInstanceNameAvailableAcceptsBareFailedDomainLookup(t *testing.T) {
	runner := &lifecycleVirshRunner{
		poolPath:   t.TempDir(),
		failVerb:   "dominfo",
		failError:  errors.New("exit status 1"),
		failStderr: "error: failed to get domain 'candidate-vm'",
	}
	provider := newLifecycleTestProvider(runner.poolPath, runner)
	available, err := provider.instanceNameAvailable(context.Background(), "candidate-vm")
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("bare missing-domain diagnostic was treated as an identity collision")
	}
}

func TestDestroyPreservesMatchingNameWithMismatchedOwnershipMarker(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleVirshRunner{
		poolPath:          poolPath,
		domainExists:      true,
		domainRunning:     true,
		volumeExists:      true,
		domainDescription: "managed-by:gitone:another-controller",
	}
	provider := newLifecycleTestProvider(poolPath, runner)
	name := provider.ownerPrefix + "-20260731120000-abcdef"
	runner.domainName = name
	instance := vmInstance{
		Name:         name,
		VolumeName:   name + ".qcow2",
		IgnitionPath: filepath.Join(poolPath, name+".ign"),
	}

	err := provider.Destroy(context.Background(), instance)
	if err == nil || !strings.Contains(err.Error(), "ownership marker") {
		t.Fatalf("Destroy ownership error = %v", err)
	}
	if !runner.domainExists || !runner.domainRunning || !runner.volumeExists {
		t.Fatalf("Destroy changed an unowned matching-name domain: %#v", runner)
	}
	if containsString(runner.verbs, "destroy") || containsString(runner.verbs, "vol-delete") {
		t.Fatalf("Destroy touched an unowned matching-name domain: %#v", runner.verbs)
	}
}

func TestCleanupNeverSweepsArtifactsForDomainThatFailedTeardown(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleVirshRunner{
		poolPath:      poolPath,
		failVerb:      "destroy",
		domainExists:  true,
		domainRunning: true,
		volumeExists:  true,
	}
	provider := newLifecycleTestProvider(poolPath, runner)
	name := provider.ownerPrefix + "-20260731120000-abcdef"
	runner.domainName = name
	volumePath := filepath.Join(poolPath, name+".qcow2")
	ignitionPath := filepath.Join(poolPath, name+".ign")
	legacyKnownHostsPath := filepath.Join(poolPath, "."+name+".known_hosts")
	for path, contents := range map[string][]byte{
		volumePath:           []byte("qcow2"),
		ignitionPath:         []byte("ignition"),
		legacyKnownHostsPath: []byte("legacy public host key"),
	} {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if err := provider.cleanupOwnedResources(context.Background()); err == nil {
		t.Fatal("Cleanup succeeded while an owned domain could not be stopped")
	}
	if !runner.domainExists || !runner.volumeExists {
		t.Fatalf("Cleanup removed resources still used by the domain: %#v", runner)
	}
	guestSSH := provider.guestSSH.(*recordingLibvirtGuestSSH)
	if containsString(guestSSH.forgotten, name) {
		t.Fatal("failed domain teardown forgot its pinned SSH host key")
	}
	for _, path := range []string{volumePath, ignitionPath, legacyKnownHostsPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("Cleanup removed protected artifact %q: %v", path, err)
		}
	}
}

func TestCleanupRemovesLegacyKnownHostsForAbsentVM(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleVirshRunner{poolPath: poolPath}
	provider := newLifecycleTestProvider(poolPath, runner)
	name := provider.ownerPrefix + "-20260731120000-abcdef"
	legacyKnownHostsPath := filepath.Join(poolPath, "."+name+".known_hosts")
	if err := os.WriteFile(legacyKnownHostsPath, []byte("legacy public host key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provider.cleanupOwnedResources(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacyKnownHostsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy known-hosts file was not removed: %v", err)
	}
}

func TestLibvirtNetworkLockHonorsContextAndCanBeReacquired(t *testing.T) {
	poolPath := t.TempDir()
	provider := &virshVMProvider{config: LibvirtConfig{
		URI:         "test:///system",
		PoolPath:    poolPath,
		NetworkName: "gitone-shared",
	}}
	first, err := provider.acquireNetworkLock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = provider.acquireNetworkLock(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended network lock error = %v", err)
	}
	if err = releaseLibvirtFileLock(first); err != nil {
		t.Fatal(err)
	}
	second, err := provider.acquireNetworkLock(context.Background())
	if err != nil {
		t.Fatalf("reacquire network lock: %v", err)
	}
	if err = releaseLibvirtFileLock(second); err != nil {
		t.Fatal(err)
	}
}

func TestProviderOwnershipAndAddressParsingAreStrict(t *testing.T) {
	prefix := libvirtOwnerPrefix("test:///system", "test-pool", "Runner One")
	provider := &virshVMProvider{
		ownerPrefix: prefix,
		config:      LibvirtConfig{PoolPath: "/var/lib/libvirt/images"},
	}
	name := prefix + "-20260731120000-abcdef"
	if !provider.ownsInstance(vmInstance{
		Name:         name,
		VolumeName:   name + ".qcow2",
		IgnitionPath: "/var/lib/libvirt/images/" + name + ".ign",
	}) {
		t.Fatal("provider rejected a strictly owned instance")
	}
	if prefix == libvirtOwnerPrefix("test:///system", "other-pool", "Runner One") {
		t.Fatal("provider ownership prefix is not scoped to the storage pool")
	}
	for _, candidate := range []string{
		prefix + "-20260731120000-abcde",
		prefix + "-20260731120000-abcdef-extra",
		prefix + "-not-a-time-abcdef",
		"other-20260731120000-abcdef",
	} {
		if provider.ownsName(candidate) {
			t.Fatalf("provider accepted unmanaged name %q", candidate)
		}
	}
	output := "lo 00:00:00:00:00:00 ipv4 127.0.0.1/8\n" +
		"vnet0 52:54:00:01:02:03 ipv6 fe80::1/64\n" +
		"vnet0 52:54:00:01:02:03 ipv4 192.0.2.44/24\n"
	if address := parseLibvirtIPAddress(output); address != "192.0.2.44" {
		t.Fatalf("parsed address = %q", address)
	}
	if !pathWithinDirectory("/var/lib/libvirt/images", "/var/lib/libvirt/images/vm.qcow2") ||
		pathWithinDirectory("/var/lib/libvirt/images", "/var/lib/libvirt/images-other/vm.qcow2") {
		t.Fatal("storage path containment check is incorrect")
	}
}

func TestWriteLibvirtFileAtomicNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.ign")
	if err := writeLibvirtFileAtomic(path, []byte("first"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := writeLibvirtFileAtomic(path, []byte("second"), 0o640); err == nil {
		t.Fatal("atomic writer overwrote an existing file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "first" {
		t.Fatalf("atomic file contents = %q", contents)
	}
}
