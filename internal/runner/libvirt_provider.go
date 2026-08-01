package runner

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
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

	libvirt "github.com/digitalocean/go-libvirt"
	"golang.org/x/sys/unix"
)

const (
	libvirtInstanceTimestampLayout = "20060102150405"
	libvirtInstanceSuffixBytes     = 3
	libvirtMaximumNameAttempts     = 16
)

var (
	libvirtResourceNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	libvirtRemoteCommandPattern = regexp.MustCompile(`^[A-Za-z0-9_./+-]+$`)
	libvirtSSHUserPattern       = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
)

type libvirtRPCProvider struct {
	config      LibvirtConfig
	client      libvirtRPCClient
	connector   libvirtRPCConnector
	pool        libvirt.StoragePool
	baseVolume  libvirt.StorageVol
	basePath    string
	network     libvirt.Network
	httpClient  *http.Client
	ownerPrefix string

	mu       sync.Mutex
	prepared bool
	lockFile *os.File
	guestSSH libvirtGuestSSH

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
		Interfaces []struct {
			MAC struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Source struct {
				Network string `xml:"network,attr"`
			} `xml:"source"`
		} `xml:"interface"`
	} `xml:"devices"`
}

func newLibvirtRPCProvider(config LibvirtConfig) vmProvider {
	ownerPrefix := libvirtOwnerPrefix(config.URI, config.PoolName, config.RunnerID)
	return &libvirtRPCProvider{
		config:                config,
		connector:             connectLibvirtRPC,
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

func (p *libvirtRPCProvider) Prepare(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.prepared {
		return nil
	}
	if err := p.validateRuntimeConfig(); err != nil {
		return err
	}

	var err error
	if p.guestSSH == nil {
		p.guestSSH, err = newNativeLibvirtSSH(p.config.SSHUser, p.config.SSHPort)
		if err != nil {
			return err
		}
	}

	if err = p.acquireLock(); err != nil {
		return err
	}
	prepared := false
	defer func() {
		if prepared {
			return
		}
		if p.client != nil {
			_ = p.client.Disconnect()
			p.client = nil
		}
		_ = p.releaseLock()
	}()
	if p.connector == nil {
		p.connector = connectLibvirtRPC
	}
	p.client, err = p.connector(ctx, p.config.URI)
	if err != nil {
		return fmt.Errorf("connect to libvirt: %w", err)
	}

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

func (p *libvirtRPCProvider) validateRuntimeConfig() error {
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
	case libvirtURIUsesSSHTransport(p.config.URI):
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

func (p *libvirtRPCProvider) acquireLock() error {
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

func (p *libvirtRPCProvider) acquireNetworkLock(ctx context.Context) (*os.File, error) {
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

func (p *libvirtRPCProvider) releaseLock() error {
	if p.lockFile == nil {
		return nil
	}
	err := unix.Flock(int(p.lockFile.Fd()), unix.LOCK_UN)
	err = errors.Join(err, p.lockFile.Close())
	p.lockFile = nil
	return err
}

func (p *libvirtRPCProvider) validateKVM(ctx context.Context) error {
	var capabilities string
	err := callLibvirtRPC(ctx, func() (err error) {
		capabilities, err = p.client.ConnectGetDomainCapabilities(
			nil,
			nil,
			nil,
			libvirt.OptString{"kvm"},
			0,
		)
		return err
	})
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

type libvirtVolumeCreateDescription struct {
	XMLName    xml.Name `xml:"volume"`
	Name       string   `xml:"name"`
	Capacity   uint64   `xml:"capacity"`
	Allocation uint64   `xml:"allocation"`
	Target     struct {
		Format struct {
			Type string `xml:"type,attr"`
		} `xml:"format"`
	} `xml:"target"`
	BackingStore struct {
		Path   string `xml:"path"`
		Format struct {
			Type string `xml:"type,attr"`
		} `xml:"format"`
	} `xml:"backingStore"`
}

func renderLibvirtVolumeXML(name string, capacity uint64, backingPath string) (string, error) {
	volume := libvirtVolumeCreateDescription{
		Name:       name,
		Capacity:   capacity,
		Allocation: 0,
	}
	volume.Target.Format.Type = "qcow2"
	volume.BackingStore.Path = backingPath
	volume.BackingStore.Format.Type = "qcow2"
	contents, err := xml.Marshal(volume)
	if err != nil {
		return "", fmt.Errorf("render libvirt volume XML: %w", err)
	}
	return string(contents), nil
}

func (p *libvirtRPCProvider) validateStoragePool(ctx context.Context) error {
	var err error
	err = callLibvirtRPC(ctx, func() error {
		p.pool, err = p.client.StoragePoolLookupByName(p.config.PoolName)
		return err
	})
	if err != nil {
		return fmt.Errorf("inspect libvirt storage pool: %w", err)
	}
	var active int32
	err = callLibvirtRPC(ctx, func() error {
		active, err = p.client.StoragePoolIsActive(p.pool)
		return err
	})
	if err != nil {
		return fmt.Errorf("inspect libvirt storage pool state: %w", err)
	}
	if active == 0 {
		if err = callLibvirtRPC(ctx, func() error {
			return p.client.StoragePoolCreate(p.pool, 0)
		}); err != nil {
			// Pool ownership is shared by runners, while the provider lock is
			// intentionally runner-specific. Accept only a simultaneous start
			// that can be confirmed by a fresh state request.
			var recheck int32
			recheckErr := callLibvirtRPC(ctx, func() (recheckErr error) {
				recheck, recheckErr = p.client.StoragePoolIsActive(p.pool)
				return recheckErr
			})
			if recheckErr != nil || recheck == 0 {
				return errors.Join(fmt.Errorf("start libvirt storage pool: %w", err), recheckErr)
			}
		}
	}
	var poolXML string
	err = callLibvirtRPC(ctx, func() (err error) {
		poolXML, err = p.client.StoragePoolGetXMLDesc(p.pool, 0)
		return err
	})
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

func (p *libvirtRPCProvider) validateBaseStorage(ctx context.Context) error {
	if err := p.ensureFlatcarBaseImage(ctx); err != nil {
		return fmt.Errorf("ensure Flatcar base image: %w", err)
	}

	err := callLibvirtRPC(ctx, func() (err error) {
		p.baseVolume, err = p.client.StorageVolLookupByName(p.pool, p.config.BaseVolumeName)
		return err
	})
	if err != nil {
		return fmt.Errorf("inspect base libvirt volume: %w", err)
	}
	var volumeXML string
	err = callLibvirtRPC(ctx, func() (err error) {
		volumeXML, err = p.client.StorageVolGetXMLDesc(p.baseVolume, 0)
		return err
	})
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
	err = callLibvirtRPC(ctx, func() (err error) {
		p.basePath, err = p.client.StorageVolGetPath(p.baseVolume)
		return err
	})
	if err != nil {
		return fmt.Errorf("resolve base libvirt volume path: %w", err)
	}
	if !pathWithinDirectory(p.config.PoolPath, p.basePath) {
		return fmt.Errorf("base libvirt volume path %q is outside storage pool", p.basePath)
	}
	baseInfo, err := os.Lstat(p.basePath)
	if err != nil {
		return fmt.Errorf("inspect base libvirt volume file: %w", err)
	}
	if !baseInfo.Mode().IsRegular() {
		return errors.New("base libvirt volume must be a regular file")
	}
	return nil
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

func (p *libvirtRPCProvider) ensureNetwork(ctx context.Context) (err error) {
	lockFile, err := p.acquireNetworkLock(ctx)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, releaseLibvirtFileLock(lockFile))
	}()

	err = callLibvirtRPC(ctx, func() (lookupErr error) {
		p.network, lookupErr = p.client.NetworkLookupByName(p.config.NetworkName)
		return lookupErr
	})
	if err != nil && !libvirtResourceAbsent(err) {
		return fmt.Errorf("inspect libvirt network: %w", err)
	}
	if libvirtResourceAbsent(err) {
		networkXML, renderErr := renderLibvirtNetworkXMLForCIDR(
			p.config.NetworkName,
			p.config.NetworkCIDR,
		)
		if renderErr != nil {
			return renderErr
		}
		err = callLibvirtRPC(ctx, func() (defineErr error) {
			p.network, defineErr = p.client.NetworkDefineXML(string(networkXML))
			return defineErr
		})
		if err != nil {
			// A provider using another storage pool cannot share this lock file.
			// Accept a simultaneous successful definition, but no other error.
			recheckErr := callLibvirtRPC(ctx, func() (lookupErr error) {
				p.network, lookupErr = p.client.NetworkLookupByName(p.config.NetworkName)
				return lookupErr
			})
			if recheckErr != nil {
				return errors.Join(fmt.Errorf("define libvirt network: %w", err), recheckErr)
			}
		}
	}

	var networkXML string
	err = callLibvirtRPC(ctx, func() (readErr error) {
		networkXML, readErr = p.client.NetworkGetXMLDesc(p.network, 0)
		return readErr
	})
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
	var active int32
	err = callLibvirtRPC(ctx, func() (stateErr error) {
		active, stateErr = p.client.NetworkIsActive(p.network)
		return stateErr
	})
	if err != nil {
		return fmt.Errorf("inspect libvirt network: %w", err)
	}
	if active == 0 {
		if err = callLibvirtRPC(ctx, func() error {
			return p.client.NetworkCreate(p.network)
		}); err != nil {
			// Providers using different storage pools cannot share the pool-local
			// lock file. Accept only the narrow race where another provider made
			// this exact network active between the state check and create call.
			var recheck int32
			recheckErr := callLibvirtRPC(ctx, func() (stateErr error) {
				recheck, stateErr = p.client.NetworkIsActive(p.network)
				return stateErr
			})
			if recheckErr != nil || recheck == 0 {
				return errors.Join(fmt.Errorf("start libvirt network: %w", err), recheckErr)
			}
		}
	}
	if err = callLibvirtRPC(ctx, func() error {
		return p.client.NetworkSetAutostart(p.network, 1)
	}); err != nil {
		return fmt.Errorf("enable libvirt network autostart: %w", err)
	}
	return nil
}

func (p *libvirtRPCProvider) Create(ctx context.Context) (instance vmInstance, err error) {
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
	identityCreated := false
	defer func() {
		if err == nil {
			return
		}
		if !cleanup {
			if identityCreated {
				p.guestSSH.ForgetVM(cleanupTarget.Name)
			}
			p.releaseInstanceIdentity(cleanupTarget)
			instance = vmInstance{}
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

	publicKey, err := p.guestSSH.CreateIdentity(instance.Name)
	if err != nil {
		return instance, fmt.Errorf("create VM SSH identity: %w", err)
	}
	identityCreated = true
	ignition, err := renderFlatcarIgnition(
		instance.Name,
		p.config.SSHUser,
		publicKey,
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

	volumeXML, err := renderLibvirtVolumeXML(
		instance.VolumeName,
		uint64(p.config.DiskSizeGiB)<<30,
		p.basePath,
	)
	if err != nil {
		return instance, err
	}
	var volume libvirt.StorageVol
	err = callLibvirtRPC(ctx, func() (createErr error) {
		volume, createErr = p.client.StorageVolCreateXML(p.pool, volumeXML, 0)
		return createErr
	})
	if err != nil {
		return instance, fmt.Errorf("create VM qcow2 overlay: %w", err)
	}
	var diskPath string
	err = callLibvirtRPC(ctx, func() (pathErr error) {
		diskPath, pathErr = p.client.StorageVolGetPath(volume)
		return pathErr
	})
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
	var domain libvirt.Domain
	err = callLibvirtRPC(ctx, func() (defineErr error) {
		domain, defineErr = p.client.DomainDefineXML(string(domainXML))
		return defineErr
	})
	if err != nil {
		return instance, fmt.Errorf("define KVM domain: %w", err)
	}
	if err = callLibvirtRPC(ctx, func() error {
		return p.client.DomainCreate(domain)
	}); err != nil {
		return instance, fmt.Errorf("start KVM domain: %w", err)
	}
	instance.Address, err = p.waitUntilReady(ctx, instance)
	if err != nil {
		return instance, fmt.Errorf("wait for KVM guest readiness: %w", err)
	}
	return instance, nil
}

func (p *libvirtRPCProvider) newInstanceIdentity(ctx context.Context) (vmInstance, error) {
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
func (p *libvirtRPCProvider) reserveInstanceIdentity(instance vmInstance) bool {
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

func (p *libvirtRPCProvider) releaseInstanceIdentity(instance vmInstance) {
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

func (p *libvirtRPCProvider) instanceNameAvailable(ctx context.Context, name string) (bool, error) {
	err := callLibvirtRPC(ctx, func() (lookupErr error) {
		_, lookupErr = p.client.DomainLookupByName(name)
		return lookupErr
	})
	if err == nil {
		return false, nil
	} else if !libvirtResourceAbsent(err) {
		return false, fmt.Errorf("check KVM domain identity collision: %w", err)
	}
	err = callLibvirtRPC(ctx, func() (lookupErr error) {
		_, lookupErr = p.client.StorageVolLookupByName(p.pool, name+".qcow2")
		return lookupErr
	})
	if err == nil {
		return false, nil
	} else if !libvirtResourceAbsent(err) {
		return false, fmt.Errorf("check VM volume identity collision: %w", err)
	}
	return true, nil
}

func (p *libvirtRPCProvider) macAddressAvailable(ctx context.Context, candidate string) (bool, error) {
	var domains []libvirt.Domain
	err := callLibvirtRPC(ctx, func() (listErr error) {
		domains, _, listErr = p.client.ConnectListAllDomains(
			1,
			libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive,
		)
		return listErr
	})
	if err != nil {
		return false, fmt.Errorf("list domains for MAC allocation: %w", err)
	}
	for _, domain := range domains {
		var contents string
		readErr := callLibvirtRPC(ctx, func() (readErr error) {
			contents, readErr = p.client.DomainGetXMLDesc(domain, 0)
			return readErr
		})
		if readErr != nil {
			if libvirtResourceAbsent(readErr) {
				continue
			}
			return false, fmt.Errorf("read domain %q interfaces: %w", domain.Name, readErr)
		}
		var description libvirtOwnedDomainDescription
		if err = xml.Unmarshal([]byte(contents), &description); err != nil {
			return false, fmt.Errorf("parse domain %q interfaces: %w", domain.Name, err)
		}
		for _, domainInterface := range description.Devices.Interfaces {
			if strings.EqualFold(domainInterface.MAC.Address, candidate) {
				return false, nil
			}
		}
	}
	return true, nil
}

func (p *libvirtRPCProvider) waitUntilReady(parent context.Context, instance vmInstance) (string, error) {
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

func (p *libvirtRPCProvider) CheckReady(ctx context.Context, instance vmInstance) error {
	if !p.ownsInstance(instance) || net.ParseIP(instance.Address) == nil {
		return errors.New("refusing to check readiness of an unmanaged VM")
	}
	var domain libvirt.Domain
	err := callLibvirtRPC(ctx, func() (lookupErr error) {
		domain, lookupErr = p.client.DomainLookupByName(instance.Name)
		return lookupErr
	})
	if err != nil {
		return fmt.Errorf("look up KVM domain: %w", err)
	}
	var state int32
	err = callLibvirtRPC(ctx, func() (stateErr error) {
		state, _, stateErr = p.client.DomainGetState(domain, 0)
		return stateErr
	})
	if err != nil {
		return fmt.Errorf("read KVM domain state: %w", err)
	}
	if libvirt.DomainState(state) != libvirt.DomainRunning {
		return fmt.Errorf("KVM domain state is %q, not running", libvirt.DomainState(state))
	}
	address, err := p.discoverAddress(ctx, instance)
	if err != nil {
		return fmt.Errorf("verify guest MAC, DHCP lease, and IP address: %w", err)
	}
	if !net.ParseIP(address).Equal(net.ParseIP(instance.Address)) {
		return fmt.Errorf(
			"VM address changed from %q to %q for MAC %q",
			instance.Address,
			address,
			instance.MACAddress,
		)
	}
	if err = p.verifyGuestReady(ctx, instance); err != nil {
		return fmt.Errorf("verify guest SSH and Docker readiness: %w", err)
	}
	return nil
}

func (p *libvirtRPCProvider) discoverAddress(ctx context.Context, instance vmInstance) (string, error) {
	addressRange, err := newLibvirtDHCPAddressRange(p.config.NetworkCIDR, p.config.NetworkName)
	if err != nil {
		return "", err
	}
	if _, err = net.ParseMAC(instance.MACAddress); err != nil {
		return "", fmt.Errorf("invalid VM MAC address %q", instance.MACAddress)
	}
	var domain libvirt.Domain
	err = callLibvirtRPC(ctx, func() (lookupErr error) {
		domain, lookupErr = p.client.DomainLookupByName(instance.Name)
		return lookupErr
	})
	if err != nil {
		return "", fmt.Errorf("look up KVM domain for address discovery: %w", err)
	}
	if err = p.verifyDomainNetworkIdentity(ctx, domain, instance); err != nil {
		return "", err
	}

	var errs []error
	interfaceAddresses := make(map[string]struct{})
	for _, source := range []libvirt.DomainInterfaceAddressesSource{
		libvirt.DomainInterfaceAddressesSrcLease,
		libvirt.DomainInterfaceAddressesSrcAgent,
		libvirt.DomainInterfaceAddressesSrcArp,
	} {
		var interfaces []libvirt.DomainInterface
		err = callLibvirtRPC(ctx, func() (addressErr error) {
			interfaces, addressErr = p.client.DomainInterfaceAddresses(domain, uint32(source), 0)
			return addressErr
		})
		if err == nil {
			address, addressErr := libvirtInterfaceIPAddress(
				interfaces,
				instance.MACAddress,
				addressRange,
			)
			if addressErr != nil {
				return "", addressErr
			}
			if address != "" {
				interfaceAddresses[address] = struct{}{}
			}
		} else {
			errs = append(errs, err)
		}
	}
	if len(interfaceAddresses) > 1 {
		return "", fmt.Errorf(
			"domain interface sources disagree on the runner-network address for MAC %q",
			instance.MACAddress,
		)
	}
	var leases []libvirt.NetworkDhcpLease
	err = callLibvirtRPC(ctx, func() (leaseErr error) {
		leases, _, leaseErr = p.client.NetworkGetDhcpLeases(
			p.network,
			libvirt.OptString{instance.MACAddress},
			1,
			0,
		)
		return leaseErr
	})
	if err == nil {
		address, addressErr := libvirtLeaseIPAddress(
			leases,
			instance.MACAddress,
			addressRange,
			time.Now(),
		)
		if addressErr != nil {
			return "", addressErr
		}
		if address != "" {
			if len(interfaceAddresses) > 0 {
				if _, found := interfaceAddresses[address]; !found {
					return "", fmt.Errorf(
						"DHCP address %q for MAC %q does not match domain interface addresses",
						address,
						instance.MACAddress,
					)
				}
			}
			return address, nil
		}
	} else {
		errs = append(errs, err)
	}
	return "", errors.Join(append(
		[]error{fmt.Errorf("VM MAC %q has no active DHCP lease in the runner network", instance.MACAddress)},
		errs...,
	)...)
}

func (p *libvirtRPCProvider) verifyDomainNetworkIdentity(
	ctx context.Context,
	domain libvirt.Domain,
	instance vmInstance,
) error {
	var contents string
	err := callLibvirtRPC(ctx, func() (readErr error) {
		contents, readErr = p.client.DomainGetXMLDesc(domain, 0)
		return readErr
	})
	if err != nil {
		return fmt.Errorf("read KVM domain network identity: %w", err)
	}
	var description libvirtOwnedDomainDescription
	if err = xml.Unmarshal([]byte(contents), &description); err != nil {
		return fmt.Errorf("parse KVM domain network identity: %w", err)
	}
	if len(description.Devices.Interfaces) != 1 {
		return fmt.Errorf(
			"KVM domain has %d network interfaces, want exactly one",
			len(description.Devices.Interfaces),
		)
	}
	domainInterface := description.Devices.Interfaces[0]
	if !sameLibvirtMACAddress(domainInterface.MAC.Address, instance.MACAddress) {
		return fmt.Errorf(
			"KVM domain MAC %q does not match reserved MAC %q",
			domainInterface.MAC.Address,
			instance.MACAddress,
		)
	}
	if domainInterface.Source.Network != p.config.NetworkName {
		return fmt.Errorf(
			"KVM domain network %q does not match runner network %q",
			domainInterface.Source.Network,
			p.config.NetworkName,
		)
	}
	return nil
}

type libvirtDHCPAddressRange struct {
	network *net.IPNet
	first   uint32
	last    uint32
}

func newLibvirtDHCPAddressRange(cidr string, networkName string) (libvirtDHCPAddressRange, error) {
	normalizedCIDR, err := normalizeLibvirtNetworkCIDR(cidr, networkName)
	if err != nil {
		return libvirtDHCPAddressRange{}, err
	}
	template, err := libvirtNetworkTemplateForCIDR(networkName, normalizedCIDR)
	if err != nil {
		return libvirtDHCPAddressRange{}, err
	}
	_, network, err := net.ParseCIDR(normalizedCIDR)
	if err != nil {
		return libvirtDHCPAddressRange{}, fmt.Errorf("parse libvirt network CIDR: %w", err)
	}
	first := net.ParseIP(template.DHCPStart).To4()
	last := net.ParseIP(template.DHCPEnd).To4()
	if first == nil || last == nil {
		return libvirtDHCPAddressRange{}, errors.New("libvirt DHCP range is not IPv4")
	}
	return libvirtDHCPAddressRange{
		network: network,
		first:   binary.BigEndian.Uint32(first),
		last:    binary.BigEndian.Uint32(last),
	}, nil
}

func (r libvirtDHCPAddressRange) address(field string) (string, bool) {
	address := net.ParseIP(strings.TrimSpace(field))
	if address == nil || !r.network.Contains(address) {
		return "", false
	}
	address = address.To4()
	if address == nil {
		return "", false
	}
	value := binary.BigEndian.Uint32(address)
	if value < r.first || value > r.last {
		return "", false
	}
	return address.String(), true
}

func libvirtInterfaceIPAddress(
	interfaces []libvirt.DomainInterface,
	expectedMAC string,
	addressRange libvirtDHCPAddressRange,
) (string, error) {
	addresses := make(map[string]struct{})
	for _, domainInterface := range interfaces {
		if len(domainInterface.Hwaddr) != 1 ||
			!sameLibvirtMACAddress(domainInterface.Hwaddr[0], expectedMAC) {
			continue
		}
		for _, address := range domainInterface.Addrs {
			if parsed, valid := addressRange.address(address.Addr); valid {
				addresses[parsed] = struct{}{}
			}
		}
	}
	return uniqueLibvirtIPAddress(addresses, "domain interface", expectedMAC)
}

func libvirtLeaseIPAddress(
	leases []libvirt.NetworkDhcpLease,
	expectedMAC string,
	addressRange libvirtDHCPAddressRange,
	now time.Time,
) (string, error) {
	addresses := make(map[string]struct{})
	for _, lease := range leases {
		if len(lease.Mac) != 1 || !sameLibvirtMACAddress(lease.Mac[0], expectedMAC) ||
			lease.Expirytime <= now.Unix() {
			continue
		}
		if address, valid := addressRange.address(lease.Ipaddr); valid {
			addresses[address] = struct{}{}
		}
	}
	return uniqueLibvirtIPAddress(addresses, "DHCP lease", expectedMAC)
}

func uniqueLibvirtIPAddress(addresses map[string]struct{}, source string, macAddress string) (string, error) {
	if len(addresses) > 1 {
		return "", fmt.Errorf("%s reported multiple runner-network addresses for MAC %q", source, macAddress)
	}
	for address := range addresses {
		return address, nil
	}
	return "", nil
}

func sameLibvirtMACAddress(first string, second string) bool {
	firstMAC, firstErr := net.ParseMAC(strings.TrimSpace(first))
	secondMAC, secondErr := net.ParseMAC(strings.TrimSpace(second))
	return firstErr == nil && secondErr == nil && firstMAC.String() == secondMAC.String()
}

func (p *libvirtRPCProvider) cleanupFailedInstance(instance vmInstance) error {
	timeout := p.config.CleanupTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return p.Destroy(ctx, instance)
}

func (p *libvirtRPCProvider) Destroy(ctx context.Context, instance vmInstance) error {
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
	var domain libvirt.Domain
	lookupErr := callLibvirtRPC(ctx, func() (err error) {
		domain, err = p.client.DomainLookupByName(instance.Name)
		return err
	})
	switch {
	case lookupErr == nil:
		domainExists = true
	case libvirtResourceAbsent(lookupErr):
		domainRemoved = true
	default:
		errs = append(errs, fmt.Errorf("inspect KVM domain before deletion: %w", lookupErr))
	}
	if domainExists {
		if ownershipErr := p.verifyDomainOwnership(ctx, instance); ownershipErr != nil {
			errs = append(errs, ownershipErr)
			return errors.Join(errs...)
		}
		stopped := false
		var state int32
		stateErr := callLibvirtRPC(ctx, func() (err error) {
			state, _, err = p.client.DomainGetState(domain, 0)
			return err
		})
		if stateErr != nil {
			errs = append(errs, fmt.Errorf("read KVM domain state: %w", stateErr))
		}
		if stateErr == nil && libvirt.DomainState(state) == libvirt.DomainShutoff {
			stopped = true
		} else {
			gracefulErr := callLibvirtRPC(ctx, func() error {
				return p.client.DomainDestroyFlags(
					domain,
					libvirt.DomainDestroyGraceful|libvirt.DomainDestroyRemoveLogs,
				)
			})
			if gracefulErr != nil {
				if libvirtResourceAbsent(gracefulErr) {
					stopped = true
				} else if forceErr := callLibvirtRPC(ctx, func() error {
					return p.client.DomainDestroyFlags(domain, libvirt.DomainDestroyRemoveLogs)
				}); forceErr == nil || libvirtResourceAbsent(forceErr) {
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
			if err := callLibvirtRPC(ctx, func() error {
				return p.client.DomainUndefineFlags(domain, 0)
			}); err == nil || libvirtResourceAbsent(err) {
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
	var volume libvirt.StorageVol
	lookupErr = callLibvirtRPC(ctx, func() (err error) {
		volume, err = p.client.StorageVolLookupByName(p.pool, instance.VolumeName)
		return err
	})
	if lookupErr == nil {
		volumeExists = true
	} else if !libvirtResourceAbsent(lookupErr) {
		errs = append(errs, fmt.Errorf("inspect VM volume before deletion: %w", lookupErr))
	}
	if volumeExists {
		if err := callLibvirtRPC(ctx, func() error {
			return p.client.StorageVolDelete(volume, 0)
		}); err != nil && !libvirtResourceAbsent(err) {
			errs = append(errs, fmt.Errorf("delete VM volume: %w", err))
		}
	}
	if err := os.Remove(instance.IgnitionPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("delete VM Ignition: %w", err))
	}
	result := errors.Join(errs...)
	if result == nil {
		if p.guestSSH != nil {
			p.guestSSH.ForgetVM(instance.Name)
		}
		p.releaseInstanceIdentity(instance)
	}
	return result
}

func (p *libvirtRPCProvider) verifyDomainOwnership(ctx context.Context, instance vmInstance) error {
	var domain libvirt.Domain
	err := callLibvirtRPC(ctx, func() (lookupErr error) {
		domain, lookupErr = p.client.DomainLookupByName(instance.Name)
		return lookupErr
	})
	if err != nil {
		return fmt.Errorf("look up KVM domain ownership metadata: %w", err)
	}
	var contents string
	err = callLibvirtRPC(ctx, func() (readErr error) {
		contents, readErr = p.client.DomainGetXMLDesc(domain, 0)
		return readErr
	})
	if err != nil {
		return fmt.Errorf("read KVM domain ownership metadata: %w", err)
	}
	var description libvirtOwnedDomainDescription
	if err = xml.Unmarshal([]byte(contents), &description); err != nil {
		return fmt.Errorf("parse KVM domain ownership metadata: %w", err)
	}
	expectedDescription := "managed-by:gitone:" + p.ownerPrefix
	if description.Description != expectedDescription {
		return fmt.Errorf("refusing to destroy KVM domain with ownership marker %q", description.Description)
	}
	expectedDiskPath := filepath.Join(p.config.PoolPath, instance.VolumeName)
	for _, disk := range description.Devices.Disks {
		if disk.Device == "disk" && filepath.Clean(disk.Source.File) == filepath.Clean(expectedDiskPath) {
			return nil
		}
	}
	return fmt.Errorf("refusing to destroy KVM domain without owned disk %q", expectedDiskPath)
}

func (p *libvirtRPCProvider) ownsInstance(instance vmInstance) bool {
	if !p.ownsName(instance.Name) {
		return false
	}
	expectedVolume := instance.Name + ".qcow2"
	expectedIgnition := filepath.Join(p.config.PoolPath, instance.Name+".ign")
	return (instance.VolumeName == "" || instance.VolumeName == expectedVolume) &&
		(instance.IgnitionPath == "" || filepath.Clean(instance.IgnitionPath) == filepath.Clean(expectedIgnition))
}

func (p *libvirtRPCProvider) ownsName(name string) bool {
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

func (p *libvirtRPCProvider) cleanupOwnedResources(ctx context.Context) error {
	var errs []error
	protectedNames := make(map[string]struct{})
	var domains []libvirt.Domain
	err := callLibvirtRPC(ctx, func() (listErr error) {
		domains, _, listErr = p.client.ConnectListAllDomains(
			1,
			libvirt.ConnectListDomainsActive|libvirt.ConnectListDomainsInactive,
		)
		return listErr
	})
	if err != nil {
		return fmt.Errorf("list KVM domains: %w", err)
	}
	for _, domain := range domains {
		name := domain.Name
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
		domainErr := callLibvirtRPC(ctx, func() (lookupErr error) {
			_, lookupErr = p.client.DomainLookupByName(name)
			return lookupErr
		})
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

	var volumes []libvirt.StorageVol
	err = callLibvirtRPC(ctx, func() (listErr error) {
		volumes, _, listErr = p.client.StoragePoolListAllVolumes(p.pool, 1, 0)
		return listErr
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("list libvirt volumes: %w", err))
	} else {
		for _, volume := range volumes {
			volumeName := volume.Name
			name, found := strings.CutSuffix(volumeName, ".qcow2")
			if !found || !p.ownsName(name) || !safeToDeleteArtifacts(name) {
				continue
			}
			if deleteErr := callLibvirtRPC(ctx, func() error {
				return p.client.StorageVolDelete(volume, 0)
			}); deleteErr != nil && !libvirtResourceAbsent(deleteErr) {
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

func (p *libvirtRPCProvider) Cleanup(ctx context.Context) error {
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
	if p.client != nil {
		if err := p.client.Disconnect(); err != nil {
			errs = append(errs, fmt.Errorf("disconnect from libvirt: %w", err))
		}
		p.client = nil
		p.pool = libvirt.StoragePool{}
		p.baseVolume = libvirt.StorageVol{}
		p.basePath = ""
		p.network = libvirt.Network{}
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
