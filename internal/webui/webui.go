package webui

import (
	"bytes"
	"embed"
	"io/fs"
	"net/http"
	"strings"
	"time"
)

//go:embed dist/*
var embedded embed.FS

var public = mustSub(embedded, "dist")

type Handler struct{}

func (Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/assets/") {
		name := strings.TrimPrefix(r.URL.Path, "/assets/")
		if name == "" || !fs.ValidPath(name) || strings.Contains(name, "/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, public, name)
		return
	}

	index, err := fs.ReadFile(public, "index.html")
	if err != nil {
		http.Error(w, "could not load UI", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
}

func mustSub(source fs.FS, directory string) fs.FS {
	sub, err := fs.Sub(source, directory)
	if err != nil {
		panic(err)
	}
	return sub
}
