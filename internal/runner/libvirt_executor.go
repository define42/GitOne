package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultLibvirtRunnerID       = "gitone-runner"
	defaultLibvirtURI            = "qemu:///system"
	defaultLibvirtPoolName       = "default"
	defaultLibvirtPoolPath       = "/var/lib/libvirt/images"
	defaultLibvirtNetworkName    = "gitone-runner"
	defaultLibvirtSSHUser        = "core"
	defaultLibvirtVirshCommand   = "virsh"
	defaultLibvirtSSHCommand     = "ssh"
	defaultLibvirtDockerCommand  = "docker"
	defaultLibvirtSSHPort        = 22
	defaultLibvirtVCPUs          = 2
	defaultLibvirtMemoryMiB      = 4096
	defaultLibvirtDiskSizeGiB    = 20
	defaultLibvirtIdleCount      = 1
	defaultLibvirtReadyTimeout   = 10 * time.Minute
	defaultLibvirtCleanupTimeout = 30 * time.Second
	defaultLibvirtRetryDelay     = time.Second
	defaultLibvirtHealthTimeout  = 10 * time.Second
	maximumLibvirtCleanupTimeout = 15 * time.Minute
	maximumLibvirtInstances      = 64
)

// LibvirtConfig describes the local hypervisor and the warm VM pool used by a
// GitOne runner. Each VM executes at most one build.
type LibvirtConfig struct {
	RunnerID           string
	URI                string
	PoolName           string
	PoolPath           string
	BaseVolumeName     string
	BaseImageURL       string
	BaseImageSHA512    string
	NetworkName        string
	NetworkCIDR        string
	SSHUser            string
	SSHKeyPath         string
	VirshCommand       string
	SSHCommand         string
	DockerCommand      string
	SSHPort            int
	VCPUs              int
	MemoryMiB          int
	DiskSizeGiB        int
	IdleCount          int
	MaxInstances       int
	ReadyTimeout       time.Duration
	CleanupTimeout     time.Duration
	RegistryMirrors    []string
	InsecureRegistries []string
}

type vmInstance struct {
	Name         string
	Address      string
	MACAddress   string
	VolumeName   string
	IgnitionPath string
}

// vmProvider owns all hypervisor- and guest-specific operations. Keeping the
// pool state machine behind this small interface makes its concurrency and
// failure behavior testable without libvirt, KVM, or SSH.
type vmProvider interface {
	Prepare(context.Context) error
	Create(context.Context) (vmInstance, error)
	CheckReady(context.Context, vmInstance) error
	Destroy(context.Context, vmInstance) error
	Cleanup(context.Context) error
	Execute(context.Context, vmInstance, ExecuteRequest, io.Writer) error
}

type vmPoolState uint8

const (
	vmIdle vmPoolState = iota
	vmReserved
	vmLeased
	vmDestroying
)

type pooledVM struct {
	instance          vmInstance
	state             vmPoolState
	destroyInProgress bool
}

// LibvirtExecutor maintains a bounded pool of SSH- and Docker-ready virtual
// machines. Reservations are single-use once assigned to a build.
type LibvirtExecutor struct {
	config   LibvirtConfig
	provider vmProvider

	lifecycleMu sync.Mutex
	mu          sync.Mutex
	instances   map[string]*pooledVM
	idle        []string
	creating    int
	started     bool
	stopping    bool
	stopped     bool
	changed     chan struct{}
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	shutdownErr error
	retryDelay  time.Duration
}

// NewLibvirtExecutor validates config and constructs the production libvirt
// provider. Hypervisor access is deferred until Start.
func NewLibvirtExecutor(config LibvirtConfig) (*LibvirtExecutor, error) {
	normalized, err := normalizeLibvirtConfig(config)
	if err != nil {
		return nil, err
	}
	return newLibvirtExecutorWithNormalizedConfig(
		normalized,
		newVirshVMProvider(normalized),
	)
}

// newLibvirtExecutorWithProvider is the dependency-injection constructor used
// by focused pool tests.
func newLibvirtExecutorWithProvider(
	config LibvirtConfig,
	provider vmProvider,
) (*LibvirtExecutor, error) {
	normalized, err := normalizeLibvirtConfig(config)
	if err != nil {
		return nil, err
	}
	return newLibvirtExecutorWithNormalizedConfig(normalized, provider)
}

