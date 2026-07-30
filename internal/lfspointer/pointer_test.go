package lfspointer

import (
	"fmt"
	"testing"
)

func FuzzParseRoundTrip(f *testing.F) {
	const validOID = "f7895326610712feb431767ef21f7e7eaec2bee6d99db789a212ed3a872b8f2a"
	for _, content := range [][]byte{
		[]byte(
			"version https://git-lfs.github.com/spec/v1\n" +
				"oid sha256:" + validOID + "\n" +
				"size 22\n",
		),
		[]byte(
			"version https://git-lfs.github.com/spec/v1\r\n" +
				"ext-test value\r\n" +
				"oid sha256:" + validOID + "\r\n" +
				"size 0\r\n",
		),
		[]byte("ordinary file contents"),
		[]byte("version https://git-lfs.github.com/spec/v1\x00"),
		nil,
	} {
		f.Add(content)
	}
	f.Fuzz(func(t *testing.T, content []byte) {
		pointer, ok := Parse(content)
		if !ok {
			return
		}
		if !ValidOID(pointer.OID) || pointer.Size < 0 {
			t.Fatalf("Parse(%q) returned invalid pointer %#v", content, pointer)
		}
		canonical := []byte(fmt.Sprintf(
			"version https://git-lfs.github.com/spec/v1\n"+
				"oid sha256:%s\n"+
				"size %d\n",
			pointer.OID,
			pointer.Size,
		))
		roundTrip, ok := Parse(canonical)
		if !ok || roundTrip != pointer {
			t.Fatalf("pointer round-trip = %#v, %t, want %#v", roundTrip, ok, pointer)
		}
	})
}
