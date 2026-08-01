package gitformat

import (
	"crypto"
	"crypto/sha1" // #nosec G505 -- required only to validate a legacy Git import.
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
	plumbinghash "github.com/go-git/go-git/v6/plumbing/hash"
)

const maxStructuredObjectSize = int64(64 << 20)

type sourceReference struct {
	name   plumbing.ReferenceName
	typ    plumbing.ReferenceType
	hash   plumbing.Hash
	target plumbing.ReferenceName
}

type convertedObject struct {
	hash plumbing.Hash
	typ  plumbing.ObjectType
}

type objectConverter struct {
	source    *git.Repository
	target    *git.Repository
	converted map[string]convertedObject
	visiting  map[string]bool
}

// Initialize the go-git hash override while packages are still being
// initialized, before application goroutines can concurrently read go-git's
// internal hash registry. RegisterHash intentionally has no synchronization.
// Registration itself does not hash anything, so strict FIPS-only mode also
// receives the standard-library implementation: any missed legacy path then
// fails under Go's diagnostic instead of falling back to sha1cd.
//
//nolint:gochecknoglobals // Package-init state is required to register SHA-1 before concurrent go-git use.
var legacySHA1ProbeErr, legacySHA1RegistrationErr = initializeLegacySHA1()

// ConvertSHA1Repository performs a one-way conversion of all objects reachable
// from source refs into a fresh, bare SHA-256 repository at destinationPath.
// The source is never modified. destinationPath must not already exist and is
// removed if any validation or conversion step fails.
func ConvertSHA1Repository(sourcePath, destinationPath string) (_ *git.Repository, retErr error) {
	gitDir, err := sourceGitDirectory(sourcePath)
	if err != nil {
		return nil, err
	}
	objectFormat, err := detectObjectFormatInGitDir(gitDir)
	if err != nil {
		return nil, fmt.Errorf("read source object format: %w", err)
	}
	if objectFormat != formatcfg.SHA1 {
		return nil, fmt.Errorf("source repository uses %s, want sha1", objectFormat)
	}
	if err := ensureLegacySHA1Available(); err != nil {
		return nil, err
	}
	if err := rejectUnsupportedSourceFiles(gitDir); err != nil {
		return nil, err
	}

	source, err := git.PlainOpen(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open source repository: %w", err)
	}
	defer func() { _ = source.Close() }()

	objectFormat, err = ObjectFormat(source)
	if err != nil {
		return nil, fmt.Errorf("read source object format: %w", err)
	}
	if objectFormat != formatcfg.SHA1 {
		return nil, fmt.Errorf("source repository uses %s, want sha1", objectFormat)
	}
	if err := rejectPartialClone(source); err != nil {
		return nil, err
	}
	refs, err := sourceReferences(source)
	if err != nil {
		return nil, err
	}

	destinationPath, err = filepath.Abs(destinationPath)
	if err != nil {
		return nil, fmt.Errorf("resolve destination path: %w", err)
	}
	if _, err := os.Lstat(destinationPath); err == nil {
		return nil, fmt.Errorf("destination already exists: %s", destinationPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect destination: %w", err)
	}
	if err := os.Mkdir(destinationPath, 0o700); err != nil {
		return nil, fmt.Errorf("create destination: %w", err)
	}

	var target *git.Repository
	defer func() {
		if retErr == nil {
			return
		}
		if target != nil {
			_ = target.Close()
		}
		if err := os.RemoveAll(destinationPath); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("remove failed conversion destination: %w", err))
		}
	}()

	target, err = Init(destinationPath, true)
	if err != nil {
		return nil, fmt.Errorf("initialize SHA-256 destination: %w", err)
	}

	converter := &objectConverter{
		source:    source,
		target:    target,
		converted: make(map[string]convertedObject),
		visiting:  make(map[string]bool),
	}
	convertedRefs := make([]*plumbing.Reference, 0, len(refs))
	for _, ref := range refs {
		switch ref.typ {
		case plumbing.HashReference:
			converted, err := converter.convert(ref.hash, plumbing.AnyObject)
			if err != nil {
				return nil, fmt.Errorf("convert reference %s: %w", ref.name, err)
			}
			convertedRefs = append(convertedRefs, plumbing.NewHashReference(ref.name, converted))
		case plumbing.SymbolicReference:
			convertedRefs = append(convertedRefs, plumbing.NewSymbolicReference(ref.name, ref.target))
		default:
			return nil, fmt.Errorf("reference %s has unsupported type %s", ref.name, ref.typ)
		}
	}

	// Hash refs are installed before symbolic refs so every non-HEAD symbolic
	// target is already present when the conversion becomes visible.
	for _, typ := range []plumbing.ReferenceType{plumbing.HashReference, plumbing.SymbolicReference} {
		for _, ref := range convertedRefs {
			if ref.Type() != typ {
				continue
			}
			if err := target.Storer.SetReference(ref); err != nil {
				return nil, fmt.Errorf("write converted reference %s: %w", ref.Name(), err)
			}
		}
	}
	if err := Validate(target); err != nil {
		return nil, fmt.Errorf("validate converted repository: %w", err)
	}
	return target, nil
}

