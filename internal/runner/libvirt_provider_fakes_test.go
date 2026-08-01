package runner

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	libvirt "github.com/digitalocean/go-libvirt"
)

// fakeLibvirtRPCClient models the small libvirt state machine exercised by the
// provider tests. The aliases retain the fixture vocabulary used by the focused
// prepare and lifecycle tests while all calls use the generated RPC API.
type (
	prepareLibvirtRPC   = fakeLibvirtRPCClient
	lifecycleLibvirtRPC = fakeLibvirtRPCClient
	readinessLibvirtRPC = fakeLibvirtRPCClient
)

type fakeLibvirtRPCClient struct {
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

	failVerb   string
	failError  error
	failDelete bool

	domainName        string
	domainExists      bool
	domainRunning     bool
	volumeExists      bool
	domainDescription string
	domainDiskPath    string
	destroyFlags      []libvirt.DomainDestroyFlagsValues
	state             string
	disconnected      bool
}

func rpcMissing(code libvirt.ErrorNumber, message string) error {
	return libvirt.Error{Code: uint32(code), Message: message}
}

func (f *fakeLibvirtRPCClient) record(verb string) error {
	f.verbs = append(f.verbs, verb)
	if f.failVerb != verb {
		return nil
	}
	if f.failError != nil {
		return f.failError
	}
	return fmt.Errorf("injected %s failure", verb)
}

func (f *fakeLibvirtRPCClient) Disconnect() error {
	f.disconnected = true
	return nil
}

func (f *fakeLibvirtRPCClient) ConnectGetDomainCapabilities(
	_ libvirt.OptString,
	_ libvirt.OptString,
	_ libvirt.OptString,
	_ libvirt.OptString,
	_ libvirt.ConnectGetDomainCapabilitiesFlags,
) (string, error) {
	if err := f.record("domcapabilities"); err != nil {
		return "", err
	}
	domainType := "qemu"
	if f.kvm {
		domainType = "kvm"
	}
	return fmt.Sprintf("<domainCapabilities><domain>%s</domain></domainCapabilities>", domainType), nil
}

func (f *fakeLibvirtRPCClient) ConnectListAllDomains(
	_ int32,
	_ libvirt.ConnectListAllDomainsFlags,
) ([]libvirt.Domain, uint32, error) {
	if err := f.record("list"); err != nil {
		return nil, 0, err
	}
	if !f.domainExists {
		return nil, 0, nil
	}
	return []libvirt.Domain{{Name: f.domainName}}, 1, nil
}

func (f *fakeLibvirtRPCClient) DomainLookupByName(name string) (libvirt.Domain, error) {
	if err := f.record("dominfo"); err != nil {
		return libvirt.Domain{}, err
	}
	if !f.domainExists || (f.domainName != "" && name != f.domainName) {
		return libvirt.Domain{}, rpcMissing(libvirt.ErrNoDomain, "domain not found")
	}
	return libvirt.Domain{Name: name}, nil
}

func (f *fakeLibvirtRPCClient) DomainDefineXML(contents string) (libvirt.Domain, error) {
	if err := f.record("define"); err != nil {
		return libvirt.Domain{}, err
	}
	var description struct {
		Name string `xml:"name"`
	}
	if err := xml.Unmarshal([]byte(contents), &description); err != nil {
		return libvirt.Domain{}, err
	}
	f.domainName = description.Name
	f.domainExists = true
	return libvirt.Domain{Name: description.Name}, nil
}

func (f *fakeLibvirtRPCClient) DomainCreate(_ libvirt.Domain) error {
	if err := f.record("start"); err != nil {
		return err
	}
	f.domainExists = true
	f.domainRunning = true
	return nil
}

func (f *fakeLibvirtRPCClient) DomainGetState(
	_ libvirt.Domain,
	_ uint32,
) (int32, int32, error) {
	if err := f.record("domstate"); err != nil {
		return 0, 0, err
	}
	running := f.domainRunning
	if f.state != "" {
		running = strings.EqualFold(f.state, "running")
	}
	if running {
		return int32(libvirt.DomainRunning), 0, nil
	}
	return int32(libvirt.DomainShutoff), 0, nil
}

