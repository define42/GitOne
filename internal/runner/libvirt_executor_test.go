package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeVMCreateResult struct {
	instance vmInstance
	err      error
}

type fakeVMProvider struct {
	mu sync.Mutex

	prepareErr       error
	cleanupErr       error
	executeErr       error
	prepareN         int
	cleanupN         int
	createN          int
	createNext       int
	create           []fakeVMCreateResult
	destroy          []string
	execute          []string
	checkReady       []string
	checkReadyErr    map[string][]error
	destroyErr       map[string][]error
	createGate       <-chan struct{}
	createActive     int
	createMaxActive  int
	destroyGate      <-chan struct{}
	destroyActive    int
	destroyMaxActive int
}

type blockingCreateVMProvider struct {
	fakeVMProvider
	createMu sync.Mutex
	creates  int
	entered  chan struct{}
	release  chan struct{}
}

func (p *blockingCreateVMProvider) Create(ctx context.Context) (vmInstance, error) {
	p.createMu.Lock()
	p.creates++
	createNumber := p.creates
	p.createMu.Unlock()
	if createNumber == 1 {
		return p.fakeVMProvider.Create(ctx)
	}
	p.fakeVMProvider.mu.Lock()
	p.fakeVMProvider.createN++
	p.fakeVMProvider.mu.Unlock()
	close(p.entered)
	<-p.release
	return vmInstance{}, context.Cause(ctx)
}

func (p *fakeVMProvider) Prepare(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.prepareN++
	return p.prepareErr
}

func (p *fakeVMProvider) Create(ctx context.Context) (vmInstance, error) {
	p.mu.Lock()
	p.createN++
	p.createActive++
	if p.createActive > p.createMaxActive {
		p.createMaxActive = p.createActive
	}
	gate := p.createGate
	var result fakeVMCreateResult
	if len(p.create) > 0 {
		result = p.create[0]
		p.create = p.create[1:]
	} else {
		p.createNext++
		name := fmt.Sprintf("test-vm-%d", p.createNext)
		result.instance = testVM(name)
	}
	p.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			result = fakeVMCreateResult{err: context.Cause(ctx)}
		}
	}
	p.mu.Lock()
	p.createActive--
	p.mu.Unlock()
	return result.instance, result.err
}

func (p *fakeVMProvider) Destroy(ctx context.Context, instance vmInstance) error {
	p.mu.Lock()
	p.destroy = append(p.destroy, instance.Name)
	p.destroyActive++
	if p.destroyActive > p.destroyMaxActive {
		p.destroyMaxActive = p.destroyActive
	}
	gate := p.destroyGate
	var err error
	if len(p.destroyErr[instance.Name]) > 0 {
		err = p.destroyErr[instance.Name][0]
		p.destroyErr[instance.Name] = p.destroyErr[instance.Name][1:]
	}
	p.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			if err == nil {
				err = ctx.Err()
			}
		}
	}
	p.mu.Lock()
	p.destroyActive--
	p.mu.Unlock()
	return err
}

func (p *fakeVMProvider) CheckReady(ctx context.Context, instance vmInstance) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.checkReady = append(p.checkReady, instance.Name)
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if len(p.checkReadyErr[instance.Name]) == 0 {
		return nil
	}
	err := p.checkReadyErr[instance.Name][0]
	p.checkReadyErr[instance.Name] = p.checkReadyErr[instance.Name][1:]
	return err
}

func (p *fakeVMProvider) Cleanup(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cleanupN++
	return p.cleanupErr
}

func (p *fakeVMProvider) Execute(
	_ context.Context,
	instance vmInstance,
	_ ExecuteRequest,
	output io.Writer,
) error {
	p.mu.Lock()
	p.execute = append(p.execute, instance.Name)
	err := p.executeErr
	p.mu.Unlock()
	if output != nil {
		_, _ = fmt.Fprintln(output, "executed on "+instance.Name)
	}
	return err
}

func (p *fakeVMProvider) snapshot() fakeVMProviderSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return fakeVMProviderSnapshot{
		prepareN:         p.prepareN,
		cleanupN:         p.cleanupN,
		createN:          p.createN,
		createMaxActive:  p.createMaxActive,
		destroy:          append([]string(nil), p.destroy...),
		execute:          append([]string(nil), p.execute...),
		checkReady:       append([]string(nil), p.checkReady...),
		destroyMaxActive: p.destroyMaxActive,
	}
}

type fakeVMProviderSnapshot struct {
	prepareN         int
	cleanupN         int
	createN          int
	createMaxActive  int
	destroy          []string
	execute          []string
	checkReady       []string
	destroyMaxActive int
}

