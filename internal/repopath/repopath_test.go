package repopath

import "testing"

func TestParseNestedRepository(t *testing.T) {
	r, s, e := ParseGitRequestPath("/engineering/backend/api.git/git-upload-pack")
	if e != nil {
		t.Fatal(e)
	}
	if r.Group() != "engineering/backend" || r.Name != "api" || s != "/git-upload-pack" {
		t.Fatalf("unexpected: %#v %q", r, s)
	}
}
func TestRejectRootRepository(t *testing.T) {
	if _, _, e := ParseGitRequestPath("/api.git/info/refs"); e == nil {
		t.Fatal("expected error")
	}
}
func TestSafeJoinEscape(t *testing.T) {
	if _, e := SafeJoin("/tmp/root", "..", "outside"); e == nil {
		t.Fatal("expected escape rejection")
	}
}
