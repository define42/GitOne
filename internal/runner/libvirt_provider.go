package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const (
	libvirtInstanceTimestampLayout = "20060102150405"
	libvirtInstanceSuffixBytes     = 3
	libvirtMaximumNameAttempts     = 16
)

var (
	libvirtResourceNamePattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	libvirtRemoteCommandPattern      = regexp.MustCompile(`^[A-Za-z0-9_./+-]+$`)
	libvirtSSHUserPattern            = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	libvirtMissingDomainErrorPattern = regexp.MustCompile(
		`(?i)^(error:[ \t]*)?failed to get domain '[^'\r\n]+'$`,
	)
)

type virshVMProvider struct {
	config      LibvirtConfig
	runner      libvirtCommandRunner
	httpClient  *http.Client
	ownerPrefix string

	mu        sync.Mutex
	prepared  bool
	publicKey string
	lockFile  *os.File
	guestSSH  libvirtGuestSSH

	identityMu            sync.Mutex
	allocatedNames        map[string]string
	allocatedMACAddresses map[string]string
}

type libvirtOwnedDomainDescription struct {
	Description string `xml:"description"`
	Devices     struct {
		Disks []struct {
			Device string `xml:"device,attr"`
			Source struct {
				File string `xml:"file,attr"`
			} `xml:"source"`
		} `xml:"disk"`
	} `xml:"devices"`
}

func newVirshVMProvider(config LibvirtConfig) vmProvider {
	ownerPrefix := libvirtOwnerPrefix(config.URI, config.PoolName, config.RunnerID)
	return &virshVMProvider{
		config:                config,
		runner:                systemLibvirtCommandRunner{},
		httpClient:            newFlatcarHTTPClient(),
		ownerPrefix:           ownerPrefix,
		allocatedNames:        make(map[string]string),
		allocatedMACAddresses: make(map[string]string),
	}
}

func libvirtOwnerPrefix(uri string, poolName string, runnerID string) string {
	normalized := strings.ToLower(strings.TrimSpace(runnerID))
	var slug strings.Builder
	for _, character := range normalized {
		switch {
		case character >= 'a' && character <= 'z':
			slug.WriteRune(character)
		case character >= '0' && character <= '9':
			slug.WriteRune(character)
		default:
			if slug.Len() > 0 && !strings.HasSuffix(slug.String(), "-") {
				slug.WriteByte('-')
			}
		}
		if slug.Len() >= 18 {
			break
		}
	}
	trimmed := strings.Trim(slug.String(), "-")
	if trimmed == "" {
		trimmed = "runner"
	}
	digest := sha256.Sum256([]byte(uri + "\x00" + poolName + "\x00" + runnerID))
	return fmt.Sprintf("gitone-%s-%x", trimmed, digest[:8])
}

func (p *virshVMProvider) Prepare(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prepared {
		return nil
	}
	if err := p.validateRuntimeConfig(); err != nil {
		return err
	}

	virshPath, err := p.runner.LookPath(p.config.VirshCommand)
	if err != nil {
		return fmt.Errorf("find virsh command %q: %w", p.config.VirshCommand, err)
	}
	p.config.VirshCommand = virshPath
	if p.guestSSH == nil {
		p.guestSSH, err = newNativeLibvirtSSH(p.config.SSHUser, p.config.SSHPort)
		if err != nil {
			return err
		}
	}
	p.publicKey = p.guestSSH.AuthorizedKey()

	if err = p.acquireLock(); err != nil {
		return err
	}
	prepared := false
	defer func() {
		if prepared {
			return
		}
		_ = p.releaseLock()
	}()

	if err = p.validateKVM(ctx); err != nil {
		return err
	}
	if err = p.validateStoragePool(ctx); err != nil {
		return err
	}
	if err = p.cleanupOwnedResources(ctx); err != nil {
		return fmt.Errorf("clean stale libvirt runner resources: %w", err)
	}
	if err = p.validateBaseStorage(ctx); err != nil {
		return err
	}
	if err = p.ensureNetwork(ctx); err != nil {
		return err
	}
	p.prepared = true
	prepared = true
	return nil
}

func (p *virshVMProvider) validateRuntimeConfig() error {
	switch {
	case !validRunnerID(p.config.RunnerID):
		return errors.New("valid libvirt runner ID is required")
	case !validLibvirtResourceName(p.config.PoolName):
		return errors.New("invalid libvirt storage pool name")
	case !validLibvirtResourceName(p.config.BaseVolumeName):
		return errors.New("invalid libvirt base volume name")
	case !validLibvirtResourceName(p.config.NetworkName):
		return errors.New("invalid libvirt network name")
	case !libvirtSSHUserPattern.MatchString(p.config.SSHUser):
		return errors.New("invalid libvirt SSH user")
	case !validRemoteExecutable(p.config.DockerCommand):
		return errors.New("invalid guest Docker command")
	case strings.TrimSpace(p.config.URI) == "" || containsControlCharacter(p.config.URI):
		return errors.New("invalid libvirt URI")
	case libvirtURIUsesExternalSSH(p.config.URI):
		return errors.New("libvirt +ssh transport is unsupported; use a local libvirt socket")
	case !filepath.IsAbs(p.config.PoolPath):
		return errors.New("libvirt storage pool path must be absolute")
	case strings.ContainsAny(p.config.PoolPath, ",\r\n\x00"):
		return errors.New("libvirt storage pool path contains unsupported characters")
	}
	if err := validateFlatcarPoolDirectory(p.config.PoolPath); err != nil {
		return err
	}
	return nil
}

