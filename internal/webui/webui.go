package webui

import (
	"embed"
	"html/template"
	"net/http"
	"strings"

	"github.com/define42/GitOne/internal/repopath"
	"github.com/define42/GitOne/internal/storage"
)

//go:embed index.html
var files embed.FS

var page = template.Must(template.ParseFS(files, "index.html"))

type groupLink struct {
	Name string
	URL  string
}

type pageData struct {
	AtRoot       bool
	Current      string
	Message      string
	Groups       []groupLink
	Repositories []string
	Breadcrumbs  []groupLink
}

type Handler struct {
	Storage   storage.Store
	Authorize func(*http.Request, string, bool) (string, bool)
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := r.BasicAuth(); !ok {
		w.Header().Set("WWW-Authenticate", `Basic realm="GitOne"`)
		http.Error(w, "authentication required", http.StatusUnauthorized)
		return
	}

	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/":
		h.showRoot(w, r)
	case r.Method == http.MethodGet:
		h.showGroup(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/groups":
		h.createGroup(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/subgroups":
		h.createSubgroup(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/ui/repositories":
		h.createRepository(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h Handler) showRoot(w http.ResponseWriter, r *http.Request) {
	groups, err := h.Storage.ListGroups()
	if err != nil {
		http.Error(w, "could not list groups", http.StatusInternalServerError)
		return
	}

	links := []groupLink{}
	for _, group := range groups {
		if strings.Contains(group.Path, "/") || !h.allowed(r, group.Path, false) {
			continue
		}
		links = append(links, groupLink{Name: group.Path, URL: groupURL(group.Path)})
	}
	h.render(w, pageData{
		AtRoot:  true,
		Message: message(r.URL.Query().Get("created")),
		Groups:  links,
	})
}

func (h Handler) showGroup(w http.ResponseWriter, r *http.Request) {
	parts, err := repopath.ParseGroup(r.PathValue("path"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	current := strings.Join(parts, "/")
	if !h.allowed(r, current, false) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	groups, err := h.Storage.ListGroups()
	if err != nil {
		http.Error(w, "could not list groups", http.StatusInternalServerError)
		return
	}
	var found *storage.GroupInfo
	children := []groupLink{}
	prefix := current + "/"
	for i := range groups {
		group := &groups[i]
		if group.Path == current {
			found = group
			continue
		}
		if !strings.HasPrefix(group.Path, prefix) {
			continue
		}
		name := strings.TrimPrefix(group.Path, prefix)
		if strings.Contains(name, "/") || !h.allowed(r, group.Path, false) {
			continue
		}
		children = append(children, groupLink{Name: name, URL: groupURL(group.Path)})
	}
	if found == nil {
		http.NotFound(w, r)
		return
	}

	h.render(w, pageData{
		Current:      current,
		Message:      message(r.URL.Query().Get("created")),
		Groups:       children,
		Repositories: found.Repositories,
		Breadcrumbs:  breadcrumbs(parts),
	})
}

func (h Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	values, ok := exactForm(w, r, "name")
	if !ok {
		return
	}
	name := values["name"]
	parts, err := repopath.ParseGroup(name)
	if err != nil || len(parts) != 1 {
		http.Error(w, "invalid top-level group name", http.StatusBadRequest)
		return
	}
	owner, ok := h.authorizedUser(r, name, true)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err = h.Storage.CreateGroup(name, owner); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, "/?created=group", http.StatusSeeOther)
}

func (h Handler) createSubgroup(w http.ResponseWriter, r *http.Request) {
	values, ok := exactForm(w, r, "parent", "name")
	if !ok {
		return
	}
	parent := values["parent"]
	parentParts, err := repopath.ParseGroup(parent)
	if err != nil {
		http.Error(w, "invalid parent group", http.StatusBadRequest)
		return
	}
	target := parent + "/" + values["name"]
	targetParts, err := repopath.ParseGroup(target)
	if err != nil || len(targetParts) != len(parentParts)+1 {
		http.Error(w, "invalid subgroup name", http.StatusBadRequest)
		return
	}
	owner, ok := h.authorizedUser(r, target, true)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if err = h.Storage.CreateGroup(target, owner); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, groupURL(parent)+"?created=subgroup", http.StatusSeeOther)
}

func (h Handler) createRepository(w http.ResponseWriter, r *http.Request) {
	values, ok := exactForm(w, r, "group", "name")
	if !ok {
		return
	}
	group := values["group"]
	if !h.allowed(r, group, true) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	name := strings.TrimSuffix(values["name"], ".git")
	repository, _, err := repopath.ParseGitRequestPath("/" + group + "/" + name + ".git/info/refs")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err = h.Storage.CreateRepository(repository); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	http.Redirect(w, r, groupURL(group)+"?created=repository", http.StatusSeeOther)
}

func (h Handler) allowed(r *http.Request, group string, write bool) bool {
	if h.Authorize == nil {
		return true
	}
	_, ok := h.Authorize(r, group, write)
	return ok
}

func (h Handler) authorizedUser(r *http.Request, group string, write bool) (string, bool) {
	if h.Authorize == nil {
		return "", false
	}
	return h.Authorize(r, group, write)
}

func (h Handler) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := page.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, "could not render page", http.StatusInternalServerError)
	}
}

func exactForm(w http.ResponseWriter, r *http.Request, fields ...string) (map[string]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := r.ParseForm(); err != nil || len(r.PostForm) != len(fields) {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return nil, false
	}
	values := make(map[string]string, len(fields))
	for _, field := range fields {
		if len(r.PostForm[field]) != 1 {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return nil, false
		}
		values[field] = r.PostForm.Get(field)
	}
	return values, true
}

func groupURL(group string) string {
	return "/groups/" + group
}

func breadcrumbs(parts []string) []groupLink {
	links := make([]groupLink, len(parts))
	for i, part := range parts {
		path := strings.Join(parts[:i+1], "/")
		links[i] = groupLink{Name: part, URL: groupURL(path)}
	}
	return links
}

func message(created string) string {
	switch created {
	case "group":
		return "Group created."
	case "subgroup":
		return "Subgroup created."
	case "repository":
		return "Repository created."
	default:
		return ""
	}
}
