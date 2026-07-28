package lfs

import "testing"

func TestParsePointer(t *testing.T) {
	const oid = "f7895326610712feb431767ef21f7e7eaec2bee6d99db789a212ed3a872b8f2a"
	pointer, ok := ParsePointer([]byte(
		"version https://git-lfs.github.com/spec/v1\n" +
			"oid sha256:" + oid + "\n" +
			"size 22\n",
	))
	if !ok {
		t.Fatal("expected a valid Git LFS pointer")
	}
	if pointer.OID != oid || pointer.Size != 22 {
		t.Fatalf("unexpected pointer: %#v", pointer)
	}
}

func TestParsePointerRejectsOrdinaryAndMalformedContent(t *testing.T) {
	testCases := [][]byte{
		[]byte("ordinary text\n"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:bad\nsize 22\n"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:f7895326610712feb431767ef21f7e7eaec2bee6d99db789a212ed3a872b8f2a\nsize -1\n"),
	}
	for _, content := range testCases {
		if pointer, ok := ParsePointer(content); ok {
			t.Fatalf("unexpected pointer %#v for %q", pointer, content)
		}
	}
}
