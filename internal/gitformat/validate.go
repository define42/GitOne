package gitformat

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
)

type reachableValidator struct {
	repo     *git.Repository
	done     map[string]plumbing.ObjectType
	visiting map[string]bool
}

type replacementReference struct {
	name        plumbing.ReferenceName
	original    plumbing.Hash
	replacement plumbing.Hash
}

// ValidateReachable performs a content-addressed fsck of every object
// reachable from a SHA-256 repository's hash refs. Unlike legacy conversion,
// validation permits SHA-256 gitlinks, notes, replace refs, signatures, and
// mergetags because no object bytes are rewritten.
func ValidateReachable(repo *git.Repository) error {
	_, err := validatedReachableObjects(repo)
	return err
}

// validatedReachableObjects validates repo and returns every object that must
// be retained to reproduce its refs in a self-contained repository. Gitlink
// targets are intentionally absent because they belong to another repository.
func validatedReachableObjects(
	repo *git.Repository,
) (map[string]plumbing.ObjectType, error) {
	if err := Validate(repo); err != nil {
		return nil, err
	}
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	var roots []plumbing.Hash
	var replacements []replacementReference
	byName := make(map[plumbing.ReferenceName]*plumbing.Reference)
	err = refs.ForEach(func(ref *plumbing.Reference) error {
		byName[ref.Name()] = ref
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		roots = append(roots, ref.Hash())
		if suffix, ok := strings.CutPrefix(ref.Name().String(), "refs/replace/"); ok {
			if !IsSHA256OID(suffix) {
				return fmt.Errorf("replace ref %s does not name a full SHA-256 object", ref.Name())
			}
			original, parsed := plumbing.FromHex(suffix)
			if !parsed {
				return fmt.Errorf("replace ref %s has invalid original object ID", ref.Name())
			}
			roots = append(roots, original)
			replacements = append(replacements, replacementReference{
				name: ref.Name(), original: original, replacement: ref.Hash(),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := validateSymbolicReferences(byName); err != nil {
		return nil, err
	}

	validator := &reachableValidator{
		repo:     repo,
		done:     make(map[string]plumbing.ObjectType),
		visiting: make(map[string]bool),
	}
	for _, root := range roots {
		if _, err := validator.walk(root, plumbing.AnyObject); err != nil {
			return nil, fmt.Errorf("validate reachable object %s: %w", root, err)
		}
	}
	for _, replacement := range replacements {
		originalType := validator.done[replacement.original.String()]
		replacementType := validator.done[replacement.replacement.String()]
		if originalType != replacementType {
			return nil, fmt.Errorf(
				"replace ref %s changes object type from %s to %s",
				replacement.name, originalType, replacementType,
			)
		}
	}
	return validator.done, nil
}

func validateSymbolicReferences(refs map[plumbing.ReferenceName]*plumbing.Reference) error {
	for _, ref := range refs {
		if ref.Type() != plumbing.SymbolicReference {
			continue
		}
		seen := map[plumbing.ReferenceName]bool{ref.Name(): true}
		current := ref
		for current.Type() == plumbing.SymbolicReference {
			target := current.Target()
			if seen[target] {
				return fmt.Errorf("symbolic reference cycle starting at %s", ref.Name())
			}
			seen[target] = true
			next, ok := refs[target]
			if !ok {
				if ref.Name() == plumbing.HEAD && target.IsBranch() {
					break
				}
				return fmt.Errorf("symbolic reference %s has missing target %s", ref.Name(), target)
			}
			current = next
		}
	}
	return nil
}

func (v *reachableValidator) walk(id plumbing.Hash, expected plumbing.ObjectType) (plumbing.ObjectType, error) {
	if !IsSHA256OID(id.String()) {
		return plumbing.InvalidObject, fmt.Errorf("invalid SHA-256 object ID %q", id)
	}
	key := id.String()
	if typ, ok := v.done[key]; ok {
		if expected != plumbing.AnyObject && typ != expected {
			return plumbing.InvalidObject, fmt.Errorf("object has type %s, expected %s", typ, expected)
		}
		return typ, nil
	}
	if v.visiting[key] {
		return plumbing.InvalidObject, errors.New("object graph contains a cycle")
	}
	obj, err := v.repo.Storer.EncodedObject(plumbing.AnyObject, id)
	if err != nil {
		return plumbing.InvalidObject, fmt.Errorf("load object: %w", err)
	}
	if expected != plumbing.AnyObject && obj.Type() != expected {
		return plumbing.InvalidObject, fmt.Errorf("object has type %s, expected %s", obj.Type(), expected)
	}
	if obj.Type() != plumbing.BlobObject && obj.Type() != plumbing.TreeObject &&
		obj.Type() != plumbing.CommitObject && obj.Type() != plumbing.TagObject {
		return plumbing.InvalidObject, fmt.Errorf("unsupported object type %s", obj.Type())
	}

	v.visiting[key] = true
	defer delete(v.visiting, key)
	if obj.Type() == plumbing.BlobObject {
		err = validateSHA256Blob(id, obj)
	} else {
		var raw []byte
		raw, err = readAndValidateSHA256Object(id, obj)
		if err == nil {
			switch obj.Type() {
			case plumbing.TreeObject:
				err = v.validateTree(raw)
			case plumbing.CommitObject:
				err = v.validateCommit(raw)
			case plumbing.TagObject:
				err = v.validateTag(raw)
			default:
				err = fmt.Errorf("unsupported structured object type %s", obj.Type())
			}
		}
	}
	if err != nil {
		return plumbing.InvalidObject, fmt.Errorf("invalid %s object %s: %w", obj.Type(), id, err)
	}
	v.done[key] = obj.Type()
	return obj.Type(), nil
}

func readAndValidateSHA256Object(id plumbing.Hash, obj plumbing.EncodedObject) ([]byte, error) {
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
	h := sha256.New()
	if err := writeObjectHeader(h, obj.Type(), int64(len(raw))); err != nil {
		return nil, err
	}
	if _, err := h.Write(raw); err != nil {
		return nil, err
	}
	if !equalBytes(h.Sum(nil), id.Bytes()) {
		return nil, errors.New("SHA-256 object ID does not match content")
	}
	return raw, nil
}

func validateSHA256Blob(id plumbing.Hash, obj plumbing.EncodedObject) error {
	if obj.Size() < 0 {
		return fmt.Errorf("negative blob size %d", obj.Size())
	}
	reader, err := obj.Reader()
	if err != nil {
		return err
	}
	h := sha256.New()
	if err := writeObjectHeader(h, plumbing.BlobObject, obj.Size()); err != nil {
		_ = reader.Close()
		return err
	}
	written, readErr := io.Copy(h, reader)
	closeErr := reader.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != obj.Size() {
		return fmt.Errorf("blob declares size %d but contains %d bytes", obj.Size(), written)
	}
	if !equalBytes(h.Sum(nil), id.Bytes()) {
		return errors.New("SHA-256 object ID does not match content")
	}
	return nil
}

func (v *reachableValidator) validateTree(raw []byte) error {
	var previous *treeEntryOrder
	for offset := 0; offset < len(raw); {
		space := bytes.IndexByte(raw[offset:], ' ')
		if space <= 0 {
			return fmt.Errorf("tree entry at byte %d is missing a mode", offset)
		}
		space += offset
		mode := raw[offset:space]
		nameStart := space + 1
		nul := bytes.IndexByte(raw[nameStart:], 0)
		if nul < 0 {
			return fmt.Errorf("tree entry at byte %d is missing a name terminator", offset)
		}
		nul += nameStart
		name := raw[nameStart:nul]
		if err := validateTreeName(name); err != nil {
			return err
		}
		hashStart := nul + 1
		hashEnd := hashStart + formatcfg.SHA256Size
		if hashEnd > len(raw) {
			return fmt.Errorf("tree entry %q has a truncated object ID", name)
		}
		child, ok := plumbing.FromBytes(raw[hashStart:hashEnd])
		if !ok || !IsSHA256OID(child.String()) || child.IsZero() {
			return fmt.Errorf("tree entry %q has an invalid SHA-256 object ID", name)
		}
		var expected plumbing.ObjectType
		isDir := false
		skipTarget := false
		switch string(mode) {
		case "40000":
			expected = plumbing.TreeObject
			isDir = true
		case "100644", "100664", "100755", "120000":
			expected = plumbing.BlobObject
		case "160000":
			expected = plumbing.CommitObject
			skipTarget = true // A gitlink target lives in another repository.
		default:
			return fmt.Errorf("tree entry %q has non-canonical or unsupported mode %q", name, mode)
		}
		if previous != nil {
			if bytes.Equal(previous.name, name) {
				return fmt.Errorf("tree has duplicate entry %q", name)
			}
			if compareTreeNames(previous.name, previous.dir, name, isDir) >= 0 {
				return fmt.Errorf("tree entries are not in canonical order at %q", name)
			}
		}
		previous = &treeEntryOrder{name: bytes.Clone(name), dir: isDir}
		if !skipTarget {
			if _, err := v.walk(child, expected); err != nil {
				return fmt.Errorf("tree entry %q: %w", name, err)
			}
		}
		offset = hashEnd
	}
	return nil
}

func (v *reachableValidator) validateCommit(raw []byte) error {
	headers, _, err := splitNativeObjectHeaders(raw, "commit")
	if err != nil {
		return err
	}
	if len(headers) < 3 {
		return errors.New("missing required commit headers")
	}
	treeValue, err := requireHeader(headers[0], "tree", "commit")
	if err != nil {
		return err
	}
	tree, err := parseSHA256TextOID(treeValue, "commit tree")
	if err != nil {
		return err
	}
	if _, err := v.walk(tree, plumbing.TreeObject); err != nil {
		return fmt.Errorf("commit tree: %w", err)
	}

	index := 1
	for index < len(headers) && bytes.HasPrefix(headers[index], []byte("parent ")) {
		parentValue, err := requireHeader(headers[index], "parent", "commit")
		if err != nil {
			return err
		}
		parent, err := parseSHA256TextOID(parentValue, "commit parent")
		if err != nil {
			return err
		}
		if _, err := v.walk(parent, plumbing.CommitObject); err != nil {
			return fmt.Errorf("commit parent: %w", err)
		}
		index++
	}
	if index >= len(headers) {
		return errors.New("missing author header")
	}
	if value, err := requireHeader(headers[index], "author", "commit"); err != nil || len(value) == 0 {
		return errors.New("missing or malformed author header")
	}
	index++
	if index >= len(headers) {
		return errors.New("missing committer header")
	}
	if value, err := requireHeader(headers[index], "committer", "commit"); err != nil || len(value) == 0 {
		return errors.New("missing or malformed committer header")
	}
	index++

	sawEncoding := false
	for index < len(headers) {
		line := headers[index]
		if len(line) == 0 || line[0] == ' ' || line[0] == '\t' {
			return errors.New("orphan commit header continuation")
		}
		key, value, err := parseHeader(line)
		if err != nil {
			return err
		}
		if key == "tree" || key == "parent" || key == "author" || key == "committer" {
			return fmt.Errorf("duplicate or out-of-order commit header %q", key)
		}
		if key == "encoding" {
			if sawEncoding || len(value) == 0 {
				return errors.New("duplicate or empty commit encoding")
			}
			sawEncoding = true
		}
		index++
		var continuation [][]byte
		for index < len(headers) && len(headers[index]) > 0 && headers[index][0] == ' ' {
			continuation = append(continuation, headers[index][1:])
			index++
		}
		if key == "encoding" && len(continuation) > 0 {
			return errors.New("encoding header has a continuation")
		}
		if key == "mergetag" {
			var embedded bytes.Buffer
			embedded.Write(value)
			embedded.WriteByte('\n')
			for _, part := range continuation {
				embedded.Write(part)
				embedded.WriteByte('\n')
			}
			if err := v.validateTag(embedded.Bytes()); err != nil {
				return fmt.Errorf("invalid mergetag: %w", err)
			}
		}
	}
	return nil
}

func (v *reachableValidator) validateTag(raw []byte) error {
	headers, _, err := splitNativeObjectHeaders(raw, "tag")
	if err != nil {
		return err
	}
	if len(headers) < 3 {
		return errors.New("missing required tag headers")
	}
	objectValue, err := requireHeader(headers[0], "object", "tag")
	if err != nil {
		return err
	}
	target, err := parseSHA256TextOID(objectValue, "tag target")
	if err != nil {
		return err
	}
	typeValue, err := requireHeader(headers[1], "type", "tag")
	if err != nil {
		return err
	}
	targetType, err := plumbing.ParseObjectType(string(typeValue))
	if err != nil || targetType.IsDelta() || targetType == plumbing.AnyObject {
		return fmt.Errorf("invalid tag target type %q", typeValue)
	}
	if value, err := requireHeader(headers[2], "tag", "tag"); err != nil || len(value) == 0 {
		return errors.New("missing or malformed tag name")
	}
	index := 3
	if index < len(headers) && bytes.HasPrefix(headers[index], []byte("tagger ")) {
		if value, err := requireHeader(headers[index], "tagger", "tag"); err != nil || len(value) == 0 {
			return errors.New("malformed tagger header")
		}
		index++
	}
	for index < len(headers) {
		if len(headers[index]) == 0 || headers[index][0] == ' ' || headers[index][0] == '\t' {
			return errors.New("orphan tag header continuation")
		}
		key, _, err := parseHeader(headers[index])
		if err != nil {
			return err
		}
		if key == "object" || key == "type" || key == "tag" || key == "tagger" {
			return fmt.Errorf("duplicate or out-of-order tag header %q", key)
		}
		index++
		for index < len(headers) && len(headers[index]) > 0 && headers[index][0] == ' ' {
			index++
		}
	}
	if _, err := v.walk(target, targetType); err != nil {
		return fmt.Errorf("tag target: %w", err)
	}
	return nil
}

func splitNativeObjectHeaders(raw []byte, kind string) (headers [][]byte, message []byte, err error) {
	separator := bytes.Index(raw, []byte("\n\n"))
	if separator < 0 {
		return nil, nil, fmt.Errorf("malformed %s: missing header terminator", kind)
	}
	if separator == 0 {
		return nil, nil, fmt.Errorf("malformed %s: empty header block", kind)
	}
	headers = bytes.Split(raw[:separator], []byte{'\n'})
	for _, header := range headers {
		if len(header) == 0 || header[0] == '\t' ||
			bytes.IndexByte(header, 0) >= 0 || bytes.IndexByte(header, '\r') >= 0 {
			return nil, nil, fmt.Errorf("malformed %s header", kind)
		}
	}
	return headers, raw[separator+2:], nil
}

func parseSHA256TextOID(value []byte, context string) (plumbing.Hash, error) {
	if !IsSHA256OID(string(value)) {
		return plumbing.ZeroHash, fmt.Errorf("%s has invalid SHA-256 object ID %q", context, value)
	}
	id, ok := plumbing.FromHex(string(value))
	if !ok {
		return plumbing.ZeroHash, fmt.Errorf("%s has invalid SHA-256 object ID %q", context, value)
	}
	return id, nil
}