func validLibvirtResourceName(name string) bool {
	return len(name) <= 120 && libvirtResourceNamePattern.MatchString(name)
}

func validRemoteExecutable(command string) bool {
	return command != "" &&
		len(command) <= 255 &&
		!strings.Contains(command, "..") &&
		libvirtRemoteCommandPattern.MatchString(command)
}

func containsControlCharacter(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func (p *virshVMProvider) acquireLock() error {
	digest := sha256.Sum256([]byte(p.config.URI + "\x00" + p.config.PoolName + "\x00" + p.config.RunnerID))
	path := filepath.Join(p.config.PoolPath, fmt.Sprintf(".gitone-provider-%x.lock", digest[:8]))
	lockFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("open libvirt provider lock: %w", err)
	}
	if err = unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lockFile.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return fmt.Errorf("libvirt runner %q is already active", p.config.RunnerID)
		}
		return fmt.Errorf("lock libvirt runner: %w", err)
	}
	p.lockFile = lockFile
	return nil
}

func (p *virshVMProvider) acquireNetworkLock(ctx context.Context) (*os.File, error) {
	digest := sha256.Sum256([]byte(p.config.URI + "\x00" + p.config.NetworkName))
	path := filepath.Join(p.config.PoolPath, fmt.Sprintf(".gitone-network-%x.lock", digest[:8]))
	lockFile, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open libvirt network lock: %w", err)
	}
	for {
		err = unix.Flock(int(lockFile.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return lockFile, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = lockFile.Close()
			return nil, fmt.Errorf("lock libvirt network: %w", err)
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = lockFile.Close()
			return nil, fmt.Errorf("wait for libvirt network lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func releaseLibvirtFileLock(lockFile *os.File) error {
	if lockFile == nil {
		return nil
	}
	err := unix.Flock(int(lockFile.Fd()), unix.LOCK_UN)
	return errors.Join(err, lockFile.Close())
}

func (p *virshVMProvider) releaseLock() error {
	if p.lockFile == nil {
		return nil
	}
	err := unix.Flock(int(p.lockFile.Fd()), unix.LOCK_UN)
	err = errors.Join(err, p.lockFile.Close())
	p.lockFile = nil
	return err
}

func (p *virshVMProvider) virsh(ctx context.Context, arguments ...string) (string, error) {
	args := make([]string, 0, len(arguments)+2)
	args = append(args, "--connect", p.config.URI)
	args = append(args, arguments...)
	return runLibvirtCommandCapture(ctx, p.runner, p.config.VirshCommand, args...)
}

func (p *virshVMProvider) validateKVM(ctx context.Context) error {
	capabilities, err := p.virsh(ctx, "domcapabilities", "--virttype", "kvm")
	if err != nil {
		return fmt.Errorf("KVM domain capabilities are required: %w", err)
	}
	compact := strings.ReplaceAll(strings.ReplaceAll(capabilities, " ", ""), "\n", "")
	if !strings.Contains(compact, "<domain>kvm</domain>") {
		return errors.New("libvirt did not report KVM domain capabilities")
	}
	return nil
}

type libvirtPoolDescription struct {
	Type   string `xml:"type,attr"`
	Name   string `xml:"name"`
	Target struct {
		Path string `xml:"path"`
	} `xml:"target"`
}

type libvirtVolumeDescription struct {
	Name     string `xml:"name"`
	Capacity string `xml:"capacity"`
	Target   struct {
		Format struct {
			Type string `xml:"type,attr"`
		} `xml:"format"`
	} `xml:"target"`
}

func (p *virshVMProvider) validateStoragePool(ctx context.Context) error {
	poolInfo, err := p.virsh(ctx, "pool-info", p.config.PoolName)
	if err != nil {
		return fmt.Errorf("inspect libvirt storage pool: %w", err)
	}
	if !virshInfoBoolean(poolInfo, "state") {
		if _, err = p.virsh(ctx, "pool-start", p.config.PoolName); err != nil {
			// Pool ownership is shared by runners, while the provider lock is
			// intentionally runner-specific. Accept only a simultaneous start
			// that can be confirmed by a fresh pool-info request.
			recheck, recheckErr := p.virsh(ctx, "pool-info", p.config.PoolName)
			if recheckErr != nil || !virshInfoBoolean(recheck, "state") {
				return errors.Join(fmt.Errorf("start libvirt storage pool: %w", err), recheckErr)
			}
		}
	}
	poolXML, err := p.virsh(ctx, "pool-dumpxml", p.config.PoolName)
	if err != nil {
		return fmt.Errorf("read libvirt storage pool XML: %w", err)
	}
	var pool libvirtPoolDescription
	if err = xml.Unmarshal([]byte(poolXML), &pool); err != nil {
		return fmt.Errorf("parse libvirt storage pool XML: %w", err)
	}
	if pool.Name != p.config.PoolName || !strings.EqualFold(pool.Type, "dir") {
		return fmt.Errorf(
			"libvirt storage pool %q must be a directory pool (got name %q, type %q)",
			p.config.PoolName,
			pool.Name,
			pool.Type,
		)
	}
	if filepath.Clean(pool.Target.Path) != filepath.Clean(p.config.PoolPath) {
		return fmt.Errorf(
			"libvirt storage pool %q targets %q instead of %q",
			p.config.PoolName,
			pool.Target.Path,
			p.config.PoolPath,
		)
	}
	return nil
}

func (p *virshVMProvider) validateBaseStorage(ctx context.Context) error {
	if err := p.ensureFlatcarBaseImage(ctx); err != nil {
		return fmt.Errorf("ensure Flatcar base image: %w", err)
	}

	volumeXML, err := p.virsh(
		ctx,
		"vol-dumpxml",
		p.config.BaseVolumeName,
		"--pool",
		p.config.PoolName,
	)
	if err != nil {
		return fmt.Errorf("inspect base libvirt volume: %w", err)
	}
	var volume libvirtVolumeDescription
	if err = xml.Unmarshal([]byte(volumeXML), &volume); err != nil {
		return fmt.Errorf("parse base libvirt volume XML: %w", err)
	}
	if volume.Name != p.config.BaseVolumeName || !strings.EqualFold(volume.Target.Format.Type, "qcow2") {
		return fmt.Errorf("base libvirt volume %q must use qcow2", p.config.BaseVolumeName)
	}
	capacity, err := strconv.ParseUint(strings.TrimSpace(volume.Capacity), 10, 64)
	if err != nil {
		return fmt.Errorf("parse base libvirt volume capacity: %w", err)
	}
	requestedCapacity := uint64(p.config.DiskSizeGiB) << 30
	if requestedCapacity < capacity {
		return fmt.Errorf(
			"libvirt disk size %d GiB is smaller than base volume capacity %d bytes",
			p.config.DiskSizeGiB,
			capacity,
		)
	}
	basePath, err := p.virsh(
		ctx,
		"vol-path",
		p.config.BaseVolumeName,
		"--pool",
		p.config.PoolName,
	)
	if err != nil {
		return fmt.Errorf("resolve base libvirt volume path: %w", err)
	}
	if !pathWithinDirectory(p.config.PoolPath, basePath) {
		return fmt.Errorf("base libvirt volume path %q is outside storage pool", basePath)
	}
	baseInfo, err := os.Lstat(basePath)
	if err != nil {
		return fmt.Errorf("inspect base libvirt volume file: %w", err)
	}
	if !baseInfo.Mode().IsRegular() {
		return errors.New("base libvirt volume must be a regular file")
	}
	return nil
}

func virshInfoBoolean(output string, field string) bool {
	field = strings.ToLower(strings.TrimSpace(field))
	for _, line := range strings.Split(output, "\n") {
		name, value, found := strings.Cut(line, ":")
		if !found || strings.ToLower(strings.TrimSpace(name)) != field {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "yes", "true", "active", "running":
			return true
		}
	}
	return false
}

func pathWithinDirectory(directory string, candidate string) bool {
	relative, err := filepath.Rel(filepath.Clean(directory), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

type libvirtNetworkDescription struct {
	Name   string `xml:"name"`
	Bridge struct {
		Name string `xml:"name,attr"`
	} `xml:"bridge"`
	Forward struct {
		Mode string `xml:"mode,attr"`
	} `xml:"forward"`
	Port struct {
		Isolated string `xml:"isolated,attr"`
	} `xml:"port"`
	IP struct {
		Address string `xml:"address,attr"`
		Netmask string `xml:"netmask,attr"`
		DHCP    struct {
			Range struct {
				Start string `xml:"start,attr"`
				End   string `xml:"end,attr"`
				Lease struct {
					Expiry int    `xml:"expiry,attr"`
					Unit   string `xml:"unit,attr"`
				} `xml:"lease"`
			} `xml:"range"`
		} `xml:"dhcp"`
	} `xml:"ip"`
}

func (p *virshVMProvider) ensureNetwork(ctx context.Context) (err error) {
	lockFile, err := p.acquireNetworkLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, releaseLibvirtFileLock(lockFile))
	}()

	networkNames, err := p.virsh(ctx, "net-list", "--all", "--name")
	if err != nil {
		return fmt.Errorf("list libvirt networks: %w", err)
	}
	found := false
	for _, name := range parseVirshNameLines(networkNames) {
		if name == p.config.NetworkName {
			found = true
			break
		}
	}
	if !found {
		networkXML, renderErr := renderLibvirtNetworkXMLForCIDR(
			p.config.NetworkName,
			p.config.NetworkCIDR,
		)
		if renderErr != nil {
			return renderErr
		}
		xmlPath, writeErr := writeLibvirtTemporaryFile(p.config.PoolPath, ".gitone-network-*.xml", networkXML, 0o600)
		if writeErr != nil {
			return fmt.Errorf("write libvirt network XML: %w", writeErr)
		}
		defer func() {
			_ = os.Remove(xmlPath)
		}()
		if _, err = p.virsh(ctx, "net-define", xmlPath); err != nil {
			// A provider using another storage pool cannot share this lock file.
			// Accept a simultaneous successful definition, but no other error.
			recheck, recheckErr := p.virsh(ctx, "net-list", "--all", "--name")
			if recheckErr != nil || !containsVirshName(recheck, p.config.NetworkName) {
				return errors.Join(fmt.Errorf("define libvirt network: %w", err), recheckErr)
			}
		}
	}

	networkXML, err := p.virsh(ctx, "net-dumpxml", p.config.NetworkName)
	if err != nil {
		return fmt.Errorf("read libvirt network XML: %w", err)
	}
	var network libvirtNetworkDescription
	if err = xml.Unmarshal([]byte(networkXML), &network); err != nil {
		return fmt.Errorf("parse libvirt network XML: %w", err)
	}
	expected, err := libvirtNetworkTemplateForCIDR(
		p.config.NetworkName,
		p.config.NetworkCIDR,
	)
	if err != nil {
		return err
	}
	if network.Name != expected.Name ||
		network.Forward.Mode != "nat" ||
		network.Port.Isolated != "yes" ||
		network.Bridge.Name != expected.BridgeName ||
		network.IP.Address != expected.Gateway ||
		network.IP.Netmask != expected.Netmask ||
		network.IP.DHCP.Range.Start != expected.DHCPStart ||
		network.IP.DHCP.Range.End != expected.DHCPEnd ||
		network.IP.DHCP.Range.Lease.Expiry != expected.LeaseMinutes ||
		network.IP.DHCP.Range.Lease.Unit != "minutes" {
		return fmt.Errorf("libvirt network %q must be a dedicated NAT network", p.config.NetworkName)
	}
	networkInfo, err := p.virsh(ctx, "net-info", p.config.NetworkName)
	if err != nil {
		return fmt.Errorf("inspect libvirt network: %w", err)
	}
	if !virshInfoBoolean(networkInfo, "active") {
		if _, err = p.virsh(ctx, "net-start", p.config.NetworkName); err != nil {
			// Providers using different storage pools cannot share the pool-local
			// lock file. Accept only the narrow race where another provider made
			// this exact network active between net-info and net-start.
			recheck, recheckErr := p.virsh(ctx, "net-info", p.config.NetworkName)
			if recheckErr != nil || !virshInfoBoolean(recheck, "active") {
				return errors.Join(fmt.Errorf("start libvirt network: %w", err), recheckErr)
			}
		}
	}
	if _, err = p.virsh(ctx, "net-autostart", p.config.NetworkName); err != nil {
		return fmt.Errorf("enable libvirt network autostart: %w", err)
	}
	return nil
}

func containsVirshName(output string, expected string) bool {
	for _, name := range parseVirshNameLines(output) {
		if name == expected {
			return true
		}
	}
	return false
}

func (p *virshVMProvider) Create(ctx context.Context) (instance vmInstance, err error) {
	p.mu.Lock()
	prepared := p.prepared
	p.mu.Unlock()
	if !prepared {
		return vmInstance{}, errors.New("libvirt provider is not prepared")
	}

	instance, err = p.newInstanceIdentity(ctx)
	if err != nil {
		return vmInstance{}, err
	}
	cleanupTarget := instance
	cleanup := false
	defer func() {
		if err == nil {
			return
		}
		if !cleanup {
			p.releaseInstanceIdentity(cleanupTarget)
			return
		}
		cleanupErr := p.cleanupFailedInstance(cleanupTarget)
		err = errors.Join(err, cleanupErr)
		if cleanupErr == nil {
			// The provider has already removed every resource. Returning a zero
			// identity prevents the pool from performing a redundant second
			// destruction pass during shutdown.
			instance = vmInstance{}
		}
	}()

	ignition, err := renderFlatcarIgnition(
		instance.Name,
		p.config.SSHUser,
		p.publicKey,
		p.config.RegistryMirrors,
		p.config.InsecureRegistries,
	)
	if err != nil {
		return instance, err
	}
	// From this point onward a failed write may still have created a file.
	// Keep the identity available to both the immediate rollback and the pool's
	// retry path.
	cleanup = true
	if err = writeLibvirtFileAtomic(instance.IgnitionPath, ignition, 0o644); err != nil {
		return instance, fmt.Errorf("write VM Ignition: %w", err)
	}

	if _, err = p.virsh(
		ctx,
		"vol-create-as",
		"--pool", p.config.PoolName,
		"--name", instance.VolumeName,
		"--capacity", strconv.Itoa(p.config.DiskSizeGiB)+"G",
		"--format", "qcow2",
		"--backing-vol", p.config.BaseVolumeName,
		"--backing-vol-format", "qcow2",
	); err != nil {
		return instance, fmt.Errorf("create VM qcow2 overlay: %w", err)
	}
	diskPath, err := p.virsh(
		ctx,
		"vol-path",
		instance.VolumeName,
		"--pool",
		p.config.PoolName,
	)
	if err != nil {
		return instance, fmt.Errorf("resolve VM volume path: %w", err)
	}
	if !pathWithinDirectory(p.config.PoolPath, diskPath) {
		return instance, fmt.Errorf("VM volume path %q is outside storage pool", diskPath)
	}
	diskInfo, statErr := os.Lstat(diskPath)
	if statErr != nil {
		return instance, fmt.Errorf("inspect VM volume file: %w", statErr)
	}
	if !diskInfo.Mode().IsRegular() {
		return instance, errors.New("VM volume must be a regular file")
	}
	domainXML, err := renderLibvirtDomainXML(libvirtDomainTemplateData{
		Name:         instance.Name,
		Description:  "managed-by:gitone:" + p.ownerPrefix,
		MemoryMiB:    p.config.MemoryMiB,
		VCPUs:        p.config.VCPUs,
		DiskPath:     diskPath,
		MACAddress:   instance.MACAddress,
		NetworkName:  p.config.NetworkName,
		IgnitionPath: instance.IgnitionPath,
	})
	if err != nil {
		return instance, err
	}
	domainXMLPath, err := writeLibvirtTemporaryFile(
		p.config.PoolPath,
		"."+instance.Name+"-*.xml",
		domainXML,
		0o600,
	)
	if err != nil {
		return instance, fmt.Errorf("write VM domain XML: %w", err)
	}
	if _, err = p.virsh(ctx, "define", domainXMLPath); err != nil {
		_ = os.Remove(domainXMLPath)
		return instance, fmt.Errorf("define KVM domain: %w", err)
	}
	if removeErr := os.Remove(domainXMLPath); removeErr != nil {
		return instance, fmt.Errorf("remove temporary VM domain XML: %w", removeErr)
	}
	if _, err = p.virsh(ctx, "start", instance.Name); err != nil {
		return instance, fmt.Errorf("start KVM domain: %w", err)
	}
	instance.Address, err = p.waitUntilReady(ctx, instance)
	if err != nil {
		return instance, fmt.Errorf("wait for KVM guest readiness: %w", err)
	}
	return instance, nil
}

func (p *virshVMProvider) newInstanceIdentity(ctx context.Context) (vmInstance, error) {
	for range libvirtMaximumNameAttempts {
		randomBytes := make([]byte, libvirtInstanceSuffixBytes)
		if _, err := rand.Read(randomBytes); err != nil {
			return vmInstance{}, fmt.Errorf("generate VM identity: %w", err)
		}
		suffix := hex.EncodeToString(randomBytes)
		name := fmt.Sprintf(
			"%s-%s-%s",
			p.ownerPrefix,
			time.Now().UTC().Format(libvirtInstanceTimestampLayout),
			suffix,
		)
		available, err := p.instanceNameAvailable(ctx, name)
		if err != nil {
			return vmInstance{}, err
		}
		if !available {
			continue
		}
		ignitionPath := filepath.Join(p.config.PoolPath, name+".ign")
		if _, err := os.Lstat(ignitionPath); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return vmInstance{}, fmt.Errorf("check VM identity collision: %w", err)
		}
		macAddress := fmt.Sprintf("52:54:00:%02x:%02x:%02x", randomBytes[0], randomBytes[1], randomBytes[2])
		available, err = p.macAddressAvailable(ctx, macAddress)
		if err != nil {
			return vmInstance{}, err
		}
		if !available {
			continue
		}
		instance := vmInstance{
			Name:         name,
			MACAddress:   macAddress,
			VolumeName:   name + ".qcow2",
			IgnitionPath: ignitionPath,
		}
		if !p.reserveInstanceIdentity(instance) {
			continue
		}
		return instance, nil
	}
	return vmInstance{}, errors.New("could not generate a unique VM identity")
}

// reserveInstanceIdentity closes the check-then-create window between
// concurrent Create calls. The reservation lives until Destroy confirms all
// resources are gone, so neither a name nor a MAC can be handed to another VM
// while rollback is still pending.
func (p *virshVMProvider) reserveInstanceIdentity(instance vmInstance) bool {
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	if p.allocatedNames == nil {
		p.allocatedNames = make(map[string]string)
	}
	if p.allocatedMACAddresses == nil {
		p.allocatedMACAddresses = make(map[string]string)
	}
	macAddress := strings.ToLower(instance.MACAddress)
	if _, exists := p.allocatedNames[instance.Name]; exists {
		return false
	}
	if _, exists := p.allocatedMACAddresses[macAddress]; exists {
		return false
	}
	p.allocatedNames[instance.Name] = macAddress
	p.allocatedMACAddresses[macAddress] = instance.Name
	return true
}

func (p *virshVMProvider) releaseInstanceIdentity(instance vmInstance) {
	p.identityMu.Lock()
	defer p.identityMu.Unlock()
	macAddress, exists := p.allocatedNames[instance.Name]
	if !exists {
		return
	}
	delete(p.allocatedNames, instance.Name)
	if p.allocatedMACAddresses[macAddress] == instance.Name {
		delete(p.allocatedMACAddresses, macAddress)
	}
}

func (p *virshVMProvider) instanceNameAvailable(ctx context.Context, name string) (bool, error) {
	if _, err := p.virsh(ctx, "dominfo", name); err == nil {
		return false, nil
	} else if !libvirtResourceAbsent(err) {
		return false, fmt.Errorf("check KVM domain identity collision: %w", err)
	}
	if _, err := p.virsh(ctx, "vol-info", name+".qcow2", "--pool", p.config.PoolName); err == nil {
		return false, nil
	} else if !libvirtResourceAbsent(err) {
		return false, fmt.Errorf("check VM volume identity collision: %w", err)
	}
	return true, nil
}

func (p *virshVMProvider) macAddressAvailable(ctx context.Context, candidate string) (bool, error) {
	domains, err := p.virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return false, fmt.Errorf("list domains for MAC allocation: %w", err)
	}
	for _, name := range parseVirshNameLines(domains) {
		interfaces, interfaceErr := p.virsh(ctx, "domiflist", name)
		if interfaceErr != nil {
			if libvirtResourceAbsent(interfaceErr) {
				continue
			}
			return false, fmt.Errorf("read domain %q interfaces: %w", name, interfaceErr)
		}
		for _, field := range strings.Fields(interfaces) {
			if strings.EqualFold(strings.TrimSpace(field), candidate) {
				return false, nil
			}
		}
	}
	return true, nil
}

func (p *virshVMProvider) waitUntilReady(parent context.Context, instance vmInstance) (string, error) {
	ctx, cancel := context.WithTimeout(parent, p.config.ReadyTimeout)
	defer cancel()
	var lastErr error
	for {
		address, err := p.discoverAddress(ctx, instance)
		if err == nil {
			instance.Address = address
			attemptCtx, attemptCancel := context.WithTimeout(ctx, 8*time.Second)
			err = p.verifyGuestReady(attemptCtx, instance)
			attemptCancel()
			if err == nil {
				return address, nil
			}
		}
		lastErr = err
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return "", errors.Join(ctx.Err(), lastErr)
		case <-timer.C:
		}
	}
}

