package runner

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	libvirt "github.com/digitalocean/go-libvirt"
)

// These focused interfaces compose the subset of DigitalOcean's generated
// libvirt RPC API used by the runner. They keep the provider independently
// testable and insulate the rest of the runner from the generated API.
type libvirtConnectionRPC interface {
	Disconnect() error
	ConnectGetDomainCapabilities(
		libvirt.OptString,
		libvirt.OptString,
		libvirt.OptString,
		libvirt.OptString,
		libvirt.ConnectGetDomainCapabilitiesFlags,
	) (string, error)
	ConnectListAllDomains(
		int32,
		libvirt.ConnectListAllDomainsFlags,
	) ([]libvirt.Domain, uint32, error)
}

type libvirtDomainRPC interface {
	DomainLookupByName(string) (libvirt.Domain, error)
	DomainDefineXML(string) (libvirt.Domain, error)
	DomainCreate(libvirt.Domain) error
	DomainGetState(libvirt.Domain, uint32) (int32, int32, error)
	DomainGetXMLDesc(libvirt.Domain, libvirt.DomainXMLFlags) (string, error)
	DomainInterfaceAddresses(libvirt.Domain, uint32, uint32) ([]libvirt.DomainInterface, error)
	DomainDestroyFlags(libvirt.Domain, libvirt.DomainDestroyFlagsValues) error
	DomainUndefineFlags(libvirt.Domain, libvirt.DomainUndefineFlagsValues) error
}

type libvirtNetworkRPC interface {
	NetworkLookupByName(string) (libvirt.Network, error)
	NetworkDefineXML(string) (libvirt.Network, error)
	NetworkGetXMLDesc(libvirt.Network, uint32) (string, error)
	NetworkIsActive(libvirt.Network) (int32, error)
	NetworkCreate(libvirt.Network) error
	NetworkSetAutostart(libvirt.Network, int32) error
	NetworkGetDhcpLeases(
		libvirt.Network,
		libvirt.OptString,
		int32,
		uint32,
	) ([]libvirt.NetworkDhcpLease, uint32, error)
}

type libvirtStoragePoolRPC interface {
	StoragePoolLookupByName(string) (libvirt.StoragePool, error)
	StoragePoolIsActive(libvirt.StoragePool) (int32, error)
	StoragePoolCreate(libvirt.StoragePool, libvirt.StoragePoolCreateFlags) error
	StoragePoolGetXMLDesc(libvirt.StoragePool, libvirt.StorageXMLFlags) (string, error)
	StoragePoolRefresh(libvirt.StoragePool, uint32) error
	StoragePoolListAllVolumes(libvirt.StoragePool, int32, uint32) ([]libvirt.StorageVol, uint32, error)
}

type libvirtStorageVolumeRPC interface {
	StorageVolLookupByName(libvirt.StoragePool, string) (libvirt.StorageVol, error)
	StorageVolCreateXML(
		libvirt.StoragePool,
		string,
		libvirt.StorageVolCreateFlags,
	) (libvirt.StorageVol, error)
	StorageVolGetXMLDesc(libvirt.StorageVol, uint32) (string, error)
	StorageVolGetPath(libvirt.StorageVol) (string, error)
	StorageVolDelete(libvirt.StorageVol, libvirt.StorageVolDeleteFlags) error
}

type libvirtRPCClient interface {
	libvirtConnectionRPC
	libvirtDomainRPC
	libvirtNetworkRPC
	libvirtStoragePoolRPC
	libvirtStorageVolumeRPC
}

type libvirtRPCConnector func(context.Context, string) (libvirtRPCClient, error)

func connectLibvirtRPC(ctx context.Context, uri string) (libvirtRPCClient, error) {
	if err := context.Cause(ctx); err != nil {
		return nil, err
	}
	parsedURI, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("parse libvirt URI: %w", err)
	}
	client, err := libvirt.ConnectToURI(parsedURI)
	if err != nil {
		return nil, err
	}
	if err = context.Cause(ctx); err != nil {
		return nil, errors.Join(err, client.Disconnect())
	}
	return client, nil
}

// callLibvirtRPC preserves the provider's context contract around generated RPC
// methods, which do not accept a context themselves. A call already in flight
// cannot be cancelled without disconnecting every request on the shared socket,
// so cancellation is observed immediately before and after each RPC.
func callLibvirtRPC(ctx context.Context, call func() error) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	if err := call(); err != nil {
		return err
	}
	return context.Cause(ctx)
}

func libvirtResourceAbsent(err error) bool {
	if err == nil {
		return false
	}
	var rpcErr libvirt.Error
	if !errors.As(err, &rpcErr) {
		return false
	}
	switch libvirt.ErrorNumber(rpcErr.Code) {
	case libvirt.ErrNoDomain,
		libvirt.ErrNoNetwork,
		libvirt.ErrNoStoragePool,
		libvirt.ErrNoStorageVol:
		return true
	default:
		return false
	}
}