func (f *fakeLibvirtRPCClient) DomainGetXMLDesc(
	domain libvirt.Domain,
	_ libvirt.DomainXMLFlags,
) (string, error) {
	if err := f.record("dumpxml"); err != nil {
		return "", err
	}
	description := f.domainDescription
	if description == "" {
		const instanceTailLength = len("-20060102150405-abcdef")
		if len(domain.Name) <= instanceTailLength {
			return "", fmt.Errorf("invalid fixture domain name %q", domain.Name)
		}
		description = "managed-by:gitone:" + domain.Name[:len(domain.Name)-instanceTailLength]
	}
	diskPath := f.domainDiskPath
	if diskPath == "" {
		diskPath = filepath.Join(f.poolPath, domain.Name+".qcow2")
	}
	return fmt.Sprintf(
		"<domain><description>%s</description><devices><disk device='disk'><source file='%s'/></disk></devices></domain>",
		description,
		diskPath,
	), nil
}

func (f *fakeLibvirtRPCClient) DomainInterfaceAddresses(
	_ libvirt.Domain,
	_ uint32,
	_ uint32,
) ([]libvirt.DomainInterface, error) {
	if err := f.record("domifaddr"); err != nil {
		return nil, err
	}
	return nil, nil
}

func (f *fakeLibvirtRPCClient) DomainDestroyFlags(
	_ libvirt.Domain,
	flags libvirt.DomainDestroyFlagsValues,
) error {
	f.destroyFlags = append(f.destroyFlags, flags)
	if err := f.record("destroy"); err != nil {
		return err
	}
	f.domainRunning = false
	return nil
}

func (f *fakeLibvirtRPCClient) DomainUndefineFlags(
	_ libvirt.Domain,
	_ libvirt.DomainUndefineFlagsValues,
) error {
	if err := f.record("undefine"); err != nil {
		return err
	}
	f.domainExists = false
	f.domainRunning = false
	return nil
}

func (f *fakeLibvirtRPCClient) NetworkLookupByName(name string) (libvirt.Network, error) {
	if err := f.record("net-list"); err != nil {
		return libvirt.Network{}, err
	}
	if !f.networkDefined {
		return libvirt.Network{}, rpcMissing(libvirt.ErrNoNetwork, "network not found")
	}
	return libvirt.Network{Name: name}, nil
}

func (f *fakeLibvirtRPCClient) NetworkDefineXML(contents string) (libvirt.Network, error) {
	if err := f.record("net-define"); err != nil {
		return libvirt.Network{}, err
	}
	var description struct {
		Name string `xml:"name"`
	}
	if err := xml.Unmarshal([]byte(contents), &description); err != nil {
		return libvirt.Network{}, err
	}
	f.networkName = description.Name
	f.networkDefined = true
	return libvirt.Network{Name: description.Name}, nil
}

func (f *fakeLibvirtRPCClient) NetworkGetXMLDesc(
	_ libvirt.Network,
	_ uint32,
) (string, error) {
	if err := f.record("net-dumpxml"); err != nil {
		return "", err
	}
	if f.networkWrong {
		return fmt.Sprintf("<network><name>%s</name><forward mode='bridge'/></network>", f.networkName), nil
	}
	contents, err := renderLibvirtNetworkXML(f.networkName)
	return string(contents), err
}

func (f *fakeLibvirtRPCClient) NetworkIsActive(_ libvirt.Network) (int32, error) {
	if err := f.record("net-info"); err != nil {
		return 0, err
	}
	if f.networkActive {
		return 1, nil
	}
	return 0, nil
}

func (f *fakeLibvirtRPCClient) NetworkCreate(_ libvirt.Network) error {
	if err := f.record("net-start"); err != nil {
		return err
	}
	if f.concurrentStart {
		f.networkActive = true
		return errors.New("network is already active")
	}
	f.networkActive = true
	return nil
}

func (f *fakeLibvirtRPCClient) NetworkSetAutostart(_ libvirt.Network, _ int32) error {
	if err := f.record("net-autostart"); err != nil {
		return err
	}
	f.autostarted = true
	return nil
}

func (f *fakeLibvirtRPCClient) NetworkGetDhcpLeases(
	_ libvirt.Network,
	_ libvirt.OptString,
	_ int32,
	_ uint32,
) ([]libvirt.NetworkDhcpLease, uint32, error) {
	if err := f.record("net-dhcp-leases"); err != nil {
		return nil, 0, err
	}
	return nil, 0, nil
}

func (f *fakeLibvirtRPCClient) StoragePoolLookupByName(name string) (libvirt.StoragePool, error) {
	return libvirt.StoragePool{Name: name}, nil
}

func (f *fakeLibvirtRPCClient) StoragePoolIsActive(_ libvirt.StoragePool) (int32, error) {
	if err := f.record("pool-info"); err != nil {
		return 0, err
	}
	if f.poolActive {
		return 1, nil
	}
	return 0, nil
}