func (p *virshVMProvider) CheckReady(ctx context.Context, instance vmInstance) error {
	if !p.ownsInstance(instance) || net.ParseIP(instance.Address) == nil {
		return errors.New("refusing to check readiness of an unmanaged VM")
	}
	state, err := p.virsh(ctx, "domstate", instance.Name)
	if err != nil {
		return fmt.Errorf("read KVM domain state: %w", err)
	}
	if strings.ToLower(strings.TrimSpace(state)) != "running" {
		return fmt.Errorf("KVM domain state is %q, not running", strings.TrimSpace(state))
	}
	if err = p.verifyGuestReady(ctx, instance); err != nil {
		return fmt.Errorf("verify guest SSH and Docker readiness: %w", err)
	}
	return nil
}

func (p *virshVMProvider) discoverAddress(ctx context.Context, instance vmInstance) (string, error) {
	var errs []error
	for _, source := range []string{"lease", "agent", "arp"} {
		output, err := p.virsh(ctx, "domifaddr", instance.Name, "--source", source)
		if err == nil {
			if address := parseLibvirtIPAddress(output); address != "" {
				return address, nil
			}
		} else {
			errs = append(errs, err)
		}
	}
	output, err := p.virsh(
		ctx,
		"net-dhcp-leases",
		p.config.NetworkName,
		"--mac",
		instance.MACAddress,
	)
	if err == nil {
		if address := parseLibvirtIPAddress(output); address != "" {
			return address, nil
		}
	} else {
		errs = append(errs, err)
	}
	return "", errors.Join(append([]error{errors.New("VM has no discoverable IP address")}, errs...)...)
}

