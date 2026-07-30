package repopath

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func FuzzParseGitRequestPathRoundTrip(f *testing.F) {
	for _, path := range []string{
		"/engineering/api.git",
		"/engineering/backend/api.git/git-upload-pack",
		"/engineering/api.git/info/lfs/objects/0123456789abcdef",
		"/team//repository.git",
		"/team/../repository.git",
		"not-a-repository",
	} {
		f.Add(path)
	}
	f.Fuzz(func(t *testing.T, input string) {
		repository, suffix, err := ParseGitRequestPath(input)
		if err != nil {
			return
		}
		if repository.Group() == "" ||
			!valid(repository.Name) ||
			len(repository.Groups) == 0 {
			t.Fatalf("accepted invalid repository %#v from %q", repository, input)
		}
		for _, group := range repository.Groups {
			if !valid(group) {
				t.Fatalf("accepted invalid group %q from %q", group, input)
			}
		}
		roundTrip, roundTripSuffix, err := ParseGitRequestPath(
			"/" + repository.Full() + ".git" + suffix,
		)
		if err != nil {
			t.Fatalf("accepted path did not round-trip: %q: %v", input, err)
		}
		if roundTrip.Full() != repository.Full() || roundTripSuffix != suffix {
			t.Fatalf(
				"path round-trip = %q %q, want %q %q",
				roundTrip.Full(),
				roundTripSuffix,
				repository.Full(),
				suffix,
			)
		}
	})
}

func FuzzParseGroupRoundTrip(f *testing.F) {
	for _, group := range []string{
		"engineering",
		"/engineering/backend/",
		"engineering//backend",
		"..",
		"team-\x00",
	} {
		f.Add(group)
	}
	f.Fuzz(func(t *testing.T, input string) {
		parts, err := ParseGroup(input)
		if err != nil {
			return
		}
		canonical := strings.Join(parts, "/")
		roundTrip, err := ParseGroup(canonical)
		if err != nil {
			t.Fatalf("accepted group did not round-trip: %q: %v", input, err)
		}
		if !slices.Equal(roundTrip, parts) {
			t.Fatalf("group round-trip = %q, want %q", roundTrip, parts)
		}
	})
}

func FuzzSafeJoinRemainsWithinRoot(f *testing.F) {
	for _, part := range []string{
		"repository.git",
		"engineering/api.git",
		"../outside",
		"/absolute",
		"..\\outside",
		"",
	} {
		f.Add(part)
	}
	f.Fuzz(func(t *testing.T, part string) {
		root := filepath.Join(os.TempDir(), "gitone-safejoin-fuzz-root")
		target, err := SafeJoin(root, part)
		if err != nil {
			return
		}
		absoluteRoot, err := filepath.Abs(root)
		if err != nil {
			t.Fatal(err)
		}
		relative, err := filepath.Rel(absoluteRoot, target)
		if err != nil {
			t.Fatal(err)
		}
		if relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("SafeJoin(%q) escaped root to %q", part, target)
		}
	})
}
