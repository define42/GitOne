package runner

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net"
	"strings"
	"text/template"
)

// The libvirt layout is inspired by the public-domain Fleeting libvirt plugin:
// https://gitlab.com/fleetingplugin/fleeting-plugin-libvirt
const (
	flatcarIgnitionVersion   = "3.3.0"
	flatcarIgnitionFWCfgName = "opt/org.flatcar-linux/config"
	libvirtDHCPLeaseMinutes  = 5
)

type libvirtIgnition struct {
	Ignition struct {
		Version string `json:"version"`
	} `json:"ignition"`
	Passwd struct {
		Users []libvirtIgnitionUser `json:"users"`
	} `json:"passwd"`
	Storage struct {
		Files []libvirtIgnitionFile `json:"files,omitempty"`
	} `json:"storage"`
	Systemd struct {
		Units []libvirtIgnitionUnit `json:"units"`
	} `json:"systemd"`
}

type libvirtIgnitionUser struct {
	Name              string   `json:"name"`
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys"`
	Groups            []string `json:"groups,omitempty"`
}

type libvirtIgnitionFile struct {
	Path      string                      `json:"path"`
	Mode      int                         `json:"mode"`
	Overwrite bool                        `json:"overwrite"`
	Contents  libvirtIgnitionFileContents `json:"contents"`
}

type libvirtIgnitionFileContents struct {
	Source string `json:"source"`
}

type libvirtIgnitionUnit struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type libvirtDockerDaemonConfig struct {
	RegistryMirrors    []string `json:"registry-mirrors,omitempty"`
	InsecureRegistries []string `json:"insecure-registries,omitempty"`
}

func renderFlatcarIgnition(
	hostname string,
	username string,
	publicKey string,
	registryMirrors []string,
	insecureRegistries []string,
) ([]byte, error) {
	config := libvirtIgnition{}
	config.Ignition.Version = flatcarIgnitionVersion
	user := libvirtIgnitionUser{
		Name:              username,
		SSHAuthorizedKeys: []string{strings.TrimSpace(publicKey)},
	}
	if username != "core" {
		user.Groups = []string{"sudo", "docker"}
	}
	config.Passwd.Users = []libvirtIgnitionUser{user}
	config.Storage.Files = []libvirtIgnitionFile{
		newLibvirtIgnitionFile("/etc/hostname", []byte(hostname+"\n")),
		newLibvirtIgnitionFile("/etc/flatcar/update.conf", []byte("SERVER=disabled\n")),
	}

	dockerConfig := libvirtDockerDaemonConfig{
		RegistryMirrors:    append([]string(nil), registryMirrors...),
		InsecureRegistries: append([]string(nil), insecureRegistries...),
	}
	if len(dockerConfig.RegistryMirrors) > 0 || len(dockerConfig.InsecureRegistries) > 0 {
		contents, err := json.MarshalIndent(dockerConfig, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("render docker daemon configuration: %w", err)
		}
		contents = append(contents, '\n')
		config.Storage.Files = append(
			config.Storage.Files,
			newLibvirtIgnitionFile("/etc/docker/daemon.json", contents),
		)
	}
	config.Systemd.Units = []libvirtIgnitionUnit{{Name: "docker.service", Enabled: true}}

	contents, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render Flatcar Ignition: %w", err)
	}
	return contents, nil
}

func newLibvirtIgnitionFile(path string, contents []byte) libvirtIgnitionFile {
	return libvirtIgnitionFile{
		Path:      path,
		Mode:      0o644,
		Overwrite: true,
		Contents: libvirtIgnitionFileContents{
			Source: "data:text/plain;charset=utf-8;base64," + base64.StdEncoding.EncodeToString(contents),
		},
	}
}

type libvirtDomainTemplateData struct {
	Name         string
	Description  string
	MemoryMiB    int
	VCPUs        int
	DiskPath     string
	MACAddress   string
	NetworkName  string
	IgnitionPath string
}

const libvirtDomainTemplateText = `
<domain type='kvm' xmlns:qemu='http://libvirt.org/schemas/domain/qemu/1.0'>
  <name>{{xml .Name}}</name>
  <description>{{xml .Description}}</description>
  <memory unit='MiB'>{{.MemoryMiB}}</memory>
  <currentMemory unit='MiB'>{{.MemoryMiB}}</currentMemory>
  <vcpu placement='static'>{{.VCPUs}}</vcpu>
  <os>
    <type arch='x86_64'>hvm</type>
  </os>
  <features>
    <acpi/>
    <apic/>
  </features>
  <cpu mode='host-passthrough' check='none' migratable='off'/>
  <clock offset='utc'>
    <timer name='rtc' tickpolicy='catchup'/>
    <timer name='pit' tickpolicy='delay'/>
    <timer name='hpet' present='no'/>
  </clock>
  <pm>
    <suspend-to-mem enabled='no'/>
    <suspend-to-disk enabled='no'/>
  </pm>
  <on_poweroff>destroy</on_poweroff>
  <on_reboot>restart</on_reboot>
  <on_crash>destroy</on_crash>
  <devices>
    <disk type='file' device='disk'>
      <driver name='qemu' type='qcow2' cache='none' discard='unmap'/>
      <source file='{{xml .DiskPath}}'/>
      <target dev='vda' bus='virtio'/>
    </disk>
    <interface type='network'>
      <mac address='{{xml .MACAddress}}'/>
      <source network='{{xml .NetworkName}}'/>
	  <port isolated='yes'/>
      <model type='virtio'/>
    </interface>
    <serial type='pty'>
      <target port='0'/>
    </serial>
    <console type='pty'>
      <target type='serial' port='0'/>
    </console>
    <rng model='virtio'>
      <backend model='random'>/dev/urandom</backend>
    </rng>
  </devices>
  <qemu:commandline>
    <qemu:arg value='-fw_cfg'/>
    <qemu:arg value='name=` + flatcarIgnitionFWCfgName + `,file={{xml .IgnitionPath}}'/>
  </qemu:commandline>
</domain>`