func parseLibvirtIPAddress(output string) string {
	var fallback string
	for _, field := range strings.Fields(output) {
		field = strings.Trim(field, "[](),")
		address := net.ParseIP(field)
		if strings.Contains(field, "/") {
			parsed, _, err := net.ParseCIDR(field)
			if err == nil {
				address = parsed
			}
		}
		if address == nil || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() {
			continue
		}
		if address.To4() != nil {
			return address.String()
		}
		if fallback == "" {
			fallback = address.String()
		}
	}
	return fallback
}

func (p *virshVMProvider) cleanupFailedInstance(instance vmInstance) error {
	timeout := p.config.CleanupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.Destroy(ctx, instance)
}

func (p *virshVMProvider) Destroy(ctx context.Context, instance vmInstance) error {
	if !p.ownsInstance(instance) {
		return errors.New("refusing to destroy an unmanaged VM")
	}
	if instance.VolumeName == "" {
		instance.VolumeName = instance.Name + ".qcow2"
	}
	if instance.IgnitionPath == "" {
		instance.IgnitionPath = filepath.Join(p.config.PoolPath, instance.Name+".ign")
	}
	var errs []error
	domainExists := false
	domainRemoved := false
	if _, err := p.virsh(ctx, "dominfo", instance.Name); err == nil {
		domainExists = true
	} else if libvirtResourceAbsent(err) {
		domainRemoved = true
	} else {
		errs = append(errs, fmt.Errorf("inspect KVM domain before deletion: %w", err))
	}
	if domainExists {
		if ownershipErr := p.verifyDomainOwnership(ctx, instance); ownershipErr != nil {
			errs = append(errs, ownershipErr)
			return errors.Join(errs...)
		}
		stopped := false
		state, stateErr := p.virsh(ctx, "domstate", instance.Name)
		if stateErr != nil {
			errs = append(errs, fmt.Errorf("read KVM domain state: %w", stateErr))
		}
		if stateErr == nil && strings.Contains(strings.ToLower(state), "shut off") {
			stopped = true
		} else {
			if _, gracefulErr := p.virsh(
				ctx,
				"destroy",
				instance.Name,
				"--graceful",
				"--remove-logs",
			); gracefulErr != nil {
				if libvirtResourceAbsent(gracefulErr) {
					stopped = true
				} else if _, forceErr := p.virsh(
					ctx,
					"destroy",
					instance.Name,
					"--remove-logs",
				); forceErr == nil || libvirtResourceAbsent(forceErr) {
					stopped = true
				} else {
					errs = append(errs, errors.Join(
						fmt.Errorf("gracefully stop KVM domain: %w", gracefulErr),
						fmt.Errorf("force stop KVM domain: %w", forceErr),
					))
				}
			} else {
				stopped = true
			}
		}
		if stopped {
			if _, err := p.virsh(ctx, "undefine", instance.Name); err == nil || libvirtResourceAbsent(err) {
				domainRemoved = true
			} else {
				errs = append(errs, fmt.Errorf("undefine KVM domain: %w", err))
			}
		}
	}

	// Keep the backing resources intact while a domain may still be using
	// them. A later pool reconciliation retries the complete operation.
	if !domainRemoved {
		return errors.Join(errs...)
	}
	volumeExists := false
	if _, err := p.virsh(ctx, "vol-info", instance.VolumeName, "--pool", p.config.PoolName); err == nil {
		volumeExists = true
	} else if !libvirtResourceAbsent(err) {
		errs = append(errs, fmt.Errorf("inspect VM volume before deletion: %w", err))
	}
	if volumeExists {
		if _, err := p.virsh(ctx, "vol-delete", instance.VolumeName, "--pool", p.config.PoolName); err != nil && !libvirtResourceAbsent(err) {
			errs = append(errs, fmt.Errorf("delete VM volume: %w", err))
		}
	}
	if err := os.Remove(instance.IgnitionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("delete VM Ignition: %w", err))
	}
	result := errors.Join(errs...)
	if result == nil {
		if p.guestSSH != nil {
			p.guestSSH.ForgetHost(instance.Name)
		}
		p.releaseInstanceIdentity(instance)
	}
	return result
}