func ensureLegacySHA1Available() error {
	if legacySHA1ProbeErr != nil {
		return fmt.Errorf(
			"%w: strict FIPS-only mode forbids validation of SHA-1 Git objects: %w",
			ErrLegacySHA1Unavailable, legacySHA1ProbeErr,
		)
	}
	if legacySHA1RegistrationErr != nil {
		return fmt.Errorf("register standard-library SHA-1 with go-git: %w", legacySHA1RegistrationErr)
	}
	return nil
}

func initializeLegacySHA1() (probeErr, registrationErr error) {
	registrationErr = plumbinghash.RegisterHash(crypto.SHA1, sha1.New)
	h := sha1.New() // #nosec G401 -- availability probe for legacy import only.
	if _, err := h.Write(nil); err != nil {
		return err, registrationErr
	}
	return nil, registrationErr
}

// RequireLegacySHA1 verifies that the standard-library SHA-1 implementation
// needed to inspect a legacy Git repository is available. It fails closed in
// Go's strict FIPS-only diagnostic mode and never falls back to an alternate
// SHA-1 implementation.
func RequireLegacySHA1() error {
	return ensureLegacySHA1Available()
}

func sourceGitDirectory(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve source path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect source path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("source repository path is not a directory: %s", abs)
	}

	dotGit := filepath.Join(abs, ".git")
	if info, err := os.Lstat(dotGit); err == nil {
		if !info.IsDir() {
			return "", errors.New("linked worktrees and gitdir files are not supported for conversion")
		}
		return dotGit, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect source .git: %w", err)
	}
	if info, err := os.Stat(filepath.Join(abs, "config")); err != nil || info.IsDir() {
		if err == nil {
			err = errors.New("config is a directory")
		}
		return "", fmt.Errorf("source is not a Git repository: %w", err)
	}
	return abs, nil
}

func rejectUnsupportedSourceFiles(gitDir string) error {
	unsupported := []struct {
		path   string
		reason string
	}{
		{filepath.Join(gitDir, "shallow"), "shallow repositories"},
		{filepath.Join(gitDir, "info", "grafts"), "grafts"},
		{filepath.Join(gitDir, "objects", "info", "alternates"), "object alternates"},
		{filepath.Join(gitDir, "objects", "info", "http-alternates"), "HTTP object alternates"},
	}
	for _, item := range unsupported {
		if _, err := os.Lstat(item.path); err == nil {
			return fmt.Errorf("source uses unsupported %s", item.reason)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect source %s: %w", item.reason, err)
		}
	}
	packDir := filepath.Join(gitDir, "objects", "pack")
	entries, err := os.ReadDir(packDir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect source packs: %w", err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".promisor") {
			return errors.New("source uses unsupported promisor objects")
		}
	}
	return nil
}

func rejectPartialClone(repo *git.Repository) error {
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	if cfg.Raw == nil || !cfg.Raw.HasSection("extensions") {
		return nil
	}
	for _, option := range cfg.Raw.Section("extensions").Options {
		if strings.EqualFold(option.Key, "partialClone") {
			return errors.New("source uses unsupported partial-clone objects")
		}
	}
	return nil
}

