package runner

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"net"
	"strings"
	"testing"
)

func TestRenderFlatcarIgnitionConfiguresDockerAndSSH(t *testing.T) {
	contents, err := renderFlatcarIgnition(
		"warm-vm-1",
		"builder",
		"ssh-ed25519 AAAATEST runner@test",
		[]string{"https://mirror.example.test"},
		[]string{"registry.example.test:5000"},
	)
	if err != nil {
		t.Fatal(err)
	}
	var ignition libvirtIgnition
	if err = json.Unmarshal(contents, &ignition); err != nil {
		t.Fatalf("Ignition is not valid JSON: %v", err)
	}
	if ignition.Ignition.Version != flatcarIgnitionVersion {
		t.Fatalf("Ignition version = %q", ignition.Ignition.Version)
	}
	if len(ignition.Passwd.Users) != 1 {
		t.Fatalf("Ignition users = %#v", ignition.Passwd.Users)
	}
	user := ignition.Passwd.Users[0]
	if user.Name != "builder" || len(user.SSHAuthorizedKeys) != 1 ||
		user.SSHAuthorizedKeys[0] != "ssh-ed25519 AAAATEST runner@test" {
		t.Fatalf("Ignition user = %#v", user)
	}
	if strings.Join(user.Groups, ",") != "sudo,docker" {
		t.Fatalf("Ignition groups = %#v", user.Groups)
	}
	files := make(map[string]string, len(ignition.Storage.Files))
	for _, file := range ignition.Storage.Files {
		files[file.Path] = decodeIgnitionDataURL(t, file.Contents.Source)
		if file.Mode != 0o644 || !file.Overwrite {
			t.Fatalf("Ignition file = %#v", file)
		}
	}
	if files["/etc/hostname"] != "warm-vm-1\n" {
		t.Fatalf("hostname contents = %q", files["/etc/hostname"])
	}
	if files["/etc/flatcar/update.conf"] != "SERVER=disabled\n" {
		t.Fatalf("Flatcar update policy = %q", files["/etc/flatcar/update.conf"])
	}
	var daemonConfig libvirtDockerDaemonConfig
	if err = json.Unmarshal([]byte(files["/etc/docker/daemon.json"]), &daemonConfig); err != nil {
		t.Fatalf("Docker daemon config is invalid: %v", err)
	}
	if strings.Join(daemonConfig.RegistryMirrors, ",") != "https://mirror.example.test" ||
		strings.Join(daemonConfig.InsecureRegistries, ",") != "registry.example.test:5000" {
		t.Fatalf("Docker daemon config = %#v", daemonConfig)
	}
	if len(ignition.Systemd.Units) != 1 ||
		ignition.Systemd.Units[0].Name != "docker.service" ||
		!ignition.Systemd.Units[0].Enabled {
		t.Fatalf("Ignition units = %#v", ignition.Systemd.Units)
	}
}

func TestRenderFlatcarIgnitionOmitsEmptyDockerConfig(t *testing.T) {
	contents, err := renderFlatcarIgnition("warm-vm-2", "core", "ssh-ed25519 AAAATEST", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var ignition libvirtIgnition
	if err = json.Unmarshal(contents, &ignition); err != nil {
		t.Fatal(err)
	}
	if len(ignition.Storage.Files) != 2 || len(ignition.Passwd.Users[0].Groups) != 0 {
		t.Fatalf("Ignition = %#v", ignition)
	}
}

func decodeIgnitionDataURL(t *testing.T, source string) string {
	t.Helper()
	const prefix = "data:text/plain;charset=utf-8;base64,"
	encoded, found := strings.CutPrefix(source, prefix)
	if !found {
		t.Fatalf("Ignition source %q is not an inline data URL", source)
	}
	contents, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode Ignition source: %v", err)
	}
	return string(contents)
}

