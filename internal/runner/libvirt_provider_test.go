package runner

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

func newPrepareTestProvider(t *testing.T) (*libvirtRPCProvider, *prepareLibvirtRPC) {
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
		DockerCommand:   "docker",
		SSHPort:         22,
		VCPUs:           1,
		MemoryMiB:       1024,
		DiskSizeGiB:     2,
		ReadyTimeout:    time.Second,
		CleanupTimeout:  time.Second,
	}
	provider, ok := newLibvirtRPCProvider(config).(*libvirtRPCProvider)
	if !ok {
		t.Fatal("production provider has an unexpected type")
	}
	runner := &prepareLibvirtRPC{
		poolPath:    poolPath,
		networkName: config.NetworkName,
		kvm:         true,
	}
	provider.connector = func(context.Context, string) (libvirtRPCClient, error) {
		return runner, nil
	}
	return provider, runner
}

func TestLibvirtRuntimeConfigRejectsUnsafeValues(t *testing.T) {
	provider, _ := newPrepareTestProvider(t)
	tests := []struct {
		name   string
		mutate func(*LibvirtConfig)
		want   string
	}{
		{"runner ID", func(config *LibvirtConfig) { config.RunnerID = "bad runner" }, "runner ID"},
		{"pool name", func(config *LibvirtConfig) { config.PoolName = "bad pool" }, "pool name"},
		{"base volume", func(config *LibvirtConfig) { config.BaseVolumeName = "bad volume" }, "base volume"},
		{"network name", func(config *LibvirtConfig) { config.NetworkName = "bad network" }, "network name"},
		{"SSH user", func(config *LibvirtConfig) { config.SSHUser = "Root" }, "SSH user"},
		{"Docker command", func(config *LibvirtConfig) { config.DockerCommand = "../docker" }, "Docker command"},
		{"blank URI", func(config *LibvirtConfig) { config.URI = " " }, "URI"},
		{"SSH URI", func(config *LibvirtConfig) {
			config.URI = "qemu+ssh://hypervisor/system"
		}, "+ssh transport"},
		{"relative pool path", func(config *LibvirtConfig) { config.PoolPath = "images" }, "absolute"},
		{"unsafe pool path", func(config *LibvirtConfig) { config.PoolPath = "/tmp/bad,path" }, "unsupported"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := provider.config
			test.mutate(&config)
			candidate := &libvirtRPCProvider{config: config}
			if err := candidate.validateRuntimeConfig(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("runtime validation error = %v, want message containing %q", err, test.want)
			}
		})
	}
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
		mutate func(*prepareLibvirtRPC)
		want   string
	}{
		{
			name: "KVM unavailable",
			mutate: func(runner *prepareLibvirtRPC) {
				runner.kvm = false
			},
			want: "KVM",
		},
		{
			name: "wrong storage type",
			mutate: func(runner *prepareLibvirtRPC) {
				runner.poolType = "logical"
			},
			want: "directory pool",
		},
		{
			name: "wrong storage target",
			mutate: func(runner *prepareLibvirtRPC) {
				runner.poolTarget = filepath.Join(runner.poolPath, "other")
			},
			want: "targets",
		},
		{
			name: "wrong existing network",
			mutate: func(runner *prepareLibvirtRPC) {
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

func newLifecycleTestProvider(poolPath string, client libvirtRPCClient) *libvirtRPCProvider {
	runnerID := "rollback-test"
	return &libvirtRPCProvider{
		config: LibvirtConfig{
			RunnerID:       runnerID,
			URI:            "test:///system",
			PoolName:       "test-pool",
			PoolPath:       poolPath,
			BaseVolumeName: "flatcar-base.qcow2",
			NetworkName:    "gitone-test",
			SSHUser:        "core",
			DockerCommand:  "docker",
			SSHPort:        22,
			VCPUs:          1,
			MemoryMiB:      1024,
			DiskSizeGiB:    2,
			ReadyTimeout:   20 * time.Millisecond,
			CleanupTimeout: time.Second,
		},
		client:      client,
		pool:        libvirt.StoragePool{Name: "test-pool"},
		baseVolume:  libvirt.StorageVol{Pool: "test-pool", Name: "flatcar-base.qcow2"},
		basePath:    filepath.Join(poolPath, "flatcar-base.qcow2"),
		network:     libvirt.Network{Name: "gitone-test"},
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
			runner := &lifecycleLibvirtRPC{poolPath: poolPath, failVerb: test.failVerb}
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
	runner := &lifecycleLibvirtRPC{
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
	const (
		macAddress = "52:54:00:01:02:03"
		address    = "10.240.0.10"
		network    = "gitone-readiness-test"
	)
	runner := &readinessLibvirtRPC{
		state:             "running",
		domainMACAddress:  macAddress,
		domainNetworkName: network,
		domainInterfaces: []libvirt.DomainInterface{{
			Hwaddr: libvirt.OptString{macAddress},
			Addrs:  []libvirt.DomainIPAddr{{Addr: address}},
		}},
		dhcpLeases: []libvirt.NetworkDhcpLease{{
			Mac:        libvirt.OptString{macAddress},
			Ipaddr:     address,
			Expirytime: time.Now().Add(time.Minute).Unix(),
		}},
	}
	guestSSH := &readinessGuestSSH{}
	provider := &libvirtRPCProvider{
		config: LibvirtConfig{
			URI:           "test:///system",
			PoolPath:      poolPath,
			NetworkName:   network,
			NetworkCIDR:   "10.240.0.0/20",
			SSHUser:       "core",
			SSHPort:       22,
			DockerCommand: "docker",
		},
		client:      runner,
		network:     libvirt.Network{Name: network},
		ownerPrefix: libvirtOwnerPrefix("test:///system", "test-pool", "readiness-test"),
		guestSSH:    guestSSH,
	}
	name := provider.ownerPrefix + "-20260731120000-abcdef"
	runner.domainName = name
	runner.domainExists = true
	runner.domainRunning = true
	instance := vmInstance{Name: name, Address: address, MACAddress: macAddress}
	if err := provider.CheckReady(context.Background(), instance); err != nil {
		t.Fatal(err)
	}
	if guestSSH.calls != 1 {
		t.Fatalf("readiness SSH probes = %d, want 1", guestSSH.calls)
	}
	if runner.requestedDHCPMAC != macAddress {
		t.Fatalf("DHCP query MAC = %q, want %q", runner.requestedDHCPMAC, macAddress)
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

func TestProviderCheckReadyRejectsMismatchedNetworkIdentityBeforeSSH(t *testing.T) {
	const (
		macAddress = "52:54:00:01:02:03"
		address    = "10.240.0.10"
		network    = "gitone-readiness-test"
	)
	tests := []struct {
		name   string
		mutate func(*readinessLibvirtRPC, *vmInstance)
		want   string
	}{
		{
			name: "domain MAC",
			mutate: func(runner *readinessLibvirtRPC, _ *vmInstance) {
				runner.domainMACAddress = "52:54:00:04:05:06"
			},
			want: "does not match reserved MAC",
		},
		{
			name: "domain network",
			mutate: func(runner *readinessLibvirtRPC, _ *vmInstance) {
				runner.domainNetworkName = "untrusted-network"
			},
			want: "does not match runner network",
		},
		{
			name: "DHCP MAC",
			mutate: func(runner *readinessLibvirtRPC, _ *vmInstance) {
				runner.dhcpLeases[0].Mac = libvirt.OptString{"52:54:00:04:05:06"}
			},
			want: "has no active DHCP lease",
		},
		{
			name: "DHCP address outside runner network",
			mutate: func(runner *readinessLibvirtRPC, _ *vmInstance) {
				runner.dhcpLeases[0].Ipaddr = "10.250.0.10"
			},
			want: "has no active DHCP lease",
		},
		{
			name: "changed address",
			mutate: func(_ *readinessLibvirtRPC, instance *vmInstance) {
				instance.Address = "10.240.0.11"
			},
			want: "address changed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			poolPath := t.TempDir()
			runner := &readinessLibvirtRPC{
				state:             "running",
				domainExists:      true,
				domainRunning:     true,
				domainMACAddress:  macAddress,
				domainNetworkName: network,
				domainInterfaces: []libvirt.DomainInterface{{
					Hwaddr: libvirt.OptString{macAddress},
					Addrs:  []libvirt.DomainIPAddr{{Addr: address}},
				}},
				dhcpLeases: []libvirt.NetworkDhcpLease{{
					Mac:        libvirt.OptString{macAddress},
					Ipaddr:     address,
					Expirytime: time.Now().Add(time.Minute).Unix(),
				}},
			}
			guestSSH := &readinessGuestSSH{}
			provider := &libvirtRPCProvider{
				config: LibvirtConfig{
					URI:           "test:///system",
					PoolPath:      poolPath,
					NetworkName:   network,
					NetworkCIDR:   "10.240.0.0/20",
					SSHUser:       "core",
					SSHPort:       22,
					DockerCommand: "docker",
				},
				client:      runner,
				network:     libvirt.Network{Name: network},
				ownerPrefix: libvirtOwnerPrefix("test:///system", "test-pool", "readiness-test"),
				guestSSH:    guestSSH,
			}
			instance := vmInstance{
				Name:       provider.ownerPrefix + "-20260731120000-abcdef",
				Address:    address,
				MACAddress: macAddress,
			}
			runner.domainName = instance.Name
			test.mutate(runner, &instance)
			err := provider.CheckReady(context.Background(), instance)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("CheckReady error = %v, want containing %q", err, test.want)
			}
			if guestSSH.calls != 0 {
				t.Fatalf("network identity failure attempted SSH %d times", guestSSH.calls)
			}
		})
	}
}

func TestDestroyIsStrictlyOwnedAndIdempotent(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleLibvirtRPC{poolPath: poolPath}
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
	for _, flags := range runner.destroyFlags {
		if flags&libvirt.DomainDestroyRemoveLogs != 0 {
			removeLogs = true
			break
		}
	}
	if !removeLogs {
		t.Fatalf("Destroy did not request QEMU log cleanup: %#v", runner.destroyFlags)
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
	provider := &libvirtRPCProvider{}
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
	runner := &lifecycleLibvirtRPC{
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
	runner := &lifecycleLibvirtRPC{
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

func TestLibvirtResourceAbsentUsesTypedRPCErrors(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "domain", err: rpcMissing(libvirt.ErrNoDomain, "missing domain"), want: true},
		{name: "network", err: rpcMissing(libvirt.ErrNoNetwork, "missing network"), want: true},
		{name: "storage pool", err: rpcMissing(libvirt.ErrNoStoragePool, "missing pool"), want: true},
		{name: "storage volume", err: rpcMissing(libvirt.ErrNoStorageVol, "missing volume"), want: true},
		{name: "wrapped", err: fmt.Errorf("lookup: %w", rpcMissing(libvirt.ErrNoDomain, "missing")), want: true},
		{name: "transport", err: errors.New("client socket is closed"), want: false},
		{name: "permission", err: rpcMissing(libvirt.ErrOperationDenied, "permission denied"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := libvirtResourceAbsent(test.err); got != test.want {
				t.Fatalf("libvirtResourceAbsent(%q) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestInstanceNameAvailableAcceptsTypedMissingDomain(t *testing.T) {
	runner := &lifecycleLibvirtRPC{
		poolPath:  t.TempDir(),
		failVerb:  "dominfo",
		failError: rpcMissing(libvirt.ErrNoDomain, "candidate domain not found"),
	}
	provider := newLifecycleTestProvider(runner.poolPath, runner)
	available, err := provider.instanceNameAvailable(context.Background(), "candidate-vm")
	if err != nil {
		t.Fatal(err)
	}
	if !available {
		t.Fatal("typed missing-domain error was treated as an identity collision")
	}
}

func TestDestroyPreservesMatchingNameWithMismatchedOwnershipMarker(t *testing.T) {
	poolPath := t.TempDir()
	runner := &lifecycleLibvirtRPC{
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
	runner := &lifecycleLibvirtRPC{
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
	runner := &lifecycleLibvirtRPC{poolPath: poolPath}
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
	provider := &libvirtRPCProvider{config: LibvirtConfig{
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
	provider := &libvirtRPCProvider{
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
	if !pathWithinDirectory("/var/lib/libvirt/images", "/var/lib/libvirt/images/vm.qcow2") ||
		pathWithinDirectory("/var/lib/libvirt/images", "/var/lib/libvirt/images-other/vm.qcow2") {
		t.Fatal("storage path containment check is incorrect")
	}
}

func TestLibvirtAddressSelectionRequiresMACLeaseAndRunnerNetwork(t *testing.T) {
	const macAddress = "52:54:00:01:02:03"
	addressRange, err := newLibvirtDHCPAddressRange("10.240.0.0/20", "gitone-test")
	if err != nil {
		t.Fatal(err)
	}
	address, err := libvirtInterfaceIPAddress([]libvirt.DomainInterface{
		{
			Hwaddr: libvirt.OptString{"52:54:00:04:05:06"},
			Addrs:  []libvirt.DomainIPAddr{{Addr: "10.240.0.20"}},
		},
		{
			Hwaddr: libvirt.OptString{macAddress},
			Addrs: []libvirt.DomainIPAddr{
				{Addr: "127.0.0.1"},
				{Addr: "10.250.0.20"},
				{Addr: "10.240.0.21"},
			},
		},
	}, macAddress, addressRange)
	if err != nil || address != "10.240.0.21" {
		t.Fatalf("domain interface address = %q, err = %v", address, err)
	}

	now := time.Now()
	address, err = libvirtLeaseIPAddress([]libvirt.NetworkDhcpLease{
		{
			Mac:        libvirt.OptString{macAddress},
			Ipaddr:     "10.240.0.22",
			Expirytime: now.Add(-time.Minute).Unix(),
		},
		{
			Mac:        libvirt.OptString{"52:54:00:04:05:06"},
			Ipaddr:     "10.240.0.23",
			Expirytime: now.Add(time.Minute).Unix(),
		},
		{
			Mac:        libvirt.OptString{macAddress},
			Ipaddr:     "10.240.0.24",
			Expirytime: now.Add(time.Minute).Unix(),
		},
	}, macAddress, addressRange, now)
	if err != nil || address != "10.240.0.24" {
		t.Fatalf("DHCP lease address = %q, err = %v", address, err)
	}
}

func TestRenderLibvirtVolumeXMLIncludesBackingStore(t *testing.T) {
	contents, err := renderLibvirtVolumeXML("vm<&>.qcow2", 20<<30, "/pool/base<&>.qcow2")
	if err != nil {
		t.Fatal(err)
	}
	var volume libvirtVolumeCreateDescription
	if err = xml.Unmarshal([]byte(contents), &volume); err != nil {
		t.Fatal(err)
	}
	if volume.XMLName.Local != "volume" || volume.Name != "vm<&>.qcow2" ||
		volume.Capacity != 20<<30 || volume.Allocation != 0 ||
		volume.Target.Format.Type != "qcow2" ||
		volume.BackingStore.Path != "/pool/base<&>.qcow2" ||
		volume.BackingStore.Format.Type != "qcow2" {
		t.Fatalf("rendered volume = %#v", volume)
	}
}

func TestCallLibvirtRPCHonorsContextBeforeAndAfterCall(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	if err := callLibvirtRPC(ctx, func() error {
		called = true
		return nil
	}); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("pre-cancelled RPC result = %v, called=%t", err, called)
	}

	ctx, cancel = context.WithCancel(context.Background())
	if err := callLibvirtRPC(ctx, func() error {
		cancel()
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("RPC cancellation after call = %v", err)
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