func newLibvirtExecutorWithNormalizedConfig(
	config LibvirtConfig,
	provider vmProvider,
) (*LibvirtExecutor, error) {
	if provider == nil {
		return nil, errors.New("libvirt VM provider is required")
	}
	return &LibvirtExecutor{
		config:     config,
		provider:   provider,
		instances:  make(map[string]*pooledVM),
		changed:    make(chan struct{}),
		retryDelay: defaultLibvirtRetryDelay,
	}, nil
}

func normalizeLibvirtConfig(config LibvirtConfig) (LibvirtConfig, error) {
	config.RunnerID = defaultString(config.RunnerID, defaultLibvirtRunnerID)
	config.URI = defaultString(config.URI, defaultLibvirtURI)
	config.PoolName = defaultString(config.PoolName, defaultLibvirtPoolName)
	config.PoolPath = defaultString(config.PoolPath, defaultLibvirtPoolPath)
	config.BaseVolumeName = strings.TrimSpace(config.BaseVolumeName)
	var err error
	config.BaseImageURL, config.BaseImageSHA512, err = normalizeFlatcarBaseImageConfig(
		config.BaseImageURL,
		config.BaseImageSHA512,
	)
	if err != nil {
		return LibvirtConfig{}, err
	}
	config.NetworkName = defaultString(config.NetworkName, defaultLibvirtNetworkName)

	config.NetworkCIDR, err = normalizeLibvirtNetworkCIDR(config.NetworkCIDR, config.NetworkName)
	if err != nil {
		return LibvirtConfig{}, err
	}
	config.SSHUser = defaultString(config.SSHUser, defaultLibvirtSSHUser)
	config.SSHKeyPath = strings.TrimSpace(config.SSHKeyPath)
	if config.SSHKeyPath != "" {
		config.SSHKeyPath = filepath.Clean(config.SSHKeyPath)
	}
	config.VirshCommand = defaultString(config.VirshCommand, defaultLibvirtVirshCommand)
	config.SSHCommand = defaultString(config.SSHCommand, defaultLibvirtSSHCommand)
	config.DockerCommand = defaultString(config.DockerCommand, defaultLibvirtDockerCommand)
	if config.SSHPort == 0 {
		config.SSHPort = defaultLibvirtSSHPort
	}
	if config.VCPUs == 0 {
		config.VCPUs = defaultLibvirtVCPUs
	}
	if config.MemoryMiB == 0 {
		config.MemoryMiB = defaultLibvirtMemoryMiB
	}
	if config.DiskSizeGiB == 0 {
		config.DiskSizeGiB = defaultLibvirtDiskSizeGiB
	}
	if config.IdleCount == 0 {
		config.IdleCount = defaultLibvirtIdleCount
	}
	if config.MaxInstances == 0 {
		config.MaxInstances = config.IdleCount
	}
	if config.ReadyTimeout == 0 {
		config.ReadyTimeout = defaultLibvirtReadyTimeout
	}
	if config.CleanupTimeout == 0 {
		config.CleanupTimeout = defaultLibvirtCleanupTimeout
	}
	config.RegistryMirrors, err = normalizeNonBlankValues(
		"libvirt registry mirror",
		config.RegistryMirrors,
	)
	if err != nil {
		return LibvirtConfig{}, err
	}
	config.InsecureRegistries, err = normalizeNonBlankValues(
		"libvirt insecure registry",
		config.InsecureRegistries,
	)
	if err != nil {
		return LibvirtConfig{}, err
	}

	switch {
	case !validRunnerID(config.RunnerID):
		return LibvirtConfig{}, errors.New("valid libvirt runner ID is required")
	case config.BaseVolumeName == "":
		return LibvirtConfig{}, errors.New("libvirt base volume name is required")
	case config.SSHKeyPath == "":
		return LibvirtConfig{}, errors.New("libvirt SSH key path is required")
	case config.SSHPort < 1 || config.SSHPort > 65535:
		return LibvirtConfig{}, errors.New("libvirt SSH port must be between 1 and 65535")
	case config.VCPUs < 1:
		return LibvirtConfig{}, errors.New("libvirt VCPUs must be at least 1")
	case config.MemoryMiB < 256:
		return LibvirtConfig{}, errors.New("libvirt memory must be at least 256 MiB")
	case config.DiskSizeGiB < 1:
		return LibvirtConfig{}, errors.New("libvirt disk size must be at least 1 GiB")
	case config.IdleCount < 0:
		return LibvirtConfig{}, errors.New("libvirt idle count cannot be negative")
	case config.MaxInstances < 1:
		return LibvirtConfig{}, errors.New("libvirt maximum instances must be at least 1")
	case config.MaxInstances > maximumLibvirtInstances:
		return LibvirtConfig{}, fmt.Errorf(
			"libvirt maximum instances cannot exceed %d",
			maximumLibvirtInstances,
		)
	case config.IdleCount > config.MaxInstances:
		return LibvirtConfig{}, errors.New("libvirt idle count cannot exceed maximum instances")
	case config.ReadyTimeout < time.Second:
		return LibvirtConfig{}, errors.New("libvirt ready timeout must be at least 1 second")
	case config.CleanupTimeout < time.Second:
		return LibvirtConfig{}, errors.New("libvirt cleanup timeout must be at least 1 second")
	case config.CleanupTimeout > maximumLibvirtCleanupTimeout:
		return LibvirtConfig{}, fmt.Errorf(
			"libvirt cleanup timeout cannot exceed %s",
			maximumLibvirtCleanupTimeout,
		)
	}
	return config, nil
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}