func (f *fakeLibvirtRPCClient) StoragePoolCreate(
	_ libvirt.StoragePool,
	_ libvirt.StoragePoolCreateFlags,
) error {
	if err := f.record("pool-start"); err != nil {
		return err
	}
	if f.concurrentPoolStart {
		f.poolActive = true
		return errors.New("storage pool is already active")
	}
	f.poolActive = true
	return nil
}

func (f *fakeLibvirtRPCClient) StoragePoolGetXMLDesc(
	pool libvirt.StoragePool,
	_ libvirt.StorageXMLFlags,
) (string, error) {
	if err := f.record("pool-dumpxml"); err != nil {
		return "", err
	}
	target := f.poolTarget
	if target == "" {
		target = f.poolPath
	}
	poolType := f.poolType
	if poolType == "" {
		poolType = "dir"
	}
	return fmt.Sprintf(
		"<pool type='%s'><name>%s</name><target><path>%s</path></target></pool>",
		poolType,
		pool.Name,
		target,
	), nil
}

func (f *fakeLibvirtRPCClient) StoragePoolRefresh(_ libvirt.StoragePool, _ uint32) error {
	return f.record("pool-refresh")
}

func (f *fakeLibvirtRPCClient) StoragePoolListAllVolumes(
	_ libvirt.StoragePool,
	_ int32,
	_ uint32,
) ([]libvirt.StorageVol, uint32, error) {
	if err := f.record("vol-list"); err != nil {
		return nil, 0, err
	}
	if !f.volumeExists {
		return nil, 0, nil
	}
	volume := libvirt.StorageVol{Pool: "test-pool", Name: f.domainName + ".qcow2"}
	return []libvirt.StorageVol{volume}, 1, nil
}

func (f *fakeLibvirtRPCClient) StorageVolLookupByName(
	pool libvirt.StoragePool,
	name string,
) (libvirt.StorageVol, error) {
	if err := f.record("vol-info"); err != nil {
		return libvirt.StorageVol{}, err
	}
	baseName := "flatcar-base.qcow2"
	if name == baseName {
		if _, err := os.Stat(filepath.Join(f.poolPath, name)); err == nil {
			return libvirt.StorageVol{Pool: pool.Name, Name: name}, nil
		}
	}
	if !f.volumeExists || name != f.domainName+".qcow2" {
		return libvirt.StorageVol{}, rpcMissing(libvirt.ErrNoStorageVol, "storage volume not found")
	}
	return libvirt.StorageVol{Pool: pool.Name, Name: name}, nil
}

func (f *fakeLibvirtRPCClient) StorageVolCreateXML(
	pool libvirt.StoragePool,
	contents string,
	_ libvirt.StorageVolCreateFlags,
) (libvirt.StorageVol, error) {
	if err := f.record("vol-create-as"); err != nil {
		return libvirt.StorageVol{}, err
	}
	var description libvirtVolumeDescription
	if err := xml.Unmarshal([]byte(contents), &description); err != nil {
		return libvirt.StorageVol{}, err
	}
	f.domainName = strings.TrimSuffix(description.Name, ".qcow2")
	f.volumeExists = true
	if err := os.WriteFile(filepath.Join(f.poolPath, description.Name), []byte("qcow2"), 0o600); err != nil {
		return libvirt.StorageVol{}, err
	}
	return libvirt.StorageVol{Pool: pool.Name, Name: description.Name}, nil
}

func (f *fakeLibvirtRPCClient) StorageVolGetXMLDesc(
	volume libvirt.StorageVol,
	_ uint32,
) (string, error) {
	if err := f.record("vol-dumpxml"); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"<volume><name>%s</name><capacity unit='bytes'>1073741824</capacity><target><format type='qcow2'/></target></volume>",
		volume.Name,
	), nil
}

func (f *fakeLibvirtRPCClient) StorageVolGetPath(volume libvirt.StorageVol) (string, error) {
	if err := f.record("vol-path"); err != nil {
		return "", err
	}
	return filepath.Join(f.poolPath, volume.Name), nil
}

func (f *fakeLibvirtRPCClient) StorageVolDelete(
	volume libvirt.StorageVol,
	_ libvirt.StorageVolDeleteFlags,
) error {
	if err := f.record("vol-delete"); err != nil {
		return err
	}
	if f.failDelete {
		return errors.New("injected volume deletion failure")
	}
	f.volumeExists = false
	if err := os.Remove(filepath.Join(f.poolPath, volume.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
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