func TestRenderLibvirtDomainIsKVMOnlyAndEscaped(t *testing.T) {
	data := libvirtDomainTemplateData{
		Name:         "vm<&>",
		Description:  "managed-by:gitone:<test>",
		MemoryMiB:    2048,
		VCPUs:        2,
		DiskPath:     "/var/lib/libvirt/images/disk<&>.qcow2",
		MACAddress:   "52:54:00:01:02:03",
		NetworkName:  "network<&>",
		IgnitionPath: "/var/lib/libvirt/images/config<&>.ign",
	}
	contents, err := renderLibvirtDomainXML(data)
	if err != nil {
		t.Fatal(err)
	}
	var domain struct {
		XMLName     xml.Name `xml:"domain"`
		Type        string   `xml:"type,attr"`
		Name        string   `xml:"name"`
		Description string   `xml:"description"`
		CPU         struct {
			Mode string `xml:"mode,attr"`
		} `xml:"cpu"`
		Devices struct {
			Disks []struct {
				Source struct {
					File string `xml:"file,attr"`
				} `xml:"source"`
				Target struct {
					Bus string `xml:"bus,attr"`
				} `xml:"target"`
			} `xml:"disk"`
			Interfaces []struct {
				Source struct {
					Network string `xml:"network,attr"`
				} `xml:"source"`
				Model struct {
					Type string `xml:"type,attr"`
				} `xml:"model"`
				Port struct {
					Isolated string `xml:"isolated,attr"`
				} `xml:"port"`
			} `xml:"interface"`
			Filesystems []struct{} `xml:"filesystem"`
		} `xml:"devices"`
	}
	if err = xml.Unmarshal(contents, &domain); err != nil {
		t.Fatalf("domain XML is invalid: %v\n%s", err, contents)
	}
	if domain.Type != "kvm" || domain.Name != data.Name || domain.Description != data.Description ||
		domain.CPU.Mode != "host-passthrough" {
		t.Fatalf("domain = %#v", domain)
	}
	if len(domain.Devices.Disks) != 1 || domain.Devices.Disks[0].Source.File != data.DiskPath ||
		domain.Devices.Disks[0].Target.Bus != "virtio" {
		t.Fatalf("domain disks = %#v", domain.Devices.Disks)
	}
	if len(domain.Devices.Interfaces) != 1 ||
		domain.Devices.Interfaces[0].Source.Network != data.NetworkName ||
		domain.Devices.Interfaces[0].Model.Type != "virtio" ||
		domain.Devices.Interfaces[0].Port.Isolated != "yes" {
		t.Fatalf("domain interfaces = %#v", domain.Devices.Interfaces)
	}
	if len(domain.Devices.Filesystems) != 0 || strings.Contains(string(contents), "type='qemu'") {
		t.Fatalf("domain permits a non-KVM or host-filesystem path:\n%s", contents)
	}
	if !strings.Contains(string(contents), flatcarIgnitionFWCfgName) ||
		!strings.Contains(string(contents), "suspend-to-mem enabled='no'") {
		t.Fatalf("domain omits Ignition or power isolation:\n%s", contents)
	}
}

func TestRenderLibvirtNetworkIsDeterministicPrivateNAT(t *testing.T) {
	first, err := renderLibvirtNetworkXML("gitone-warm")
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderLibvirtNetworkXML("gitone-warm")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("network rendering is not deterministic")
	}
	var network libvirtNetworkDescription
	if err = xml.Unmarshal(first, &network); err != nil {
		t.Fatalf("network XML is invalid: %v", err)
	}
	expected := libvirtNetworkTemplateFor("gitone-warm")
	if network.Name != expected.Name || network.Forward.Mode != "nat" ||
		network.Port.Isolated != "yes" ||
		network.Bridge.Name != expected.BridgeName || network.IP.Address != expected.Gateway ||
		network.IP.Netmask != "255.255.240.0" ||
		network.IP.DHCP.Range.Start != expected.DHCPStart ||
		network.IP.DHCP.Range.End != expected.DHCPEnd ||
		network.IP.DHCP.Range.Lease.Expiry != libvirtDHCPLeaseMinutes ||
		network.IP.DHCP.Range.Lease.Unit != "minutes" {
		t.Fatalf("network = %#v, expected %#v", network, expected)
	}
	if address := net.ParseIP(network.IP.Address); address == nil || !address.IsPrivate() {
		t.Fatalf("network gateway %q is not private", network.IP.Address)
	}
	if len(network.Bridge.Name) > 15 {
		t.Fatalf("bridge name %q exceeds Linux interface limit", network.Bridge.Name)
	}

	custom, err := renderLibvirtNetworkXMLForCIDR("gitone-warm", "10.240.0.0/20")
	if err != nil {
		t.Fatal(err)
	}
	if err = xml.Unmarshal(custom, &network); err != nil {
		t.Fatal(err)
	}
	if network.IP.Address != "10.240.0.1" ||
		network.IP.DHCP.Range.Start != "10.240.0.2" ||
		network.IP.DHCP.Range.End != "10.240.15.254" {
		t.Fatalf("custom network range = %#v", network.IP)
	}
	if _, err = renderLibvirtNetworkXMLForCIDR("gitone-warm", "10.240.1.0/20"); err == nil {
		t.Fatal("network renderer accepted a misaligned CIDR")
	}
}