func normalizeNonBlankValues(name string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]string, len(values))
	for index, value := range values {
		normalized[index] = strings.TrimSpace(value)
		if normalized[index] == "" {
			return nil, fmt.Errorf("%s %d cannot be blank", name, index+1)
		}
	}
	return normalized, nil
}

// Start prepares libvirt and synchronously provisions the configured warm
// capacity. A runner cannot claim work until all initial idle VMs are ready.
func (e *LibvirtExecutor) Start(ctx context.Context) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()

	e.mu.Lock()
	if e.started {
		e.mu.Unlock()
		return nil
	}
	if e.stopping || e.stopped {
		e.mu.Unlock()
		return errors.New("libvirt executor is shut down")
	}
	e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	if err := e.provider.Prepare(ctx); err != nil {
		cleanupErr := e.cleanupProvider(context.Background())
		e.markStartupFailed()
		return errors.Join(fmt.Errorf("prepare libvirt executor: %w", err), cleanupErr)
	}
	if err := ctx.Err(); err != nil {
		cleanupErr := e.cleanupProvider(context.Background())
		e.markStartupFailed()
		return errors.Join(err, cleanupErr)
	}

	created, err := e.preheat(ctx)
	if err != nil {
		cleanupErr := e.cleanupStartup(context.Background(), created)
		e.markStartupFailed()
		return errors.Join(err, cleanupErr)
	}

	e.mu.Lock()
	for _, instance := range created {
		e.instances[instance.Name] = &pooledVM{instance: instance, state: vmIdle}
		e.idle = append(e.idle, instance.Name)
	}
	reconcileContext, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.started = true
	e.signalLocked()
	e.wg.Add(1)
	e.mu.Unlock()
	go e.reconcile(reconcileContext)
	return nil
}

type vmCreateResult struct {
	instance vmInstance
	err      error
}

// preheat launches the complete initial deficit together. Each Create still
// has the caller's deadline, and the first failed attempt cancels its peers so
// startup can roll every returned resource back promptly.
func (e *LibvirtExecutor) preheat(ctx context.Context) ([]vmInstance, error) {
	count := e.config.IdleCount
	if count == 0 {
		return nil, nil
	}

	createContext, cancelCreate := context.WithCancel(ctx)
	defer cancelCreate()
	results := make(chan vmCreateResult, count)

	e.mu.Lock()
	e.creating += count
	e.signalLocked()
	e.mu.Unlock()
	for range count {
		go func() {
			instance, err := e.provider.Create(createContext)
			if err == nil {
				err = validateVMInstance(instance)
			}
			if err == nil {
				err = ctx.Err()
			}
			if err != nil {
				cancelCreate()
			}
			results <- vmCreateResult{instance: instance, err: err}
		}()
	}

	created := make([]vmInstance, 0, count)
	createdNames := make(map[string]struct{}, count)
	var result error
	for range count {
		createResult := <-results
		name := strings.TrimSpace(createResult.instance.Name)
		if name != "" {
			if _, exists := createdNames[name]; exists {
				result = errors.Join(
					result,
					fmt.Errorf("libvirt provider created duplicate VM %q", name),
				)
			} else {
				createdNames[name] = struct{}{}
				created = append(created, createResult.instance)
			}
		}
		if createResult.err != nil {
			result = errors.Join(
				result,
				fmt.Errorf("preheat libvirt VM: %w", createResult.err),
			)
		}
	}

	e.mu.Lock()
	e.creating -= count
	e.signalLocked()
	e.mu.Unlock()
	return created, result
}