func sourceReferences(repo *git.Repository) ([]sourceReference, error) {
	iter, err := repo.References()
	if err != nil {
		return nil, err
	}
	var refs []sourceReference
	byName := make(map[plumbing.ReferenceName]sourceReference)
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name()
		if err := name.Validate(); err != nil {
			return fmt.Errorf("invalid source reference %q: %w", name, err)
		}
		if name.IsNote() || strings.HasPrefix(name.String(), "refs/notes/") {
			return fmt.Errorf("source reference %s uses unsupported Git notes", name)
		}
		if strings.HasPrefix(name.String(), "refs/replace/") {
			return fmt.Errorf("source reference %s uses unsupported replace objects", name)
		}

		item := sourceReference{name: name, typ: ref.Type()}
		switch ref.Type() {
		case plumbing.HashReference:
			if ref.Hash().IsZero() || !isSHA1OID(ref.Hash().String()) {
				return fmt.Errorf("source reference %s does not contain a SHA-1 object ID", name)
			}
			item.hash = ref.Hash()
		case plumbing.SymbolicReference:
			if err := ref.Target().Validate(); err != nil {
				return fmt.Errorf("source reference %s has invalid symbolic target: %w", name, err)
			}
			if ref.Target().IsNote() || strings.HasPrefix(ref.Target().String(), "refs/replace/") {
				return fmt.Errorf("source reference %s targets an unsupported ref namespace", name)
			}
			item.target = ref.Target()
		default:
			return fmt.Errorf("source reference %s has unsupported type %s", name, ref.Type())
		}
		refs = append(refs, item)
		byName[name] = item
		return nil
	})
	if err != nil {
		return nil, err
	}

	for _, ref := range refs {
		if ref.typ != plumbing.SymbolicReference {
			continue
		}
		seen := map[plumbing.ReferenceName]bool{ref.name: true}
		current := ref
		for current.typ == plumbing.SymbolicReference {
			if seen[current.target] {
				return nil, fmt.Errorf("symbolic reference cycle starting at %s", ref.name)
			}
			seen[current.target] = true
			next, ok := byName[current.target]
			if !ok {
				if ref.name == plumbing.HEAD && current.target.IsBranch() {
					break // An unborn HEAD is valid.
				}
				return nil, fmt.Errorf("symbolic reference %s has missing target %s", ref.name, current.target)
			}
			current = next
		}
	}
	return refs, nil
}

