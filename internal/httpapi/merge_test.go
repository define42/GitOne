package httpapi

import "testing"

func TestMergeTextLines(t *testing.T) {
	base := "one\ntwo\nthree\nfour\n"
	target := "one\ntarget\ntwo\nthree\nfour\n"
	source := "one\ntwo\nthree\nsource\n"

	merged, clean := mergeTextLines(base, target, source)
	if !clean {
		t.Fatal("independent line changes were reported as conflicting")
	}
	expected := "one\ntarget\ntwo\nthree\nsource\n"
	if merged != expected {
		t.Fatalf("unexpected merged text:\n%s", merged)
	}
}

func TestMergeTextLinesRejectsConflict(t *testing.T) {
	base := "one\ntwo\nthree\n"
	target := "one\ntarget\nthree\n"
	source := "one\nsource\nthree\n"

	if merged, clean := mergeTextLines(base, target, source); clean {
		t.Fatalf("overlapping line changes unexpectedly merged as %q", merged)
	}
}

func TestMergeTextLinesAcceptsIdenticalChange(t *testing.T) {
	base := "one\ntwo\n"
	version := "one\nchanged\n"

	merged, clean := mergeTextLines(base, version, version)
	if !clean || merged != version {
		t.Fatalf("identical changes did not merge cleanly: clean=%v merged=%q", clean, merged)
	}
}
