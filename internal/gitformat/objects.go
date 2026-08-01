package gitformat

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v6/plumbing"
	formatcfg "github.com/go-git/go-git/v6/plumbing/format/config"
)

type treeEntryOrder struct {
	name []byte
	dir  bool
}

func (c *objectConverter) rewriteTree(raw []byte) ([]byte, error) {
	var out bytes.Buffer
	out.Grow(len(raw) + len(raw)/2)
	var previous *treeEntryOrder

	for offset := 0; offset < len(raw); {
		space := bytes.IndexByte(raw[offset:], ' ')
		if space <= 0 {
			return nil, fmt.Errorf("malformed tree entry at byte %d: missing mode", offset)
		}
		space += offset
		mode := raw[offset:space]
		nameStart := space + 1
		nul := bytes.IndexByte(raw[nameStart:], 0)
		if nul < 0 {
			return nil, fmt.Errorf("malformed tree entry at byte %d: missing name terminator", offset)
		}
		nul += nameStart
		name := raw[nameStart:nul]
		if err := validateTreeName(name); err != nil {
			return nil, fmt.Errorf("malformed tree entry at byte %d: %w", offset, err)
		}
		hashStart := nul + 1
		hashEnd := hashStart + formatcfg.SHA1Size
		if hashEnd > len(raw) {
			return nil, fmt.Errorf("malformed tree entry %q: truncated object ID", name)
		}
		old, ok := plumbing.FromBytes(raw[hashStart:hashEnd])
		if !ok || !isSHA1OID(old.String()) {
			return nil, fmt.Errorf("malformed tree entry %q: invalid SHA-1 object ID", name)
		}

		var expected plumbing.ObjectType
		isDir := false
		switch string(mode) {
		case "40000":
			expected = plumbing.TreeObject
			isDir = true
		case "100644", "100664", "100755", "120000":
			expected = plumbing.BlobObject
		case "160000":
			return nil, fmt.Errorf("tree entry %q is an unsupported submodule gitlink", name)
		default:
			return nil, fmt.Errorf("tree entry %q has non-canonical or unsupported mode %q", name, mode)
		}
		if previous != nil {
			if bytes.Equal(previous.name, name) {
				return nil, fmt.Errorf("tree has duplicate entry %q", name)
			}
			if compareTreeNames(previous.name, previous.dir, name, isDir) >= 0 {
				return nil, fmt.Errorf("tree entries are not in canonical order at %q", name)
			}
		}
		previous = &treeEntryOrder{name: bytes.Clone(name), dir: isDir}

		converted, err := c.convert(old, expected)
		if err != nil {
			return nil, fmt.Errorf("tree entry %q: %w", name, err)
		}
		out.Write(mode)
		out.WriteByte(' ')
		out.Write(name)
		out.WriteByte(0)
		out.Write(converted.Bytes())
		offset = hashEnd
	}
	return out.Bytes(), nil
}

func validateTreeName(name []byte) error {
	if len(name) == 0 {
		return errors.New("empty name")
	}
	if bytes.IndexByte(name, '/') >= 0 {
		return errors.New("name contains slash")
	}
	if bytes.Equal(name, []byte(".")) || bytes.Equal(name, []byte("..")) {
		return fmt.Errorf("forbidden name %q", name)
	}
	if strings.EqualFold(string(name), ".git") {
		return errors.New("forbidden .git name")
	}
	return nil
}

// compareTreeNames implements Git's base_name_compare ordering. A directory
// sorts as though its name had a trailing slash.
func compareTreeNames(a []byte, aDir bool, b []byte, bDir bool) int {
	common := min(len(a), len(b))
	if cmp := bytes.Compare(a[:common], b[:common]); cmp != 0 {
		return cmp
	}
	var aNext, bNext byte
	if len(a) > common {
		aNext = a[common]
	} else if aDir {
		aNext = '/'
	}
	if len(b) > common {
		bNext = b[common]
	} else if bDir {
		bNext = '/'
	}
	return int(aNext) - int(bNext)
}

func (c *objectConverter) rewriteCommit(raw []byte) ([]byte, error) {
	headers, message, err := splitObjectHeaders(raw, "commit")
	if err != nil {
		return nil, err
	}
	if len(headers) < 3 {
		return nil, errors.New("malformed commit: missing required headers")
	}

	treeValue, err := requireHeader(headers[0], "tree", "commit")
	if err != nil {
		return nil, err
	}
	treeID, err := parseSHA1TextOID(treeValue, "commit tree")
	if err != nil {
		return nil, err
	}
	convertedTree, err := c.convert(treeID, plumbing.TreeObject)
	if err != nil {
		return nil, fmt.Errorf("commit tree: %w", err)
	}

	var out bytes.Buffer
	out.Grow(len(raw) + 24*(1+len(headers)))
	fmt.Fprintf(&out, "tree %s\n", convertedTree)

	index := 1
	for index < len(headers) {
		key, value, err := parseHeader(headers[index])
		if err != nil {
			return nil, fmt.Errorf("malformed commit header: %w", err)
		}
		if key != "parent" {
			break
		}
		parentID, err := parseSHA1TextOID(value, "commit parent")
		if err != nil {
			return nil, err
		}
		convertedParent, err := c.convert(parentID, plumbing.CommitObject)
		if err != nil {
			return nil, fmt.Errorf("commit parent: %w", err)
		}
		fmt.Fprintf(&out, "parent %s\n", convertedParent)
		index++
	}

	if index >= len(headers) {
		return nil, errors.New("malformed commit: missing author header")
	}
	author, err := requireHeader(headers[index], "author", "commit")
	if err != nil || len(author) == 0 {
		if err == nil {
			err = errors.New("empty author")
		}
		return nil, fmt.Errorf("malformed commit: %w", err)
	}
	out.Write(headers[index])
	out.WriteByte('\n')
	index++

	if index >= len(headers) {
		return nil, errors.New("malformed commit: missing committer header")
	}
	committer, err := requireHeader(headers[index], "committer", "commit")
	if err != nil || len(committer) == 0 {
		if err == nil {
			err = errors.New("empty committer")
		}
		return nil, fmt.Errorf("malformed commit: %w", err)
	}
	out.Write(headers[index])
	out.WriteByte('\n')
	index++

	sawEncoding := false
	for ; index < len(headers); index++ {
		key, value, err := parseHeader(headers[index])
		if err != nil {
			return nil, fmt.Errorf("malformed commit header: %w", err)
		}
		switch {
		case key == "encoding" && !sawEncoding && len(value) > 0:
			sawEncoding = true
			out.Write(headers[index])
			out.WriteByte('\n')
		case key == "gpgsig" || strings.HasPrefix(key, "gpgsig-"):
			return nil, errors.New("signed commits are not supported during SHA-1 conversion")
		case key == "mergetag":
			return nil, errors.New("commit mergetag headers are not supported during SHA-1 conversion")
		default:
			return nil, fmt.Errorf("unsupported commit header %q", key)
		}
	}
	out.WriteByte('\n')
	out.Write(message)
	return out.Bytes(), nil
}