func (p *virshVMProvider) verifyDomainOwnership(ctx context.Context, instance vmInstance) error {
	contents, err := p.virsh(ctx, "dumpxml", instance.Name)
	if err != nil {
		return fmt.Errorf("read KVM domain ownership metadata: %w", err)
	}
	var domain libvirtOwnedDomainDescription
	if err = xml.Unmarshal([]byte(contents), &domain); err != nil {
		return fmt.Errorf("parse KVM domain ownership metadata: %w", err)
	}
	expectedDescription := "managed-by:gitone:" + p.ownerPrefix
	if domain.Description != expectedDescription {
		return fmt.Errorf("refusing to destroy KVM domain with ownership marker %q", domain.Description)
	}
	expectedDiskPath := filepath.Join(p.config.PoolPath, instance.VolumeName)
	for _, disk := range domain.Devices.Disks {
		if disk.Device == "disk" && filepath.Clean(disk.Source.File) == filepath.Clean(expectedDiskPath) {
			return nil
		}
	}
	return fmt.Errorf("refusing to destroy KVM domain without owned disk %q", expectedDiskPath)
}

func (p *virshVMProvider) ownsInstance(instance vmInstance) bool {
	if !p.ownsName(instance.Name) {
		return false
	}
	expectedVolume := instance.Name + ".qcow2"
	expectedIgnition := filepath.Join(p.config.PoolPath, instance.Name+".ign")
	return (instance.VolumeName == "" || instance.VolumeName == expectedVolume) &&
		(instance.IgnitionPath == "" || filepath.Clean(instance.IgnitionPath) == filepath.Clean(expectedIgnition))
}