func renderLibvirtDomainXML(data libvirtDomainTemplateData) ([]byte, error) {
	domainTemplate, err := template.New("libvirt-domain").Funcs(template.FuncMap{
		"xml": escapeLibvirtXML,
	}).Parse(strings.TrimSpace(libvirtDomainTemplateText))
	if err != nil {
		return nil, fmt.Errorf("parse libvirt domain XML template: %w", err)
	}
	var output bytes.Buffer
	if err = domainTemplate.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("render libvirt domain XML: %w", err)
	}
	return output.Bytes(), nil
}

type libvirtNetworkTemplateData struct {
	Name         string
	BridgeName   string
	Gateway      string
	Netmask      string
	DHCPStart    string
	DHCPEnd      string
	LeaseMinutes int
}

const libvirtNetworkTemplateText = `
<network>
  <name>{{xml .Name}}</name>
  <forward mode='nat'/>
  <bridge name='{{xml .BridgeName}}' stp='on' delay='0'/>
  <port isolated='yes'/>
  <ip address='{{xml .Gateway}}' netmask='{{xml .Netmask}}'>
    <dhcp>
      <range start='{{xml .DHCPStart}}' end='{{xml .DHCPEnd}}'>
        <lease expiry='{{.LeaseMinutes}}' unit='minutes'/>
      </range>
    </dhcp>
  </ip>
</network>`

func defaultLibvirtNetworkCIDR(name string) string {
	digest := sha256.Sum256([]byte(name))
	secondOctet := 16 + int(digest[4]&0x0f)
	thirdOctet := int(digest[5] & 0xf0)
	return fmt.Sprintf("172.%d.%d.0/20", secondOctet, thirdOctet)
}

func normalizeLibvirtNetworkCIDR(value string, networkName string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultLibvirtNetworkCIDR(networkName)
	}
	address, network, err := net.ParseCIDR(value)
	if err != nil || address.To4() == nil {
		return "", fmt.Errorf("libvirt network CIDR %q must be an IPv4 /20", value)
	}
	ones, bits := network.Mask.Size()
	if bits != 32 || ones != 20 || !address.Equal(network.IP) {
		return "", fmt.Errorf("libvirt network CIDR %q must be an aligned IPv4 /20", value)
	}
	if !network.IP.IsPrivate() {
		return "", fmt.Errorf("libvirt network CIDR %q must use private address space", value)
	}
	return network.String(), nil
}

func libvirtNetworkTemplateForCIDR(name string, cidr string) (libvirtNetworkTemplateData, error) {
	normalized, err := normalizeLibvirtNetworkCIDR(cidr, name)
	if err != nil {
		return libvirtNetworkTemplateData{}, err
	}
	_, network, _ := net.ParseCIDR(normalized)
	base := binary.BigEndian.Uint32(network.IP.To4())
	address := func(offset uint32) string {
		value := make(net.IP, net.IPv4len)
		binary.BigEndian.PutUint32(value, base+offset)
		return value.String()
	}
	digest := sha256.Sum256([]byte(name))
	return libvirtNetworkTemplateData{
		Name:         name,
		BridgeName:   fmt.Sprintf("virbr%x", digest[:5]),
		Gateway:      address(1),
		Netmask:      "255.255.240.0",
		DHCPStart:    address(2),
		DHCPEnd:      address((1 << 12) - 2),
		LeaseMinutes: libvirtDHCPLeaseMinutes,
	}, nil
}

func libvirtNetworkTemplateFor(name string) libvirtNetworkTemplateData {
	result, _ := libvirtNetworkTemplateForCIDR(name, "")
	return result
}

func renderLibvirtNetworkXML(name string) ([]byte, error) {
	return renderLibvirtNetworkXMLForCIDR(name, "")
}

func renderLibvirtNetworkXMLForCIDR(name string, cidr string) ([]byte, error) {
	networkTemplate, err := template.New("libvirt-network").Funcs(template.FuncMap{
		"xml": escapeLibvirtXML,
	}).Parse(strings.TrimSpace(libvirtNetworkTemplateText))
	if err != nil {
		return nil, fmt.Errorf("parse libvirt network XML template: %w", err)
	}
	var output bytes.Buffer
	data, err := libvirtNetworkTemplateForCIDR(name, cidr)
	if err != nil {
		return nil, err
	}
	if err = networkTemplate.Execute(&output, data); err != nil {
		return nil, fmt.Errorf("render libvirt network XML: %w", err)
	}
	return output.Bytes(), nil
}

func escapeLibvirtXML(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}