func testVM(name string) vmInstance {
	return vmInstance{
		Name:         name,
		Address:      "192.0.2.10",
		MACAddress:   "52:54:00:00:00:01",
		VolumeName:   name + ".qcow2",
		IgnitionPath: "/tmp/" + name + ".ign",
	}
}

func validLibvirtTestConfig() LibvirtConfig {
	return LibvirtConfig{
		BaseVolumeName: "flatcar.qcow2",
		IdleCount:      1,
		MaxInstances:   2,
		ReadyTimeout:   time.Second,
		CleanupTimeout: time.Second,
	}
}

func newTestLibvirtExecutor(
	t *testing.T,
	config LibvirtConfig,
	provider vmProvider,
) *LibvirtExecutor {
	t.Helper()
	executor, err := newLibvirtExecutorWithProvider(config, provider)
	if err != nil {
		t.Fatal(err)
	}
	executor.retryDelay = 10 * time.Millisecond
	return executor
}

func TestLibvirtConfigNormalizationAndValidation(t *testing.T) {
	provider := &fakeVMProvider{}
	executor, err := newLibvirtExecutorWithProvider(LibvirtConfig{
		BaseVolumeName:     " flatcar.qcow2 ",
		RegistryMirrors:    []string{" https://mirror.example.test "},
		InsecureRegistries: []string{" registry.example.test:5000 "},
	}, provider)
	if err != nil {
		t.Fatal(err)
	}
	config := executor.config
	if config.RunnerID != defaultLibvirtRunnerID ||
		config.URI != defaultLibvirtURI ||
		config.PoolName != defaultLibvirtPoolName ||
		config.PoolPath != defaultLibvirtPoolPath ||
		config.BaseImageURL != DefaultFlatcarBaseImageURL ||
		config.BaseImageSHA512 != DefaultFlatcarBaseImageSHA512 ||
		config.NetworkName != defaultLibvirtNetworkName ||
		config.NetworkCIDR != defaultLibvirtNetworkCIDR(defaultLibvirtNetworkName) ||
		config.SSHUser != defaultLibvirtSSHUser ||
		config.VirshCommand != defaultLibvirtVirshCommand ||
		config.DockerCommand != defaultLibvirtDockerCommand ||
		config.SSHPort != defaultLibvirtSSHPort ||
		config.VCPUs != defaultLibvirtVCPUs ||
		config.MemoryMiB != defaultLibvirtMemoryMiB ||
		config.DiskSizeGiB != defaultLibvirtDiskSizeGiB ||
		config.IdleCount != DefaultLibvirtIdleCount ||
		config.MaxInstances != DefaultLibvirtMaxInstances ||
		config.ReadyTimeout != defaultLibvirtReadyTimeout ||
		config.CleanupTimeout != defaultLibvirtCleanupTimeout {
		t.Fatalf("normalized config = %#v", config)
	}
	if config.BaseVolumeName != "flatcar.qcow2" ||
		config.RegistryMirrors[0] != "https://mirror.example.test" ||
		config.InsecureRegistries[0] != "registry.example.test:5000" {
		t.Fatalf("trimmed config = %#v", config)
	}

	tests := []struct {
		name   string
		mutate func(*LibvirtConfig)
		want   string
	}{
		{"runner ID", func(c *LibvirtConfig) { c.RunnerID = "bad runner" }, "runner ID"},
		{"external SSH URI", func(c *LibvirtConfig) {
			c.URI = "qemu+ssh://hypervisor/system"
		}, "+ssh transport"},
		{"base volume", func(c *LibvirtConfig) { c.BaseVolumeName = " " }, "base volume"},
		{"base image URL", func(c *LibvirtConfig) {
			c.BaseImageURL = "http://downloads.example.test/flatcar.img"
		}, "HTTPS"},
		{"base image URL credentials", func(c *LibvirtConfig) {
			c.BaseImageURL = "https://user:secret@downloads.example.test/flatcar.img"
		}, "credentials"},
		{"base image digest", func(c *LibvirtConfig) {
			c.BaseImageSHA512 = "not-a-sha512"
		}, "128 hexadecimal"},
		{"network CIDR size", func(c *LibvirtConfig) { c.NetworkCIDR = "10.20.0.0/24" }, "IPv4 /20"},
		{"network CIDR alignment", func(c *LibvirtConfig) { c.NetworkCIDR = "10.20.1.0/20" }, "aligned"},
		{"public network CIDR", func(c *LibvirtConfig) { c.NetworkCIDR = "203.0.112.0/20" }, "private"},
		{"loopback network CIDR", func(c *LibvirtConfig) { c.NetworkCIDR = "127.0.0.0/20" }, "private"},
		{"unspecified network CIDR", func(c *LibvirtConfig) { c.NetworkCIDR = "0.0.0.0/20" }, "private"},
		{"SSH port", func(c *LibvirtConfig) { c.SSHPort = -1 }, "SSH port"},
		{"VCPUs", func(c *LibvirtConfig) { c.VCPUs = -1 }, "VCPUs"},
		{"memory", func(c *LibvirtConfig) { c.MemoryMiB = 255 }, "memory"},
		{"disk", func(c *LibvirtConfig) { c.DiskSizeGiB = -1 }, "disk size"},
		{"idle count", func(c *LibvirtConfig) { c.IdleCount = -1 }, "idle count"},
		{"max instances", func(c *LibvirtConfig) { c.MaxInstances = -1 }, "maximum instances"},
		{"too many instances", func(c *LibvirtConfig) {
			c.MaxInstances = maximumLibvirtInstances + 1
		}, "cannot exceed"},
		{"idle over max", func(c *LibvirtConfig) {
			c.IdleCount, c.MaxInstances = 2, 1
		}, "cannot exceed"},
		{"ready timeout", func(c *LibvirtConfig) {
			c.ReadyTimeout = 500 * time.Millisecond
		}, "ready timeout"},
		{"cleanup timeout", func(c *LibvirtConfig) {
			c.CleanupTimeout = -time.Second
		}, "cleanup timeout"},
		{"long cleanup timeout", func(c *LibvirtConfig) {
			c.CleanupTimeout = maximumLibvirtCleanupTimeout + time.Second
		}, "cannot exceed"},
		{"blank mirror", func(c *LibvirtConfig) {
			c.RegistryMirrors = []string{" "}
		}, "registry mirror"},
		{"blank insecure registry", func(c *LibvirtConfig) {
			c.InsecureRegistries = []string{""}
		}, "insecure registry"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validLibvirtTestConfig()
			test.mutate(&config)
			_, err := newLibvirtExecutorWithProvider(config, provider)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
	if _, err = newLibvirtExecutorWithProvider(validLibvirtTestConfig(), nil); err == nil {
		t.Fatal("nil provider was accepted")
	}
}

func TestLibvirtExecutorPreheatsAndShutsDownIdempotently(t *testing.T) {
	provider := &fakeVMProvider{}
	config := validLibvirtTestConfig()
	config.IdleCount = 2
	config.MaxInstances = 3
	executor := newTestLibvirtExecutor(t, config, provider)

	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertLibvirtPool(t, executor, 2, 0, 0, 2)
	snapshot := provider.snapshot()
	if snapshot.prepareN != 1 || snapshot.createN != 2 {
		t.Fatalf("provider after start = %#v", snapshot)
	}

	if err := executor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := executor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot = provider.snapshot()
	if snapshot.cleanupN != 1 || len(snapshot.destroy) != 2 {
		t.Fatalf("provider after shutdown = %#v", snapshot)
	}
	if _, err := executor.Reserve(context.Background()); err == nil {
		t.Fatal("shut-down executor allowed a reservation")
	}
}

func TestLibvirtExecutorPreheatsFullDeficitConcurrently(t *testing.T) {
	gate := make(chan struct{})
	var closeGate sync.Once
	releaseCreates := func() {
		closeGate.Do(func() { close(gate) })
	}
	defer releaseCreates()
	provider := &fakeVMProvider{createGate: gate}
	config := validLibvirtTestConfig()
	config.IdleCount = 4
	config.MaxInstances = 4
	executor := newTestLibvirtExecutor(t, config, provider)

	startResult := make(chan error, 1)
	go func() {
		startResult <- executor.Start(context.Background())
	}()
	eventuallyLibvirt(t, func() bool {
		return provider.snapshot().createMaxActive == 4
	})
	releaseCreates()
	if err := <-startResult; err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()
	assertLibvirtPool(t, executor, 4, 0, 0, 4)
}

func TestLibvirtShutdownDestroysVMsConcurrently(t *testing.T) {
	gate := make(chan struct{})
	provider := &fakeVMProvider{destroyGate: gate}
	config := validLibvirtTestConfig()
	config.IdleCount = 3
	config.MaxInstances = 3
	executor := newTestLibvirtExecutor(t, config, provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	var closeGate sync.Once
	releaseDestroys := func() {
		closeGate.Do(func() { close(gate) })
	}
	defer releaseDestroys()
	shutdownResult := make(chan error, 1)
	go func() {
		shutdownResult <- executor.Shutdown(context.Background())
	}()
	eventuallyLibvirt(t, func() bool {
		return provider.snapshot().destroyMaxActive == 3
	})
	if cleanupN := provider.snapshot().cleanupN; cleanupN != 0 {
		t.Fatalf("provider cleanup started while VM destruction was active: %d", cleanupN)
	}
	releaseDestroys()
	select {
	case err := <-shutdownResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent shutdown did not finish")
	}
	snapshot := provider.snapshot()
	if snapshot.destroyMaxActive != 3 || len(snapshot.destroy) != 3 || snapshot.cleanupN != 1 {
		t.Fatalf("concurrent shutdown activity = %#v", snapshot)
	}
}

func TestLibvirtShutdownDeadlineBoundsInFlightProvisioningWait(t *testing.T) {
	provider := &blockingCreateVMProvider{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.(AssignableExecutorReservation).Assign()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("replacement provisioning did not start")
	}

	shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Millisecond)
	err = executor.Shutdown(shutdownContext)
	cancelShutdown()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("short Shutdown error = %v, want deadline exceeded", err)
	}
	close(provider.release)
	if err = executor.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
}

func TestLibvirtCleanupBudgets(t *testing.T) {
	config := validLibvirtTestConfig()
	config.CleanupTimeout = 5 * time.Second
	executor := newTestLibvirtExecutor(t, config, &fakeVMProvider{})
	if got := executor.ShutdownTimeout(); got != 15*time.Second {
		t.Fatalf("ShutdownTimeout() = %s, want 15s", got)
	}
	reservation := &libvirtReservation{executor: executor, name: "budget-test"}
	if got := reservation.ReleaseTimeout(); got != 5*time.Second {
		t.Fatalf("ReleaseTimeout() = %s, want 5s", got)
	}
	maxConfig := validLibvirtTestConfig()
	maxConfig.CleanupTimeout = maximumLibvirtCleanupTimeout
	maxExecutor := newTestLibvirtExecutor(t, maxConfig, &fakeVMProvider{})
	if got := boundedExecutorCleanupTimeout(maxExecutor.ShutdownTimeout()); got != 45*time.Minute {
		t.Fatalf("maximum bounded ShutdownTimeout() = %s, want 45m", got)
	}
}

func TestLibvirtExecutorStartupFailureCleansCreatedResources(t *testing.T) {
	t.Run("cancelled before prepare can retry", func(t *testing.T) {
		provider := &fakeVMProvider{}
		executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
		cancelled, cancel := context.WithCancel(context.Background())
		cancel()
		if err := executor.Start(cancelled); !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() error = %v, want context canceled", err)
		}
		if provider.snapshot().prepareN != 0 {
			t.Fatal("pre-cancelled Start prepared the provider")
		}
		if err := executor.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := executor.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("prepare failure", func(t *testing.T) {
		provider := &fakeVMProvider{prepareErr: errors.New("libvirt unavailable")}
		executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
		err := executor.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "libvirt unavailable") {
			t.Fatalf("Start() error = %v", err)
		}
		snapshot := provider.snapshot()
		if snapshot.prepareN != 1 || snapshot.createN != 0 || snapshot.cleanupN != 1 {
			t.Fatalf("prepare failure cleanup = %#v", snapshot)
		}
	})

	t.Run("provision failure", func(t *testing.T) {
		provider := &fakeVMProvider{create: []fakeVMCreateResult{
			{instance: testVM("warm-one")},
			{err: errors.New("clone failed")},
		}}
		config := validLibvirtTestConfig()
		config.IdleCount = 2
		executor := newTestLibvirtExecutor(t, config, provider)
		err := executor.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "clone failed") {
			t.Fatalf("Start() error = %v", err)
		}
		snapshot := provider.snapshot()
		if snapshot.cleanupN != 1 || len(snapshot.destroy) != 1 ||
			snapshot.destroy[0] != "warm-one" {
			t.Fatalf("startup cleanup = %#v", snapshot)
		}
		if err = executor.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if provider.snapshot().cleanupN != 1 {
			t.Fatal("Shutdown repeated cleanup after failed startup")
		}
	})

	t.Run("partial provision failure", func(t *testing.T) {
		provider := &fakeVMProvider{create: []fakeVMCreateResult{{
			instance: testVM("partial-vm"),
			err:      errors.New("start failed and rollback failed"),
		}}}
		executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
		err := executor.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "rollback failed") {
			t.Fatalf("Start() error = %v", err)
		}
		snapshot := provider.snapshot()
		if len(snapshot.destroy) != 1 || snapshot.destroy[0] != "partial-vm" ||
			snapshot.cleanupN != 1 {
			t.Fatalf("partial provision cleanup = %#v", snapshot)
		}
	})

	t.Run("duplicate name", func(t *testing.T) {
		provider := &fakeVMProvider{create: []fakeVMCreateResult{
			{instance: testVM("duplicate")},
			{instance: testVM("duplicate")},
		}}
		config := validLibvirtTestConfig()
		config.IdleCount = 2
		executor := newTestLibvirtExecutor(t, config, provider)
		err := executor.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "duplicate VM") {
			t.Fatalf("Start() error = %v", err)
		}
		snapshot := provider.snapshot()
		if len(snapshot.destroy) != 1 || snapshot.destroy[0] != "duplicate" ||
			snapshot.cleanupN != 1 {
			t.Fatalf("duplicate cleanup = %#v", snapshot)
		}
	})

	t.Run("invalid VM", func(t *testing.T) {
		invalid := testVM("invalid")
		invalid.Address = ""
		provider := &fakeVMProvider{create: []fakeVMCreateResult{{instance: invalid}}}
		executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
		err := executor.Start(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no SSH address") {
			t.Fatalf("Start() error = %v", err)
		}
		snapshot := provider.snapshot()
		if len(snapshot.destroy) != 1 || snapshot.destroy[0] != "invalid" ||
			snapshot.cleanupN != 1 {
			t.Fatalf("invalid VM cleanup = %#v", snapshot)
		}
	})

	t.Run("rollback destroys preheated VMs concurrently", func(t *testing.T) {
		gate := make(chan struct{})
		provider := &fakeVMProvider{
			create: []fakeVMCreateResult{
				{instance: testVM("warm-one")},
				{instance: testVM("warm-two")},
				{instance: testVM("warm-three")},
				{err: errors.New("fourth VM failed")},
			},
			destroyGate: gate,
		}
		config := validLibvirtTestConfig()
		config.IdleCount = 4
		config.MaxInstances = 4
		executor := newTestLibvirtExecutor(t, config, provider)
		result := make(chan error, 1)
		go func() {
			result <- executor.Start(context.Background())
		}()
		eventuallyLibvirt(t, func() bool {
			return len(provider.snapshot().destroy) == 3
		})
		close(gate)
		if err := <-result; err == nil || !strings.Contains(err.Error(), "fourth VM failed") {
			t.Fatalf("Start error = %v", err)
		}
		if got := provider.snapshot().destroyMaxActive; got != 3 {
			t.Fatalf("startup rollback max concurrency = %d, want 3", got)
		}
	})
}