func validateVMInstance(instance vmInstance) error {
	if strings.TrimSpace(instance.Name) == "" {
		return errors.New("libvirt provider created a VM without a name")
	}
	if strings.TrimSpace(instance.Address) == "" {
		return fmt.Errorf("libvirt VM %q has no SSH address", instance.Name)
	}
	return nil
}

func (e *LibvirtExecutor) markStartupFailed() {
	e.mu.Lock()
	e.stopping = true
	e.stopped = true
	e.signalLocked()
	e.mu.Unlock()
}

func (e *LibvirtExecutor) cleanupStartup(
	ctx context.Context,
	instances []vmInstance,
) error {
	destroyContext, cancelDestroy := e.newCleanupContext(ctx)
	destroyErrors := make([]error, len(instances))
	var destroyWait sync.WaitGroup
	for index, instance := range instances {
		destroyWait.Add(1)
		go func() {
			defer destroyWait.Done()
			if err := e.provider.Destroy(destroyContext, instance); err != nil {
				destroyErrors[index] = fmt.Errorf(
					"destroy startup VM %q: %w",
					instance.Name,
					err,
				)
			}
		}()
	}
	destroyWait.Wait()
	cancelDestroy()
	var result error
	for _, err := range destroyErrors {
		result = errors.Join(result, err)
	}
	return errors.Join(result, e.cleanupProvider(ctx))
}

func (e *LibvirtExecutor) cleanupProvider(ctx context.Context) error {
	cleanupContext, cancel := e.newCleanupContext(ctx)
	defer cancel()
	if err := e.provider.Cleanup(cleanupContext); err != nil {
		return fmt.Errorf("cleanup libvirt provider: %w", err)
	}
	return nil
}

// Reserve waits for one ready VM and removes it from the idle pool. Merely
// reserving does not trigger a refill: a caller may have polled and found no
// build. Assign or Run marks the reservation single-use and starts refill.
func (e *LibvirtExecutor) Reserve(ctx context.Context) (ExecutorReservation, error) {
reserveLoop:
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		e.mu.Lock()
		if !e.started {
			e.mu.Unlock()
			return nil, errors.New("libvirt executor is not started")
		}
		if e.stopping || e.stopped {
			e.mu.Unlock()
			return nil, errors.New("libvirt executor is shutting down")
		}
		for len(e.idle) > 0 {
			name := e.idle[0]
			e.idle = e.idle[1:]
			instance := e.instances[name]
			if instance == nil || instance.state != vmIdle {
				continue
			}
			instance.state = vmReserved
			value := instance.instance
			e.mu.Unlock()

			healthContext, cancelHealth := context.WithTimeout(
				ctx,
				defaultLibvirtHealthTimeout,
			)
			healthErr := e.provider.CheckReady(healthContext, value)
			cancelHealth()

			e.mu.Lock()
			current := e.instances[name]
			if ctxErr := context.Cause(ctx); ctxErr != nil {
				if current != nil && current.state == vmReserved &&
					!e.stopping && !e.stopped {
					current.state = vmIdle
					e.idle = append(e.idle, name)
					e.signalLocked()
				}
				e.mu.Unlock()
				return nil, ctxErr
			}
			if e.stopping || e.stopped {
				e.mu.Unlock()
				return nil, errors.New("libvirt executor is shutting down")
			}
			if current == nil || current.state != vmReserved {
				e.mu.Unlock()
				return nil, errors.New("libvirt VM reservation is no longer available")
			}
			if healthErr == nil {
				e.mu.Unlock()
				return &libvirtReservation{executor: e, name: name}, nil
			}
			current.state = vmDestroying
			current.destroyInProgress = false
			e.signalLocked()
			e.mu.Unlock()
			log.Printf("libvirt warm VM %q failed its readiness check and will be recycled: %v", name, healthErr)
			continue reserveLoop
		}
		changed := e.changed
		e.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// Run preserves the ordinary Executor contract for callers that do not use
// explicit reservations.
func (e *LibvirtExecutor) Run(
	ctx context.Context,
	request ExecuteRequest,
	output io.Writer,
) error {
	reservation, err := e.Reserve(ctx)
	if err != nil {
		return err
	}
	runErr := reservation.Run(ctx, request, output)
	releaseErr := reservation.Release(ctx)
	return errors.Join(runErr, releaseErr)
}

// ShutdownTimeout covers an in-flight Create rollback, concurrent tracked-VM
// destruction, and provider-wide cleanup.
func (e *LibvirtExecutor) ShutdownTimeout() time.Duration {
	return 3 * e.config.CleanupTimeout
}

type libvirtReservation struct {
	executor *LibvirtExecutor
	name     string

	mu       sync.Mutex
	assigned bool
	ran      bool
	released bool
}

// Assign marks the VM as consumed by a real build. It is intentionally
// idempotent because both Remote and Run may call it defensively.
func (r *libvirtReservation) Assign() {
	r.mu.Lock()
	if r.released || r.assigned {
		r.mu.Unlock()
		return
	}
	r.assigned = true
	r.mu.Unlock()
	r.executor.assignReservation(r.name)
}

func (r *libvirtReservation) Run(
	ctx context.Context,
	request ExecuteRequest,
	output io.Writer,
) error {
	r.Assign()
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return errors.New("libvirt VM reservation is released")
	}
	if r.ran {
		r.mu.Unlock()
		return errors.New("libvirt VM reservation can run only one build")
	}
	r.ran = true
	r.mu.Unlock()

	instance, err := r.executor.leasedInstance(r.name)
	if err != nil {
		return err
	}
	return r.executor.provider.Execute(ctx, instance, request, output)
}

