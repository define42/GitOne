package githttp

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/protocol"
	"github.com/go-git/go-git/v6/plumbing/protocol/capability"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/plumbing/transport"
	gitstorage "github.com/go-git/go-git/v6/storage"
)

const (
	gitSHA256ObjectFormat = "sha256"
	gitSHA256ObjectIDSize = 32
	gitSHA256ZeroObjectID = "0000000000000000000000000000000000000000000000000000000000000000"
)

// advertiseSHA256ReceivePack emits the v0/v1 receive-pack advertisement that
// go-git currently emits, with object-format=sha256 added. Receive-pack has no
// protocol-v2 service, so a v2 request falls back to a v0 advertisement and,
// like git-http-backend, omits the smart HTTP service prefix.
func advertiseSHA256ReceivePack(
	storage gitstorage.Storer,
	gitProtocol string,
	w io.Writer,
) error {
	version := transport.ProtocolVersion(gitProtocol)
	adv := &packp.AdvRefs{}
	adv.Capabilities.Set(capability.Agent, capability.DefaultAgent())
	adv.Capabilities.Set(capability.OFSDelta)
	adv.Capabilities.Set(capability.Sideband64k)
	adv.Capabilities.Set(capability.NoThin)
	adv.Capabilities.Set(capability.DeleteRefs)
	adv.Capabilities.Set(capability.ReportStatus)
	adv.Capabilities.Set(capability.PushOptions)
	adv.Capabilities.Set(capability.Quiet)
	adv.Capabilities.Set(capability.ObjectFormat, gitSHA256ObjectFormat)

	if err := addReceiveAdvertisementReferences(storage, adv); err != nil {
		return err
	}
	if err := capability.Validate(&adv.Capabilities); err != nil {
		return fmt.Errorf("invalid receive-pack capabilities: %w", err)
	}

	if version != protocol.V2 {
		if err := (&packp.SmartReply{Service: transport.ReceivePackService}).Encode(w); err != nil {
			return fmt.Errorf("encode receive-pack smart reply: %w", err)
		}
	}
	if version == protocol.V1 {
		adv.Version = protocol.V1
	}
	return encodeSHA256ReceiveAdvertisement(w, adv)
}

func addReceiveAdvertisementReferences(storage gitstorage.Storer, adv *packp.AdvRefs) error {
	iter, err := storage.IterReferences()
	if err != nil {
		return err
	}
	return iter.ForEach(func(reference *plumbing.Reference) error {
		hash := reference.Hash()
		if reference.Type() == plumbing.SymbolicReference {
			resolved, resolveErr := storer.ResolveReference(storage, reference.Target())
			if errors.Is(resolveErr, plumbing.ErrReferenceNotFound) {
				return nil
			}
			if resolveErr != nil {
				return resolveErr
			}
			hash = resolved.Hash()
		}
		if reference.Name() == plumbing.HEAD {
			return nil
		}
		if err := validateSHA256ObjectID(reference.Name(), "advertised", hash); err != nil {
			return err
		}

		adv.References = append(adv.References,
			plumbing.NewHashReference(reference.Name(), hash),
		)
		if reference.Name().IsTag() {
			if tag, tagErr := object.GetTag(storage, hash); tagErr == nil {
				if err := validateSHA256ObjectID(reference.Name(), "peeled", tag.Target); err != nil {
					return err
				}
				adv.References = append(adv.References, plumbing.NewHashReference(
					plumbing.ReferenceName(reference.Name().String()+"^{}"),
					tag.Target,
				))
			}
		}
		return nil
	})
}

func encodeSHA256ReceiveAdvertisement(w io.Writer, adv *packp.AdvRefs) error {
	switch adv.Version {
	case protocol.V0:
	case protocol.V1:
		if _, err := pktline.Writef(w, "version %d\n", adv.Version); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported receive advertisement protocol version: %d", adv.Version)
	}

	references, peeled := receiveAdvertisementReferences(adv.References)
	if len(references) == 0 {
		if _, err := pktline.Writef(
			w,
			"%s capabilities^{}\x00%s\n",
			gitSHA256ZeroObjectID,
			adv.Capabilities.String(),
		); err != nil {
			return err
		}
	} else {
		for index, reference := range references {
			if index == 0 {
				if _, err := pktline.Writef(
					w,
					"%s %s\x00%s\n",
					reference.Hash(),
					reference.Name(),
					adv.Capabilities.String(),
				); err != nil {
					return err
				}
			} else if _, err := pktline.Writef(
				w,
				"%s %s\n",
				reference.Hash(),
				reference.Name(),
			); err != nil {
				return err
			}
			if target, ok := peeled[reference.Name()]; ok {
				if _, err := pktline.Writef(
					w,
					"%s %s^{}\n",
					target,
					reference.Name(),
				); err != nil {
					return err
				}
			}
		}
	}
	shallows := make([]string, len(adv.Shallows))
	for index, shallow := range adv.Shallows {
		shallows[index] = shallow.String()
	}
	sort.Strings(shallows)
	for _, shallow := range shallows {
		if _, err := pktline.Writef(w, "shallow %s\n", shallow); err != nil {
			return err
		}
	}
	return pktline.WriteFlush(w)
}

func receiveAdvertisementReferences(
	references []*plumbing.Reference,
) ([]*plumbing.Reference, map[plumbing.ReferenceName]plumbing.Hash) {
	ordinary := make([]*plumbing.Reference, 0, len(references))
	peeled := make(map[plumbing.ReferenceName]plumbing.Hash)
	for _, reference := range references {
		name := reference.Name().String()
		if base, ok := strings.CutSuffix(name, "^{}"); ok {
			peeled[plumbing.ReferenceName(base)] = reference.Hash()
			continue
		}
		ordinary = append(ordinary, reference)
	}
	sort.Slice(ordinary, func(left, right int) bool {
		return ordinary[left].Name() < ordinary[right].Name()
	})
	for index, reference := range ordinary {
		if reference.Name() == plumbing.HEAD {
			ordinary[0], ordinary[index] = ordinary[index], ordinary[0]
			break
		}
	}
	return ordinary, peeled
}

func validateReceiveObjectIDs(request *packp.UpdateRequests) error {
	if len(request.Shallows) != 0 {
		return errors.New("shallow pushes are not supported")
	}
	for _, command := range request.Commands {
		if command == nil {
			return errors.New("receive command is missing")
		}
		if err := validateSHA256ObjectID(command.Name, "old", command.Old); err != nil {
			return err
		}
		if err := validateSHA256ObjectID(command.Name, "new", command.New); err != nil {
			return err
		}
	}
	return nil
}

func validateSHA256ObjectID(
	reference plumbing.ReferenceName,
	field string,
	objectID plumbing.Hash,
) error {
	if objectID.Size() != gitSHA256ObjectIDSize {
		return fmt.Errorf(
			"%s %s object ID must use SHA-256",
			reference,
			field,
		)
	}
	return nil
}