func (p *virshVMProvider) ownsName(name string) bool {
	tail, found := strings.CutPrefix(name, p.ownerPrefix+"-")
	if !found || len(tail) != len("20060102150405-000000") {
		return false
	}
	if tail[14] != '-' {
		return false
	}
	if _, err := time.Parse(libvirtInstanceTimestampLayout, tail[:14]); err != nil {
		return false
	}
	_, err := hex.DecodeString(tail[15:])
	return err == nil
}

func libvirtResourceAbsent(err error) bool {
	if err == nil {
		return false
	}
	diagnostic := strings.TrimSpace(err.Error())
	var commandErr *libvirtCommandError
	if errors.As(err, &commandErr) {
		switch {
		case strings.TrimSpace(commandErr.Stderr) != "":
			diagnostic = strings.TrimSpace(commandErr.Stderr)
		case commandErr.Err != nil:
			diagnostic = strings.TrimSpace(commandErr.Err.Error())
		}
	}
	if libvirtMissingDomainErrorPattern.MatchString(diagnostic) {
		return true
	}
	message := strings.ToLower(diagnostic)
	for _, fragment := range []string{
		"domain not found",
		"no domain with matching name",
		"storage volume not found",
		"no storage vol",
		"unknown storage volume",
	} {
		if strings.Contains(message, fragment) {
			return true
		}
	}
	return false
}