func (r *libvirtReservation) Release(ctx context.Context) error {
	r.mu.Lock()
	if r.released {
		r.mu.Unlock()
		return nil
	}
	r.released = true
	assigned := r.assigned
	r.mu.Unlock()

	if !assigned {
		return r.executor.returnIdle(r.name)
	}
	return r.executor.destroyReleased(ctx, r.name)
}

// ReleaseTimeout bounds the destruction of the single VM held by this
// reservation.
func (r *libvirtReservation) ReleaseTimeout() time.Duration {
	return r.executor.config.CleanupTimeout
}

func (e *LibvirtExecutor) leasedInstance(name string) (vmInstance, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	instance := e.instances[name]
	if instance == nil || instance.state != vmLeased {
		return vmInstance{}, errors.New("libvirt VM reservation is no longer available")
	}
	return instance.instance, nil
}

func (e *LibvirtExecutor) assignReservation(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	instance := e.instances[name]
	if instance == nil || instance.state != vmReserved {
		return
	}
	instance.state = vmLeased
	e.signalLocked()
}

func (e *LibvirtExecutor) returnIdle(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping || e.stopped {
		return nil
	}
	instance := e.instances[name]
	if instance == nil {
		return nil
	}
	if instance.state != vmReserved {
		return errors.New("libvirt VM reservation is already assigned")
	}
	instance.state = vmIdle
	e.idle = append(e.idle, name)
	e.signalLocked()
	return nil
}

func (e *LibvirtExecutor) destroyReleased(ctx context.Context, name string) error {
	e.mu.Lock()
	instance := e.instances[name]
	if instance == nil {
		e.mu.Unlock()
		return nil
	}
	instance.state = vmDestroying
	instance.destroyInProgress = true
	e.removeIdleLocked(name)
	e.signalLocked()
	value := instance.instance
	e.mu.Unlock()

	cleanupContext, cancel := e.newCleanupContext(ctx)
	err := e.provider.Destroy(cleanupContext, value)
	cancel()

	e.mu.Lock()
	current := e.instances[name]
	if current != nil {
		if err == nil {
			delete(e.instances, name)
		} else {
			current.destroyInProgress = false
		}
	}
	e.signalLocked()
	e.mu.Unlock()
	if err != nil {
		return fmt.Errorf("destroy libvirt VM %q: %w", name, err)
	}
	return nil
}

func (e *LibvirtExecutor) signalLocked() {
	close(e.changed)
	e.changed = make(chan struct{})
}

func (e *LibvirtExecutor) removeIdleLocked(name string) {
	for index, idleName := range e.idle {
		if idleName == name {
			e.idle = append(e.idle[:index], e.idle[index+1:]...)
			return
		}
	}
}