func isSHA1OID(value string) bool {
	if len(value) != formatcfg.SHA1HexSize {
		return false
	}
	for i := range len(value) {
		c := value[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (c *objectConverter) convert(old plumbing.Hash, expected plumbing.ObjectType) (plumbing.Hash, error) {
	key := old.String()
	if !isSHA1OID(key) {
		return plumbing.ZeroHash, fmt.Errorf("invalid source object ID %q", key)
	}
	if converted, ok := c.converted[key]; ok {
		if expected != plumbing.AnyObject && converted.typ != expected {
			return plumbing.ZeroHash, fmt.Errorf(
				"object %s has type %s, expected %s", old, converted.typ, expected,
			)
		}
		return converted.hash, nil
	}
	if c.visiting[key] {
		return plumbing.ZeroHash, fmt.Errorf("object graph cycle at %s", old)
	}

	obj, err := c.source.Storer.EncodedObject(plumbing.AnyObject, old)
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("load source object %s: %w", old, err)
	}
	if expected != plumbing.AnyObject && obj.Type() != expected {
		return plumbing.ZeroHash, fmt.Errorf("object %s has type %s, expected %s", old, obj.Type(), expected)
	}
	if obj.Type() != plumbing.BlobObject && obj.Type() != plumbing.TreeObject &&
		obj.Type() != plumbing.CommitObject && obj.Type() != plumbing.TagObject {
		return plumbing.ZeroHash, fmt.Errorf("object %s has unsupported type %s", old, obj.Type())
	}

	c.visiting[key] = true
	defer delete(c.visiting, key)

	var converted plumbing.Hash
	if obj.Type() == plumbing.BlobObject {
		converted, err = c.convertBlob(old, obj)
	} else {
		var raw []byte
		raw, err = readAndValidateSourceObject(old, obj)
		if err == nil {
			switch obj.Type() {
			case plumbing.TreeObject:
				raw, err = c.rewriteTree(raw)
			case plumbing.CommitObject:
				raw, err = c.rewriteCommit(raw)
			case plumbing.TagObject:
				raw, err = c.rewriteTag(raw)
			default:
				err = fmt.Errorf("unsupported structured object type %s", obj.Type())
			}
		}
		if err == nil {
			converted, err = writeTargetObject(c.target, obj.Type(), raw)
		}
	}
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("convert %s object %s: %w", obj.Type(), old, err)
	}
	if !IsSHA256OID(converted.String()) {
		return plumbing.ZeroHash, fmt.Errorf("converted object %s did not produce a SHA-256 ID", old)
	}
	c.converted[key] = convertedObject{hash: converted, typ: obj.Type()}
	return converted, nil
}

func readAndValidateSourceObject(old plumbing.Hash, obj plumbing.EncodedObject) ([]byte, error) {
	if obj.Size() < 0 || obj.Size() > maxStructuredObjectSize {
		return nil, fmt.Errorf("structured object size %d exceeds limit %d", obj.Size(), maxStructuredObjectSize)
	}
	reader, err := obj.Reader()
	if err != nil {
		return nil, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(reader, maxStructuredObjectSize+1))
	closeErr := reader.Close()
	if readErr != nil {
		return nil, readErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if int64(len(raw)) != obj.Size() {
		return nil, fmt.Errorf("object declares size %d but contains %d bytes", obj.Size(), len(raw))
	}
	if err := validateSourceHash(old, obj.Type(), raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func validateSourceHash(old plumbing.Hash, typ plumbing.ObjectType, raw []byte) error {
	h := sha1.New() // #nosec G401 -- validation of legacy Git input only.
	if err := writeObjectHeader(h, typ, int64(len(raw))); err != nil {
		return fmt.Errorf("hash SHA-1 object header: %w", err)
	}
	if _, err := h.Write(raw); err != nil {
		return fmt.Errorf("hash SHA-1 object body: %w", err)
	}
	if !equalBytes(h.Sum(nil), old.Bytes()) {
		return fmt.Errorf("source object hash mismatch: content is not %s", old)
	}
	return nil
}

func (c *objectConverter) convertBlob(old plumbing.Hash, obj plumbing.EncodedObject) (plumbing.Hash, error) {
	if obj.Size() < 0 {
		return plumbing.ZeroHash, fmt.Errorf("negative blob size %d", obj.Size())
	}
	reader, err := obj.Reader()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	writer, err := c.target.Storer.RawObjectWriter(plumbing.BlobObject, obj.Size())
	if err != nil {
		_ = reader.Close()
		return plumbing.ZeroHash, err
	}

	sourceHash := sha1.New() // #nosec G401 -- validation of legacy Git input only.
	targetHash := sha256.New()
	if err := writeObjectHeader(sourceHash, plumbing.BlobObject, obj.Size()); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return plumbing.ZeroHash, err
	}
	if err := writeObjectHeader(targetHash, plumbing.BlobObject, obj.Size()); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return plumbing.ZeroHash, err
	}

	written, copyErr := io.Copy(io.MultiWriter(sourceHash, targetHash, writer), reader)
	readCloseErr := reader.Close()
	writeCloseErr := writer.Close()
	if copyErr != nil {
		return plumbing.ZeroHash, copyErr
	}
	if readCloseErr != nil {
		return plumbing.ZeroHash, readCloseErr
	}
	if writeCloseErr != nil {
		return plumbing.ZeroHash, writeCloseErr
	}
	if written != obj.Size() {
		return plumbing.ZeroHash, fmt.Errorf("blob declares size %d but contains %d bytes", obj.Size(), written)
	}
	if !equalBytes(sourceHash.Sum(nil), old.Bytes()) {
		return plumbing.ZeroHash, fmt.Errorf("source object hash mismatch: content is not %s", old)
	}
	converted, ok := plumbing.FromBytes(targetHash.Sum(nil))
	if !ok || !IsSHA256OID(converted.String()) {
		return plumbing.ZeroHash, errors.New("failed to construct converted blob object ID")
	}
	if err := c.target.Storer.HasEncodedObject(converted); err != nil {
		return plumbing.ZeroHash, fmt.Errorf("converted blob was not stored: %w", err)
	}
	return converted, nil
}

func writeTargetObject(repo *git.Repository, typ plumbing.ObjectType, raw []byte) (plumbing.Hash, error) {
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(typ)
	obj.SetSize(int64(len(raw)))
	writer, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := writer.Write(raw); err != nil {
		_ = writer.Close()
		return plumbing.ZeroHash, err
	}
	if err := writer.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	h, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return h, nil
}

func writeObjectHeader(h hash.Hash, typ plumbing.ObjectType, size int64) error {
	_, err := fmt.Fprintf(h, "%s %d\x00", typ, size)
	return err
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var different byte
	for i := range a {
		different |= a[i] ^ b[i]
	}
	return different == 0
}