func (p *virshVMProvider) cleanupOwnedResources(ctx context.Context) error {
	var errs []error
	protectedNames := make(map[string]struct{})
	domains, err := p.virsh(ctx, "list", "--all", "--name")
	if err != nil {
		return fmt.Errorf("list KVM domains: %w", err)
	}
	for _, name := range parseVirshNameLines(domains) {
		if !p.ownsName(name) {
			continue
		}
		instance := vmInstance{
			Name:         name,
			VolumeName:   name + ".qcow2",
			IgnitionPath: filepath.Join(p.config.PoolPath, name+".ign"),
		}
		if destroyErr := p.Destroy(ctx, instance); destroyErr != nil {
			errs = append(errs, destroyErr)
			// Never let the independent orphan sweeps below remove backing
			// resources for a domain whose teardown was not confirmed.
			protectedNames[name] = struct{}{}
		}
	}
	knownAbsent := make(map[string]bool)
	safeToDeleteArtifacts := func(name string) bool {
		if _, protected := protectedNames[name]; protected {
			return false
		}
		if absent, known := knownAbsent[name]; known {
			return absent
		}
		_, domainErr := p.virsh(ctx, "dominfo", name)
		switch {
		case domainErr == nil:
			errs = append(errs, fmt.Errorf(
				"refusing to delete artifacts for active owned domain %q",
				name,
			))
			knownAbsent[name] = false
		case libvirtResourceAbsent(domainErr):
			knownAbsent[name] = true
		default:
			errs = append(errs, fmt.Errorf(
				"verify owned domain %q is absent: %w",
				name,
				domainErr,
			))
			knownAbsent[name] = false
		}
		return knownAbsent[name]
	}

	volumes, err := p.virsh(ctx, "vol-list", p.config.PoolName)
	if err != nil {
		errs = append(errs, fmt.Errorf("list libvirt volumes: %w", err))
	} else {
		for _, volumeName := range parseVirshTableNames(volumes) {
			name, found := strings.CutSuffix(volumeName, ".qcow2")
			if !found || !p.ownsName(name) || !safeToDeleteArtifacts(name) {
				continue
			}
			if _, deleteErr := p.virsh(ctx, "vol-delete", volumeName, "--pool", p.config.PoolName); deleteErr != nil && !libvirtResourceAbsent(deleteErr) {
				errs = append(errs, fmt.Errorf("delete stale VM volume %q: %w", volumeName, deleteErr))
			}
		}
	}

	ignitionPattern := filepath.Join(p.config.PoolPath, p.ownerPrefix+"-*.ign")
	ignitionPaths, globErr := filepath.Glob(ignitionPattern)
	if globErr != nil {
		errs = append(errs, fmt.Errorf("find stale VM Ignition files: %w", globErr))
	}
	for _, ignitionPath := range ignitionPaths {
		name := strings.TrimSuffix(filepath.Base(ignitionPath), ".ign")
		if !p.ownsName(name) || !safeToDeleteArtifacts(name) {
			continue
		}
		if removeErr := os.Remove(ignitionPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("delete stale VM Ignition %q: %w", ignitionPath, removeErr))
		}
	}

	// Releases before the native Go SSH transport kept public host-key pins in
	// the pool. Remove those legacy files only after confirming their one-use VM
	// no longer exists; current releases never create them.
	legacyKnownHostsPattern := filepath.Join(p.config.PoolPath, "."+p.ownerPrefix+"-*.known_hosts")
	legacyKnownHostsPaths, globErr := filepath.Glob(legacyKnownHostsPattern)
	if globErr != nil {
		errs = append(errs, fmt.Errorf("find legacy VM SSH known-hosts files: %w", globErr))
	}
	for _, knownHostsPath := range legacyKnownHostsPaths {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(knownHostsPath), "."), ".known_hosts")
		if !p.ownsName(name) || !safeToDeleteArtifacts(name) {
			continue
		}
		if removeErr := os.Remove(knownHostsPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("delete legacy VM SSH known-hosts file %q: %w", knownHostsPath, removeErr))
		}
	}

	return errors.Join(errs...)
}