func (e *LibvirtExecutor) reconcile(ctx context.Context) {
	defer e.wg.Done()
	for {
		action, instance, changed := e.nextReconcileAction()
		switch action {
		case reconcileStop:
			return
		case reconcileWait:
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
		case reconcileCreate:
			e.wg.Add(1)
			go e.runReconcileCreate(ctx)
		case reconcileDestroy:
			if err := e.reconcileDestroy(instance); err != nil {
				log.Printf("libvirt warm-pool cleanup failed: %v", err)
				if !waitLibvirtRetry(ctx, e.retryDelay) {
					return
				}
			}
		}
	}
}

type reconcileAction uint8

const (
	reconcileWait reconcileAction = iota
	reconcileCreate
	reconcileDestroy
	reconcileStop
)

func (e *LibvirtExecutor) nextReconcileAction() (
	reconcileAction,
	vmInstance,
	<-chan struct{},
) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.started || e.stopping || e.stopped {
		return reconcileStop, vmInstance{}, nil
	}
	if e.availableCapacityLocked()+e.creating < e.config.IdleCount &&
		len(e.instances)+e.creating < e.config.MaxInstances {
		e.creating++
		e.signalLocked()
		return reconcileCreate, vmInstance{}, nil
	}
	for _, pooled := range e.instances {
		if pooled.state == vmDestroying && !pooled.destroyInProgress {
			pooled.destroyInProgress = true
			return reconcileDestroy, pooled.instance, nil
		}
	}
	return reconcileWait, vmInstance{}, e.changed
}

func (e *LibvirtExecutor) availableCapacityLocked() int {
	available := len(e.idle)
	for _, pooled := range e.instances {
		if pooled.state == vmReserved {
			available++
		}
	}
	return available
}

func (e *LibvirtExecutor) reconcileCreate(ctx context.Context) (string, error) {
	instance, err := e.provider.Create(ctx)
	returnedInstance := strings.TrimSpace(instance.Name) != ""
	if err == nil {
		err = validateVMInstance(instance)
	}

	e.mu.Lock()
	alreadyTracked := false
	if returnedInstance {
		_, alreadyTracked = e.instances[instance.Name]
	}
	if err == nil {
		if alreadyTracked {
			err = fmt.Errorf("libvirt provider created duplicate VM %q", instance.Name)
		} else {
			e.instances[instance.Name] = &pooledVM{instance: instance, state: vmIdle}
			e.idle = append(e.idle, instance.Name)
		}
	}
	e.signalLocked()
	e.mu.Unlock()
	if err != nil {
		// A duplicate name refers to the VM that is already tracked by the
		// pool. Destroying it here could terminate an active build; libvirt
		// cannot have two domains or volumes with the same identity anyway.
		if returnedInstance && !alreadyTracked {
			if ctx.Err() != nil {
				e.retainFailedCleanup(instance)
				return instance.Name, err
			}
			cleanupErr := e.destroyUntracked(instance)
			if cleanupErr != nil {
				e.retainFailedCleanup(instance)
				return instance.Name, errors.Join(err, cleanupErr)
			}
			err = errors.Join(err, cleanupErr)
		}
		return "", err
	}
	return "", nil
}

func (e *LibvirtExecutor) runReconcileCreate(ctx context.Context) {
	defer e.wg.Done()
	retryDestroyName, err := e.reconcileCreate(ctx)
	if retryDestroyName != "" {
		// The partial VM now occupies this capacity slot in instances. Stop
		// counting the same physical resource as an in-flight Create while its
		// destruction observes the retry delay below.
		e.mu.Lock()
		e.creating--
		e.signalLocked()
		e.mu.Unlock()
	}
	if err != nil {
		log.Printf("libvirt warm-pool provisioning failed: %v", err)
		_ = waitLibvirtRetry(ctx, e.retryDelay)
	}

	e.mu.Lock()
	if retryDestroyName == "" {
		e.creating--
	}
	if retryDestroyName != "" {
		if instance := e.instances[retryDestroyName]; instance != nil && instance.state == vmDestroying {
			instance.destroyInProgress = false
		}
	}
	e.signalLocked()
	e.mu.Unlock()
}

