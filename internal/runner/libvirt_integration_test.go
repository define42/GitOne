package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/repoconfig"
)

// TestLibvirtExecutorWithKVM is intentionally opt-in because it creates real
// domains and needs a writable libvirt directory pool, NAT, and /dev/kvm. The
// pinned Flatcar image is downloaded automatically when it is absent.
func TestLibvirtExecutorWithKVM(t *testing.T) {
	if os.Getenv("GITONE_RUNNER_LIBVIRT_TEST") != "1" {
		t.Skip("set GITONE_RUNNER_LIBVIRT_TEST=1 on a dedicated KVM host")
	}
	baseVolume := os.Getenv("GITONE_RUNNER_LIBVIRT_BASE_VOLUME")
	if baseVolume == "" {
		baseVolume = "flatcar_production_qemu_image.img"
	}
	poolName := libvirtEnvOrDefault(os.Getenv("GITONE_RUNNER_LIBVIRT_POOL_NAME"), "default")
	poolPath := libvirtEnvOrDefault(
		os.Getenv("GITONE_RUNNER_LIBVIRT_POOL_PATH"),
		"/var/lib/libvirt/images",
	)
	uri := libvirtEnvOrDefault(os.Getenv("GITONE_RUNNER_LIBVIRT_URI"), "qemu:///system")

	executor, err := NewLibvirtExecutor(LibvirtConfig{
		RunnerID:       fmt.Sprintf("integration-%d", time.Now().UnixNano()),
		URI:            uri,
		PoolName:       poolName,
		PoolPath:       poolPath,
		BaseVolumeName: baseVolume,
		NetworkName:    "gitone-runner-integration",
		SSHUser:        "core",
		IdleCount:      1,
		MaxInstances:   2,
		ReadyTimeout:   10 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	startContext, cancelStart := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancelStart()
	if err = executor.Start(startContext); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancelShutdown := context.WithTimeout(
			context.Background(),
			2*time.Minute,
		)
		defer cancelShutdown()
		if shutdownErr := executor.Shutdown(shutdownContext); shutdownErr != nil {
			t.Errorf("shutdown libvirt executor: %v", shutdownErr)
		}
	})

	workspace := t.TempDir()
	if err = os.WriteFile(
		filepath.Join(workspace, "source.txt"),
		[]byte("source from isolated KVM\n"),
		0o640,
	); err != nil {
		t.Fatal(err)
	}
	request := ExecuteRequest{
		Job: Job{
			ID:         "libvirt-integration",
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
	buildContext, cancelBuild := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancelBuild()
	var output bytes.Buffer
	if err = executor.Run(buildContext, request, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "source from isolated KVM\n") {
		t.Fatalf("unexpected KVM build output: %q", output.String())
	}
}

func libvirtEnvOrDefault(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return strings.TrimSpace(value)
}
