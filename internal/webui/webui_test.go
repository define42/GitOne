package webui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandlerServesAssetsAndRejectsNestedAssetPaths(t *testing.T) {
	for _, test := range []struct {
		name        string
		path        string
		status      int
		contentType string
	}{
		{name: "asset", path: "/assets/app.js", status: http.StatusOK, contentType: "text/javascript"},
		{name: "syntax highlighter", path: "/assets/prism.min.js", status: http.StatusOK, contentType: "text/javascript"},
		{name: "empty asset", path: "/assets/", status: http.StatusNotFound},
		{name: "nested asset", path: "/assets/nested/app.js", status: http.StatusNotFound},
		{name: "invalid asset", path: "/assets/../index.html", status: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			(Handler{}).ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d", response.Code, test.status)
			}
			if test.contentType != "" && !strings.Contains(response.Header().Get("Content-Type"), test.contentType) {
				t.Fatalf("content type = %q, want %q", response.Header().Get("Content-Type"), test.contentType)
			}
		})
	}
}

func TestMustSubPanicsForMissingDirectory(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("mustSub did not panic")
		}
	}()
	_ = mustSub(fstest.MapFS{}, "../missing")
}

func TestEmbeddedDistributionContainsIndex(t *testing.T) {
	public := mustSub(embedded, "dist")
	if _, err := fs.Stat(public, "index.html"); err != nil {
		t.Fatal(err)
	}
}
