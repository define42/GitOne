package storage

import (
	"context"
	"fmt"

	"github.com/define42/GitOne/internal/gitformat"
	gitclient "github.com/go-git/go-git/v6/plumbing/client"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
	"github.com/go-git/go-git/v6/plumbing/protocol"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	gittransport "github.com/go-git/go-git/v6/plumbing/transport"
)

// preflightRemoteObjectFormat performs only upload-pack discovery. It never
// requests refs or a packfile, which lets strict FIPS mode reject a legacy
// remote before go-git enters an object hashing path.
func preflightRemoteObjectFormat(
	ctx context.Context,
	rawURL string,
	clientOptions []gitclient.Option,
) (_ formatcfg.ObjectFormat, retErr error) {
	remoteURL, err := gittransport.ParseURL(rawURL)
	if err != nil {
		return formatcfg.UnsetObjectFormat, err
	}
	client := gitclient.New(clientOptions...)
	defer func() {
		if err := client.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()

	session, err := client.Handshake(ctx, &gittransport.Request{
		URL:      remoteURL,
		Command:  gittransport.UploadPackService,
		Protocol: protocol.V2,
	})
	if err != nil {
		return formatcfg.UnsetObjectFormat, err
	}
	defer func() {
		if err := session.Close(); err != nil && retErr == nil {
			retErr = err
		}
	}()

	objectFormat, err := advertisedRemoteObjectFormat(session.Capabilities())
	if err != nil {
		return formatcfg.UnsetObjectFormat, err
	}
	if objectFormat == formatcfg.SHA1 {
		if err := gitformat.RequireLegacySHA1(); err != nil {
			return formatcfg.UnsetObjectFormat, fmt.Errorf(
				"remote uses legacy SHA-1 objects: %w", err,
			)
		}
	}
	return objectFormat, nil
}

func advertisedRemoteObjectFormat(caps *capability.List) (formatcfg.ObjectFormat, error) {
	if caps == nil || !caps.Supports(capability.ObjectFormat) {
		// Git protocol v0/v1 and v2 both define an omitted object-format as
		// the original SHA-1 format.
		return formatcfg.SHA1, nil
	}
	objectFormats := caps.Get(capability.ObjectFormat)
	var objectFormat formatcfg.ObjectFormat
	switch len(objectFormats) {
	case 1:
		switch formatcfg.ObjectFormat(objectFormats[0]) {
		case formatcfg.SHA1:
			objectFormat = formatcfg.SHA1
		case formatcfg.SHA256:
			objectFormat = formatcfg.SHA256
		default:
			return formatcfg.UnsetObjectFormat, fmt.Errorf(
				"remote advertised unsupported object-format %q", objectFormats[0],
			)
		}
	default:
		return formatcfg.UnsetObjectFormat, fmt.Errorf(
			"remote advertised %d object-format values; exactly one is required",
			len(objectFormats),
		)
	}
	return objectFormat, nil
}