func parseVirshNameLines(output string) []string {
	var names []string
	for _, line := range strings.Split(output, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func parseVirshTableNames(output string) []string {
	var names []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(strings.ToLower(line), "name ") || strings.HasPrefix(line, "---") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	return names
}

func (p *virshVMProvider) Cleanup(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.prepared && p.lockFile == nil {
		return nil
	}
	p.prepared = false
	var errs []error
	if err := p.cleanupOwnedResources(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := p.releaseLock(); err != nil {
		errs = append(errs, fmt.Errorf("release libvirt provider lock: %w", err))
	}
	return errors.Join(errs...)
}

func writeLibvirtTemporaryFile(
	directory string,
	pattern string,
	contents []byte,
	mode os.FileMode,
) (string, error) {
	file, err := os.CreateTemp(directory, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(path)
		}
	}()
	if err = file.Chmod(mode); err == nil {
		_, err = file.Write(contents)
	}
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return "", err
	}
	cleanup = false
	return path, nil
}

func writeLibvirtFileAtomic(path string, contents []byte, mode os.FileMode) (err error) {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("refusing to overwrite existing file %q", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := writeLibvirtTemporaryFile(filepath.Dir(path), ".gitone-ignition-*.tmp", contents, mode)
	if err != nil {
		return err
	}
	defer func() {
		if removeErr := os.Remove(temporary); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	}()
	if err = os.Link(temporary, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("refusing to overwrite existing file %q", path)
		}
		return err
	}
	committed := true
	defer func() {
		if err != nil && committed {
			err = errors.Join(err, os.Remove(path))
		}
	}()
	if err = os.Remove(temporary); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	err = errors.Join(directory.Sync(), directory.Close())
	if err == nil {
		committed = false
	}
	return err
}