func (c *objectConverter) rewriteTag(raw []byte) ([]byte, error) {
	headers, message, err := splitObjectHeaders(raw, "tag")
	if err != nil {
		return nil, err
	}
	if len(headers) < 3 {
		return nil, errors.New("malformed tag: missing required headers")
	}
	objectValue, err := requireHeader(headers[0], "object", "tag")
	if err != nil {
		return nil, err
	}
	targetID, err := parseSHA1TextOID(objectValue, "tag target")
	if err != nil {
		return nil, err
	}
	typeValue, err := requireHeader(headers[1], "type", "tag")
	if err != nil {
		return nil, err
	}
	targetType, err := plumbing.ParseObjectType(string(typeValue))
	if err != nil || targetType.IsDelta() || targetType == plumbing.AnyObject {
		return nil, fmt.Errorf("malformed tag target type %q", typeValue)
	}
	tagName, err := requireHeader(headers[2], "tag", "tag")
	if err != nil || len(tagName) == 0 {
		if err == nil {
			err = errors.New("empty tag name")
		}
		return nil, fmt.Errorf("malformed tag: %w", err)
	}
	for i := 3; i < len(headers); i++ {
		key, value, err := parseHeader(headers[i])
		if err != nil {
			return nil, fmt.Errorf("malformed tag header: %w", err)
		}
		switch {
		case key == "tagger" && i == 3 && len(value) > 0:
			// Preserved below.
		case key == "gpgsig" || strings.HasPrefix(key, "gpgsig-"):
			return nil, errors.New("signed tags are not supported during SHA-1 conversion")
		default:
			return nil, fmt.Errorf("unsupported tag header %q", key)
		}
	}
	if containsSignature(message) {
		return nil, errors.New("signed tags are not supported during SHA-1 conversion")
	}

	convertedTarget, err := c.convert(targetID, targetType)
	if err != nil {
		return nil, fmt.Errorf("tag target: %w", err)
	}
	var out bytes.Buffer
	out.Grow(len(raw) + 24)
	fmt.Fprintf(&out, "object %s\n", convertedTarget)
	for _, header := range headers[1:] {
		out.Write(header)
		out.WriteByte('\n')
	}
	out.WriteByte('\n')
	out.Write(message)
	return out.Bytes(), nil
}

func splitObjectHeaders(raw []byte, kind string) (headers [][]byte, message []byte, err error) {
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

func parseHeader(line []byte) (key string, value []byte, err error) {
	space := bytes.IndexByte(line, ' ')
	if space <= 0 || space == len(line)-1 {
		return "", nil, fmt.Errorf("invalid header line %q", line)
	}
	keyBytes := line[:space]
	for _, b := range keyBytes {
		if (b < 'a' || b > 'z') && (b < '0' || b > '9') && b != '-' {
			return "", nil, fmt.Errorf("invalid header name %q", keyBytes)
		}
	}
	return string(keyBytes), line[space+1:], nil
}

func requireHeader(line []byte, expected, kind string) ([]byte, error) {
	key, value, err := parseHeader(line)
	if err != nil {
		return nil, fmt.Errorf("malformed %s: %w", kind, err)
	}
	if key != expected {
		return nil, fmt.Errorf("malformed %s: %s header must be in canonical position", kind, expected)
	}
	return value, nil
}

func parseSHA1TextOID(value []byte, context string) (plumbing.Hash, error) {
	if !isSHA1OID(string(value)) {
		return plumbing.ZeroHash, fmt.Errorf("%s has invalid SHA-1 object ID %q", context, value)
	}
	id, ok := plumbing.FromHex(string(value))
	if !ok {
		return plumbing.ZeroHash, fmt.Errorf("%s has invalid SHA-1 object ID %q", context, value)
	}
	return id, nil
}

func containsSignature(message []byte) bool {
	for offset := 0; offset <= len(message); {
		line := message[offset:]
		for _, prefix := range [...]string{
			"-----BEGIN PGP SIGNATURE-----",
			"-----BEGIN PGP MESSAGE-----",
			"-----BEGIN SIGNED MESSAGE-----",
			"-----BEGIN SSH SIGNATURE-----",
		} {
			if bytes.HasPrefix(line, []byte(prefix)) {
				return true
			}
		}
		next := bytes.IndexByte(line, '\n')
		if next < 0 {
			break
		}
		offset += next + 1
	}
	return false
}