func TestUnusedLibvirtReservationsNeverGrowWarmPool(t *testing.T) {
	provider := &fakeVMProvider{}
	config := validLibvirtTestConfig()
	config.IdleCount = 2
	config.MaxInstances = 5
	executor := newTestLibvirtExecutor(t, config, provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	for range 25 {
		reservation, err := executor.Reserve(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err = reservation.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(5 * executor.retryDelay)
	assertLibvirtPool(t, executor, 2, 0, 0, 2)
	if creates := provider.snapshot().createN; creates != 2 {
		t.Fatalf("unused reservations caused %d creates, want 2", creates)
	}
}

func TestConcurrentLibvirtReservationsUseDistinctVMs(t *testing.T) {
	provider := &fakeVMProvider{}
	config := validLibvirtTestConfig()
	config.IdleCount = 2
	config.MaxInstances = 4
	executor := newTestLibvirtExecutor(t, config, provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	reservations := make(chan ExecutorReservation, 2)
	errorsChannel := make(chan error, 2)
	reserveContext, cancelReserve := context.WithTimeout(context.Background(), time.Second)
	defer cancelReserve()
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			reservation, err := executor.Reserve(reserveContext)
			if err != nil {
				errorsChannel <- err
				return
			}
			reservations <- reservation
		}()
	}
	workers.Wait()
	close(reservations)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatal(err)
	}
	var acquired []ExecutorReservation
	names := make(map[string]struct{}, 2)
	for reservation := range reservations {
		acquired = append(acquired, reservation)
		name := reservation.(*libvirtReservation).name
		names[name] = struct{}{}
	}
	if len(acquired) != 2 || len(names) != 2 {
		t.Fatalf("reservations = %d, distinct VMs = %v", len(acquired), names)
	}
	for _, reservation := range acquired {
		if err := reservation.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	assertLibvirtPool(t, executor, 2, 0, 0, 2)
}

func TestLibvirtReserveRecyclesUnhealthyWarmVM(t *testing.T) {
	provider := &fakeVMProvider{
		create: []fakeVMCreateResult{
			{instance: testVM("stale-warm")},
			{instance: testVM("healthy-replacement")},
		},
		checkReadyErr: map[string][]error{
			"stale-warm": {errors.New("Docker is unavailable")},
		},
	}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	reservation, err := executor.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reserved, ok := reservation.(*libvirtReservation)
	if !ok || reserved.name != "healthy-replacement" {
		t.Fatalf("reservation = %#v, want healthy replacement", reservation)
	}
	snapshot := provider.snapshot()
	if !containsString(snapshot.destroy, "stale-warm") ||
		len(snapshot.checkReady) != 2 ||
		snapshot.checkReady[0] != "stale-warm" ||
		snapshot.checkReady[1] != "healthy-replacement" {
		t.Fatalf("warm VM health activity = %#v", snapshot)
	}
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAssignedLibvirtReservationIsSingleUseAndReplenished(t *testing.T) {
	provider := &fakeVMProvider{}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assignable, ok := reservation.(AssignableExecutorReservation)
	if !ok {
		t.Fatal("reservation is not assignable")
	}
	assignable.Assign()
	assignable.Assign()
	eventuallyLibvirt(t, func() bool {
		idle, leased, destroying, total := libvirtPoolCounts(executor)
		return provider.snapshot().createN == 2 && idle == 1 && leased == 1 &&
			destroying == 0 && total == 2
	})
	assertLibvirtPool(t, executor, 1, 1, 0, 2)

	var output bytes.Buffer
	request := ExecuteRequest{Job: Job{ID: "libvirt-test-build"}}
	if err = reservation.Run(context.Background(), request, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "executed on test-vm-1") {
		t.Fatalf("execution output = %q", output.String())
	}
	if err = reservation.Run(context.Background(), request, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "only one") {
		t.Fatalf("second Run() error = %v", err)
	}
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventuallyLibvirt(t, func() bool {
		idle, leased, destroying, total := libvirtPoolCounts(executor)
		return idle == 1 && leased == 0 && destroying == 0 && total == 1
	})
	snapshot := provider.snapshot()
	if len(snapshot.execute) != 1 || snapshot.execute[0] != "test-vm-1" ||
		len(snapshot.destroy) != 1 || snapshot.destroy[0] != "test-vm-1" {
		t.Fatalf("provider activity = %#v", snapshot)
	}
}

func TestAssignedLibvirtReservationsRefillFullDeficitConcurrently(t *testing.T) {
	provider := &fakeVMProvider{}
	config := validLibvirtTestConfig()
	config.IdleCount = 3
	config.MaxInstances = 6
	executor := newTestLibvirtExecutor(t, config, provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	reservations := make([]ExecutorReservation, 0, config.IdleCount)
	for range config.IdleCount {
		reservation, err := executor.Reserve(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		reservations = append(reservations, reservation)
	}

	gate := make(chan struct{})
	var closeGate sync.Once
	releaseCreates := func() {
		closeGate.Do(func() { close(gate) })
	}
	defer releaseCreates()
	provider.mu.Lock()
	provider.createGate = gate
	provider.createMaxActive = 0
	provider.mu.Unlock()
	for _, reservation := range reservations {
		reservation.(AssignableExecutorReservation).Assign()
	}
	eventuallyLibvirt(t, func() bool {
		return provider.snapshot().createMaxActive == config.IdleCount
	})
	assertLibvirtPool(t, executor, 0, 3, 0, 6)

	releaseCreates()
	eventuallyLibvirt(t, func() bool {
		idle, leased, destroying, total := libvirtPoolCounts(executor)
		return idle == 3 && leased == 3 && destroying == 0 && total == 6
	})
	for _, reservation := range reservations {
		if err := reservation.Release(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	assertLibvirtPool(t, executor, 3, 0, 0, 3)
}

func TestFailedDestroyDoesNotStarveRefillWithSpareCapacity(t *testing.T) {
	destroyFailures := make([]error, 100)
	for index := range destroyFailures {
		destroyFailures[index] = errors.New("persistent destroy failure")
	}
	provider := &fakeVMProvider{
		create: []fakeVMCreateResult{
			{instance: testVM("warm-one")},
			{instance: testVM("warm-two")},
			{instance: testVM("replacement")},
		},
		checkReadyErr: make(map[string][]error),
		destroyErr:    make(map[string][]error),
	}
	config := validLibvirtTestConfig()
	config.IdleCount = 2
	config.MaxInstances = 3
	executor := newTestLibvirtExecutor(t, config, provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()
	executor.mu.Lock()
	unhealthyName := executor.idle[0]
	executor.mu.Unlock()
	provider.mu.Lock()
	provider.checkReadyErr[unhealthyName] = []error{errors.New("guest health check failed")}
	provider.destroyErr[unhealthyName] = destroyFailures
	provider.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reservation, err := executor.Reserve(ctx)
	if err != nil {
		t.Fatal(err)
	}
	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return snapshot.createN == 3 && len(snapshot.destroy) > 0
	})
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventuallyLibvirt(t, func() bool {
		idle, leased, destroying, total := libvirtPoolCounts(executor)
		return idle == 2 && leased == 0 && destroying == 1 && total == 3
	})
}

func TestAssignedReservationWithoutRunIsDestroyed(t *testing.T) {
	provider := &fakeVMProvider{}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.(AssignableExecutorReservation).Assign()
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return snapshot.createN == 2 && len(snapshot.destroy) == 1
	})
	if len(provider.snapshot().execute) != 0 {
		t.Fatal("assigned reservation executed a build during release")
	}
}

func TestLibvirtMaxInstancesIncludesAssignedVMs(t *testing.T) {
	provider := &fakeVMProvider{}
	config := validLibvirtTestConfig()
	config.MaxInstances = 1
	executor := newTestLibvirtExecutor(t, config, provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.(AssignableExecutorReservation).Assign()
	time.Sleep(5 * executor.retryDelay)
	if creates := provider.snapshot().createN; creates != 1 {
		t.Fatalf("created %d VMs while assigned VM occupied maximum of 1", creates)
	}
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventuallyLibvirt(t, func() bool {
		idle, leased, destroying, total := libvirtPoolCounts(executor)
		return provider.snapshot().createN == 2 && idle == 1 && leased == 0 &&
			destroying == 0 && total == 1
	})
	assertLibvirtPool(t, executor, 1, 0, 0, 1)
}

func TestLibvirtProvisioningRetriesWithoutBusyLoop(t *testing.T) {
	provider := &fakeVMProvider{create: []fakeVMCreateResult{
		{instance: testVM("warm-initial")},
		{err: errors.New("temporary provision failure")},
		{instance: testVM("warm-replacement")},
	}}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	executor.retryDelay = 30 * time.Millisecond
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	reservation.(AssignableExecutorReservation).Assign()
	eventuallyLibvirt(t, func() bool {
		return provider.snapshot().createN == 3
	})
	if elapsed := time.Since(started); elapsed < executor.retryDelay/2 {
		t.Fatalf("provision retry took %s, expected a backoff", elapsed)
	}
	if creates := provider.snapshot().createN; creates != 3 {
		t.Fatalf("provision attempts = %d, want exactly 3", creates)
	}
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLibvirtReleaseFailureIsRetried(t *testing.T) {
	provider := &fakeVMProvider{destroyErr: map[string][]error{
		"test-vm-1": {errors.New("domain is busy")},
	}}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.(AssignableExecutorReservation).Assign()
	err = reservation.Release(context.Background())
	if err == nil || !strings.Contains(err.Error(), "domain is busy") {
		t.Fatalf("Release() error = %v", err)
	}
	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return len(snapshot.destroy) >= 2 && snapshot.createN >= 2
	})
}

func TestLibvirtReserveHonorsCancellation(t *testing.T) {
	provider := &fakeVMProvider{}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()
	cancelled, cancelImmediately := context.WithCancel(context.Background())
	cancelImmediately()
	if _, err := executor.Reserve(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled Reserve() error = %v, want context canceled", err)
	}

	first, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = executor.Reserve(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Reserve() error = %v, want deadline exceeded", err)
	}
	if err = first.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLibvirtExecutorOrdinaryRunUsesAndReleasesVM(t *testing.T) {
	provider := &fakeVMProvider{}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()

	var output bytes.Buffer
	if err := executor.Run(
		context.Background(),
		ExecuteRequest{Job: Job{ID: "ordinary-run"}},
		&output,
	); err != nil {
		t.Fatal(err)
	}
	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return len(snapshot.execute) == 1 && len(snapshot.destroy) == 1 &&
			snapshot.createN == 2
	})
}

func TestLibvirtCleanupContextHonorsExpiredParentDeadline(t *testing.T) {
	executor := newTestLibvirtExecutor(
		t,
		validLibvirtTestConfig(),
		&fakeVMProvider{},
	)
	parent, cancel := context.WithDeadline(
		context.Background(),
		time.Now().Add(-time.Second),
	)
	defer cancel()
	cleanup, cancelCleanup := executor.newCleanupContext(parent)
	defer cancelCleanup()
	if !errors.Is(cleanup.Err(), context.DeadlineExceeded) {
		t.Fatalf("cleanup context error = %v, want deadline exceeded", cleanup.Err())
	}
}

func TestAsyncInvalidLibvirtVMIsDestroyedBeforeRetry(t *testing.T) {
	invalid := testVM("invalid-replacement")
	invalid.Address = ""
	provider := &fakeVMProvider{create: []fakeVMCreateResult{
		{instance: testVM("warm-initial")},
		{instance: invalid},
		{instance: testVM("valid-replacement")},
	}}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()
	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.(AssignableExecutorReservation).Assign()
	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return snapshot.createN == 3 && containsString(snapshot.destroy, "invalid-replacement")
	})
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncPartialLibvirtVMIsDestroyedBeforeRetry(t *testing.T) {
	provider := &fakeVMProvider{create: []fakeVMCreateResult{
		{instance: testVM("warm-initial")},
		{
			instance: testVM("partial-replacement"),
			err:      errors.New("create failed after allocating resources"),
		},
		{instance: testVM("valid-replacement")},
	}}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()
	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.(AssignableExecutorReservation).Assign()
	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return snapshot.createN == 3 && containsString(snapshot.destroy, "partial-replacement")
	})
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncFailedPartialCleanupRemainsTrackedAndCapped(t *testing.T) {
	provider := &fakeVMProvider{
		create: []fakeVMCreateResult{
			{instance: testVM("warm-initial")},
			{
				instance: testVM("partial-replacement"),
				err:      errors.New("create failed after allocating resources"),
			},
			{instance: testVM("valid-replacement")},
		},
		destroyErr: map[string][]error{
			"partial-replacement": {
				errors.New("first cleanup failure"),
				errors.New("second cleanup failure"),
			},
		},
	}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	executor.retryDelay = 200 * time.Millisecond
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()
	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.(AssignableExecutorReservation).Assign()

	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return snapshot.createN == 2 && len(snapshot.destroy) == 1
	})
	assertLibvirtPool(t, executor, 0, 1, 1, 2)
	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return snapshot.createN == 2 && len(snapshot.destroy) == 2
	})
	if creates := provider.snapshot().createN; creates != 2 {
		t.Fatalf("created %d VMs while failed cleanup occupied capacity, want 2", creates)
	}
	assertLibvirtPool(t, executor, 0, 1, 1, 2)

	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return snapshot.createN == 3 && len(snapshot.destroy) >= 3
	})
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncDuplicateLibvirtVMDoesNotDestroyTrackedLease(t *testing.T) {
	provider := &fakeVMProvider{create: []fakeVMCreateResult{
		{instance: testVM("warm-initial")},
		{instance: testVM("warm-initial")},
		{instance: testVM("valid-replacement")},
	}}
	executor := newTestLibvirtExecutor(t, validLibvirtTestConfig(), provider)
	if err := executor.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executor.Shutdown(context.Background()) }()
	reservation, err := executor.Reserve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.(AssignableExecutorReservation).Assign()
	eventuallyLibvirt(t, func() bool {
		snapshot := provider.snapshot()
		return snapshot.createN == 3
	})
	if destroys := provider.snapshot().destroy; len(destroys) != 0 {
		t.Fatalf("duplicate result destroyed tracked VM: %#v", destroys)
	}
	if err = reservation.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func assertLibvirtPool(
	t *testing.T,
	executor *LibvirtExecutor,
	wantIdle int,
	wantLeased int,
	wantDestroying int,
	wantTotal int,
) {
	t.Helper()
	idle, leased, destroying, total := libvirtPoolCounts(executor)
	if idle != wantIdle || leased != wantLeased || destroying != wantDestroying ||
		total != wantTotal {
		t.Fatalf(
			"pool = idle:%d leased:%d destroying:%d total:%d, want %d/%d/%d/%d",
			idle,
			leased,
			destroying,
			total,
			wantIdle,
			wantLeased,
			wantDestroying,
			wantTotal,
		)
	}
}

func libvirtPoolCounts(executor *LibvirtExecutor) (int, int, int, int) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	var idle, leased, destroying int
	for _, instance := range executor.instances {
		switch instance.state {
		case vmIdle:
			idle++
		case vmReserved, vmLeased:
			leased++
		case vmDestroying:
			destroying++
		}
	}
	return idle, leased, destroying, len(executor.instances) + executor.creating
}

func eventuallyLibvirt(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition was not satisfied before timeout")
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
