package review

import (
	"strings"
	"testing"
)

func TestPersistedReviewCommitIDsRequireCanonicalSHA256(t *testing.T) {
	repository := testRepository()
	valid := validPersistedRequest(repository)
	if err := validate(repository, valid.ID, valid); err != nil {
		t.Fatalf("canonical SHA-256 review was rejected: %v", err)
	}

	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "legacy SHA-1", value: strings.Repeat("a", 40)},
		{name: "uppercase SHA-256", value: strings.Repeat("A", 64)},
		{name: "non-hex SHA-256", value: strings.Repeat("g", 64)},
		{name: "abbreviated SHA-256", value: strings.Repeat("a", 63)},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := valid
			request.HeadCommit = test.value
			if err := validate(repository, request.ID, request); err == nil ||
				!strings.Contains(err.Error(), "lowercase SHA-256") {
				t.Fatalf("validate commit %q error = %v", test.value, err)
			}
		})
	}
}

func TestValidCommitHashRejectsNonCanonicalEncodings(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  bool
	}{
		{name: "lowercase SHA-256", value: strings.Repeat("a", 64), want: true},
		{name: "legacy SHA-1", value: strings.Repeat("a", 40)},
		{name: "uppercase SHA-256", value: strings.Repeat("A", 64)},
		{name: "non-hex SHA-256", value: strings.Repeat("g", 64)},
		{name: "empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validCommitHash(test.value); got != test.want {
				t.Fatalf("validCommitHash(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}
