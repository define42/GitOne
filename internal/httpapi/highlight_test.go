package httpapi

import (
	"strings"
	"testing"
)

func TestHighlightRepositoryBlob(t *testing.T) {
	source := "const value = \"<script>alert('nope')</script>\";\n"
	highlighted, language := highlightRepositoryBlob("server.js", source)

	if language != "JavaScript" {
		t.Fatalf("expected JavaScript lexer, got %q", language)
	}
	if highlighted == "" || !strings.Contains(highlighted, "<span") {
		t.Fatalf("expected highlighted HTML, got %q", highlighted)
	}
	if strings.Contains(highlighted, "<script>") ||
		!strings.Contains(highlighted, "&lt;script&gt;") {
		t.Fatalf("highlighted output did not safely escape source: %q", highlighted)
	}
}

func TestHighlightRepositoryBlobFallsBackToPlainSource(t *testing.T) {
	if highlighted, language := highlightRepositoryBlob("notes.unknown", "plain text\n"); highlighted != "" || language != "" {
		t.Fatalf("unknown file was unexpectedly highlighted as %q: %q", language, highlighted)
	}
	large := strings.Repeat("a", maxHighlightedBlobSize+1)
	if highlighted, language := highlightRepositoryBlob("large.js", large); highlighted != "" || language != "" {
		t.Fatalf("oversized file was unexpectedly highlighted as %q", language)
	}
}