func (e *LibvirtExecutor) retainFailedCleanup(instance vmInstance) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.instances[instance.Name]; exists {
		return
	}
	e.instances[instance.Name] = &pooledVM{
		instance:          instance,
		state:             vmDestroying,
		destroyInProgress: true,
	}
	e.signalLocked()
}

func (e *LibvirtExecutor) destroyUntracked(instance vmInstance) error {
	cleanupContext, cancel := e.newCleanupContext(context.Background())
	defer cancel()
	if err := e.provider.Destroy(cleanupContext, instance); err != nil {
		return fmt.Errorf("destroy invalid libvirt VM %q: %w", instance.Name, err)
	}
	return nil
}

func (e *LibvirtExecutor) reconcileDestroy(instance vmInstance) error {
	cleanupContext, cancel := e.newCleanupContext(context.Background())
	err := e.provider.Destroy(cleanupContext, instance)
	cancel()

	e.mu.Lock()
	current := e.instances[instance.Name]
	if current != nil {
		if err == nil {
			delete(e.instances, instance.Name)
			e.removeIdleLocked(instance.Name)
		} else {
			current.destroyInProgress = false
		}
	}
	e.signalLocked()
	e.mu.Unlock()
	if err != nil {
		return fmt.Errorf("destroy libvirt VM %q: %w", instance.Name, err)
	}
	return nil
}

func waitLibvirtRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Shutdown stops background provisioning, destroys every VM known to the
// pool, and asks the provider to remove any remaining managed resources.
func (e *LibvirtExecutor) Shutdown(ctx context.Context) error {
	e.lifecycleMu.Lock()
	defer e.lifecycleMu.Unlock()

	e.mu.Lock()
	if e.stopped {
		err := e.shutdownErr
		e.mu.Unlock()
		return err
	}
	e.stopping = true
	if e.cancel != nil {
		e.cancel()
	}
	e.signalLocked()
	e.mu.Unlock()
	reconcileDone := make(chan struct{})
	go func() {
		e.wg.Wait()
		close(reconcileDone)
	}()
	select {
	case <-reconcileDone:
	case <-ctx.Done():
		return fmt.Errorf(
			"wait for libvirt provisioning to stop: %w",
			context.Cause(ctx),
		)
	}

	e.mu.Lock()
	instances := make([]vmInstance, 0, len(e.instances))
	for _, pooled := range e.instances {
		instances = append(instances, pooled.instance)
	}
	e.mu.Unlock()

	destroyContext, cancelDestroy := e.newCleanupContext(ctx)
	destroyErrors := make([]error, len(instances))
	var destroyWait sync.WaitGroup
	for index, instance := range instances {
		destroyWait.Add(1)
		go func() {
			defer destroyWait.Done()
			if err := e.provider.Destroy(destroyContext, instance); err != nil {
				destroyErrors[index] = fmt.Errorf(
					"destroy libvirt VM %q during shutdown: %w",
					instance.Name,
					err,
				)
			}
		}()
	}
	destroyWait.Wait()
	cancelDestroy()
	var result error
	for _, err := range destroyErrors {
		result = errors.Join(result, err)
	}
	result = errors.Join(result, e.cleanupProvider(ctx))

	e.mu.Lock()
	e.instances = make(map[string]*pooledVM)
	e.idle = nil
	e.creating = 0
	e.started = false
	e.stopped = true
	e.shutdownErr = result
	e.signalLocked()
	e.mu.Unlock()
	return result
}

func (e *LibvirtExecutor) newCleanupContext(parent context.Context) (
	context.Context,
	context.CancelFunc,
) {
	cleanupDeadline := time.Now().Add(e.config.CleanupTimeout)
	if parentDeadline, ok := parent.Deadline(); ok {
		if parentDeadline.Before(cleanupDeadline) {
			cleanupDeadline = parentDeadline
		}
	}
	return context.WithDeadline(context.Background(), cleanupDeadline)
}

var (
	_ Executor                        = (*LibvirtExecutor)(nil)
	_ ExecutorLifecycle               = (*LibvirtExecutor)(nil)
	_ ExecutorShutdownTimeoutProvider = (*LibvirtExecutor)(nil)
	_ ReservingExecutor               = (*LibvirtExecutor)(nil)
	_ ExecutorReservation             = (*libvirtReservation)(nil)
	_ AssignableExecutorReservation   = (*libvirtReservation)(nil)
	_ ExecutorReleaseTimeoutProvider  = (*libvirtReservation)(nil)
)
