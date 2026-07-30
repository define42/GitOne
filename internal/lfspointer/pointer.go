package lfspointer

import (
	"bytes"
	"encoding/hex"
	"strconv"
	"strings"
)

const (
	pointerVersion = "https://git-lfs.github.com/spec/v1"
	MaxPointerSize = 1024
)

type Pointer struct {
	OID  string
	Size int64
}

func Parse(content []byte) (Pointer, bool) {
	if len(content) == 0 || len(content) > MaxPointerSize || bytes.IndexByte(content, 0) >= 0 {
		return Pointer{}, false
	}

	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) < 3 || lines[0] != "version "+pointerVersion {
		return Pointer{}, false
	}

	var pointer Pointer
	var hasOID, hasSize bool
	for _, line := range lines[1:] {
		key, value, found := strings.Cut(line, " ")
		if !found || value == "" {
			return Pointer{}, false
		}
		switch key {
		case "oid":
			if hasOID || !strings.HasPrefix(value, "sha256:") {
				return Pointer{}, false
			}
			pointer.OID = strings.TrimPrefix(value, "sha256:")
			if !ValidOID(pointer.OID) {
				return Pointer{}, false
			}
			hasOID = true
		case "size":
			if hasSize || !decimalDigits(value) {
				return Pointer{}, false
			}
			size, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return Pointer{}, false
			}
			pointer.Size = size
			hasSize = true
		default:
			if !strings.HasPrefix(key, "ext-") {
				return Pointer{}, false
			}
		}
	}
	return pointer, hasOID && hasSize
}

func ValidOID(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func decimalDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}
