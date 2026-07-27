package githttp

import (
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"testing"
)

func TestControlRefValidation(t *testing.T) {
	req := packp.NewReferenceUpdateRequest()
	req.Commands = []*packp.Command{{Name: plumbing.NewBranchReferenceName("main"), Old: plumbing.NewHash("1111111111111111111111111111111111111111"), New: plumbing.NewHash("2222222222222222222222222222222222222222")}}
	if e := validateControlRefs(req); e != nil {
		t.Fatal(e)
	}
}
func TestControlRejectsTags(t *testing.T) {
	req := packp.NewReferenceUpdateRequest()
	req.Commands = []*packp.Command{{Name: plumbing.NewTagReferenceName("v1"), Old: plumbing.NewHash("1111111111111111111111111111111111111111"), New: plumbing.NewHash("2222222222222222222222222222222222222222")}}
	if e := validateControlRefs(req); e == nil {
		t.Fatal("expected rejection")
	}
}
