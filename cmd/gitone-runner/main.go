package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/define42/GitOne/internal/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	err := run(ctx, os.Args[1:])
	stop()
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil && !isOnlyContextTermination(err) {
		log.Fatal(err)
	}
}

func isOnlyContextTermination(err error) bool {
	if err == nil {
		return false
	}
	type multiUnwrapper interface {
		Unwrap() []error
	}
	if joined, ok := err.(multiUnwrapper); ok {
		children := joined.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !isOnlyContextTermination(child) {
				return false
			}
		}
		return true
	}
	type unwrapper interface {
		Unwrap() error
	}
	if wrapped, ok := err.(unwrapper); ok {
		return isOnlyContextTermination(wrapped.Unwrap())
	}
	return errors.Is(err, context.Canceled)
}

func run(ctx context.Context, args []string) error {
	remote, runnerID, serverURL, err := newRemoteRunner(args)
	if err != nil {
		return err
	}
	log.Printf("GitOne runner %s polling %s", runnerID, serverURL)
	return remote.Run(ctx)
}

func newRemoteRunner(args []string) (*runner.Remote, string, string, error) {
	flags := flag.NewFlagSet("gitone-runner", flag.ContinueOnError)
	serverURL := flags.String("runner-url", "http://gitone:8080", "GitOne server URL")
	token := flags.String(
		"runner-token",
		os.Getenv("GITONE_RUNNER_TOKEN"),
		"shared remote runner token",
	)
	runnerID := flags.String("runner-id", "gitone-runner", "unique runner ID")
	executorName := flags.String(
		"runner-executor",
		"libvirt",
		"build executor: libvirt or docker (legacy)",
	)
	command := flags.String(
		"runner-command",
		"docker",
		"Docker-compatible command inside the VM (or host for legacy executor)",
	)
	workers := flags.Int("runner-workers", 1, "number of concurrent builds")
	workRoot := flags.String(
		"runner-work-root",
		"/var/lib/gitone-runner",
		"temporary source workspace root",
	)
	pollInterval := flags.Duration("runner-poll-interval", 2*time.Second, "idle polling interval")
	libvirtURI := flags.String("libvirt-uri", "qemu:///system", "libvirt connection URI")
	libvirtPoolName := flags.String("libvirt-pool-name", "default", "libvirt storage pool")
	libvirtPoolPath := flags.String(
		"libvirt-pool-path",
		"/var/lib/libvirt/images",
		"host-visible storage pool path for Ignition files",
	)
	libvirtBaseVolume := flags.String(
		"libvirt-base-volume",
		"flatcar_production_qemu_image.img",
		"immutable Flatcar qcow2 base volume name",
	)
	libvirtBaseImageURL := flags.String(
		"libvirt-base-image-url",
		runner.DefaultFlatcarBaseImageURL,
		"pinned Flatcar qcow2 download URL used when the base volume is missing",
	)
	libvirtBaseImageSHA512 := flags.String(
		"libvirt-base-image-sha512",
		runner.DefaultFlatcarBaseImageSHA512,
		"expected SHA-512 for the Flatcar base image",
	)
	libvirtNetwork := flags.String(
		"libvirt-network",
		"gitone-runner",
		"dedicated libvirt NAT network",
	)
	libvirtNetworkCIDR := flags.String(
		"libvirt-network-cidr",
		"",
		"dedicated IPv4 /20 (deterministic default; set to avoid host route conflicts)",
	)
	libvirtSSHUser := flags.String("libvirt-ssh-user", "core", "VM SSH user")
	libvirtSSHPort := flags.Int("libvirt-ssh-port", 22, "VM SSH port")
	libvirtVCPUs := flags.Int("libvirt-vcpus", 2, "vCPUs per VM")
	libvirtMemoryMiB := flags.Int("libvirt-memory-mib", 4096, "memory per VM in MiB")
	libvirtDiskSizeGiB := flags.Int("libvirt-disk-size-gib", 20, "overlay disk size in GiB")
	libvirtIdleCount := flags.Int(
		"libvirt-idle-count",
		runner.DefaultLibvirtIdleCount,
		"number of pre-heated Docker-ready VMs",
	)
	libvirtMaxInstances := flags.Int(
		"libvirt-max-instances",
		runner.DefaultLibvirtMaxInstances,
		"maximum creating, idle, and assigned VMs",
	)
	libvirtReadyTimeout := flags.Duration(
		"libvirt-ready-timeout",
		10*time.Minute,
		"maximum time for a VM to reach SSH and Docker readiness",
	)
	libvirtCleanupTimeout := flags.Duration(
		"libvirt-cleanup-timeout",
		30*time.Second,
		"maximum time for one VM cleanup",
	)
	libvirtRegistryMirrors := flags.String(
		"libvirt-registry-mirrors",
		"",
		"comma-separated Docker registry mirror URLs installed in each VM",
	)
	libvirtInsecureRegistries := flags.String(
		"libvirt-insecure-registries",
		"",
		"comma-separated insecure Docker registry hosts installed in each VM",
	)
	virshCommand := flags.String("libvirt-virsh-command", "virsh", "virsh command")
	if err := flags.Parse(args); err != nil {
		return nil, "", "", err
	}
	if flags.NArg() != 0 {
		return nil, "", "", fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	if *workers < 1 || *workers > 32 {
		return nil, "", "", errors.New("runner workers must be between 1 and 32")
	}

	var (
		executor runner.Executor
		err      error
	)
	switch strings.ToLower(strings.TrimSpace(*executorName)) {
	case "libvirt":
		idleCount := *libvirtIdleCount
		if idleCount == 0 {
			idleCount = runner.DefaultLibvirtIdleCount
		}
		maxInstances := *libvirtMaxInstances
		if maxInstances == 0 {
			maxInstances = runner.DefaultLibvirtMaxInstances
		}
		executor, err = runner.NewLibvirtExecutor(runner.LibvirtConfig{
			RunnerID:           *runnerID,
			URI:                *libvirtURI,
			PoolName:           *libvirtPoolName,
			PoolPath:           *libvirtPoolPath,
			BaseVolumeName:     *libvirtBaseVolume,
			BaseImageURL:       *libvirtBaseImageURL,
			BaseImageSHA512:    *libvirtBaseImageSHA512,
			NetworkName:        *libvirtNetwork,
			NetworkCIDR:        *libvirtNetworkCIDR,
			SSHUser:            *libvirtSSHUser,
			SSHPort:            *libvirtSSHPort,
			VCPUs:              *libvirtVCPUs,
			MemoryMiB:          *libvirtMemoryMiB,
			DiskSizeGiB:        *libvirtDiskSizeGiB,
			IdleCount:          idleCount,
			MaxInstances:       maxInstances,
			ReadyTimeout:       *libvirtReadyTimeout,
			CleanupTimeout:     *libvirtCleanupTimeout,
			RegistryMirrors:    commaSeparatedValues(*libvirtRegistryMirrors),
			InsecureRegistries: commaSeparatedValues(*libvirtInsecureRegistries),
			VirshCommand:       *virshCommand,
			DockerCommand:      *command,
		})
		if err != nil {
			return nil, "", "", err
		}
	case "docker":
		executor = runner.ContainerExecutor{Command: *command}
	default:
		return nil, "", "", fmt.Errorf(
			"runner executor must be libvirt or docker, got %q",
			*executorName,
		)
	}
	remote, err := runner.NewRemote(runner.RemoteConfig{
		URL:          *serverURL,
		Token:        *token,
		ID:           *runnerID,
		WorkRoot:     *workRoot,
		Workers:      *workers,
		PollInterval: *pollInterval,
		Executor:     executor,
	})
	if err != nil {
		return nil, "", "", err
	}
	return remote, *runnerID, *serverURL, nil
}

func commaSeparatedValues(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	values := strings.Split(value, ",")
	for index := range values {
		values[index] = strings.TrimSpace(values[index])
	}
	return values
}
