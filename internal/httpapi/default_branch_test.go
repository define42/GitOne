package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/define42/GitOne/internal/control"
	"github.com/define42/GitOne/internal/repopath"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func fixtureRepository(t *testing.T, service API) *git.Repository {
	t.Helper()
	gitPath, err := service.Storage.GitPath(repopath.Repository{
		Groups: []string{"engineering"},
		Name:   "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository, err := git.PlainOpen(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func setFixtureBranch(
	t *testing.T,
	repository *git.Repository,
	name string,
	hash plumbing.Hash,
) plumbing.ReferenceName {
	t.Helper()
	branch := plumbing.NewBranchReferenceName(name)
	if err := repository.Storer.SetReference(plumbing.NewHashReference(branch, hash)); err != nil {
		t.Fatal(err)
	}
	return branch
}

func removeFixtureBranch(t *testing.T, repository *git.Repository, name string) {
	t.Helper()
	if err := repository.Storer.RemoveReference(plumbing.NewBranchReferenceName(name)); err != nil {
		t.Fatal(err)
	}
}

func setFixtureHEAD(
	t *testing.T,
	repository *git.Repository,
	reference *plumbing.Reference,
) {
	t.Helper()
	if err := repository.Storer.SetReference(reference); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryDefaultReferences(t *testing.T) {
	for _, test := range []struct {
		name              string
		arrange           func(*testing.T, *git.Repository, plumbing.Hash)
		wantDefaultBranch string
		wantDefaultRef    string
	}{
		{
			name:              "main",
			arrange:           func(*testing.T, *git.Repository, plumbing.Hash) {},
			wantDefaultBranch: "main",
			wantDefaultRef:    "main",
		},
		{
			name: "master",
			arrange: func(t *testing.T, repository *git.Repository, hash plumbing.Hash) {
				master := setFixtureBranch(t, repository, "master", hash)
				removeFixtureBranch(t, repository, "main")
				setFixtureHEAD(t, repository, plumbing.NewSymbolicReference(plumbing.HEAD, master))
			},
			wantDefaultBranch: "master",
			wantDefaultRef:    "master",
		},
		{
			name: "trunk symbolic HEAD wins over main",
			arrange: func(t *testing.T, repository *git.Repository, hash plumbing.Hash) {
				trunk := setFixtureBranch(t, repository, "trunk", hash)
				setFixtureHEAD(t, repository, plumbing.NewSymbolicReference(plumbing.HEAD, trunk))
			},
			wantDefaultBranch: "trunk",
			wantDefaultRef:    "trunk",
		},
		{
			name: "detached HEAD remains browsable without claiming a default branch",
			arrange: func(t *testing.T, repository *git.Repository, hash plumbing.Hash) {
				setFixtureBranch(t, repository, "trunk", hash)
				removeFixtureBranch(t, repository, "main")
				setFixtureHEAD(t, repository, plumbing.NewHashReference(plumbing.HEAD, hash))
			},
			wantDefaultBranch: "",
			wantDefaultRef:    "HEAD",
		},
		{
			name: "missing symbolic default falls back deterministically",
			arrange: func(t *testing.T, repository *git.Repository, hash plumbing.Hash) {
				setFixtureBranch(t, repository, "trunk", hash)
				setFixtureBranch(t, repository, "master", hash)
				removeFixtureBranch(t, repository, "main")
				setFixtureHEAD(
					t,
					repository,
					plumbing.NewSymbolicReference(
						plumbing.HEAD,
						plumbing.NewBranchReferenceName("missing"),
					),
				)
			},
			wantDefaultBranch: "",
			wantDefaultRef:    "master",
		},
		{
			name: "tag-backed HEAD remains browsable",
			arrange: func(t *testing.T, repository *git.Repository, hash plumbing.Hash) {
				if _, err := repository.CreateTag("v1", hash, nil); err != nil {
					t.Fatal(err)
				}
				removeFixtureBranch(t, repository, "main")
				setFixtureHEAD(
					t,
					repository,
					plumbing.NewSymbolicReference(
						plumbing.HEAD,
						plumbing.NewTagReferenceName("v1"),
					),
				)
			},
			wantDefaultBranch: "",
			wantDefaultRef:    "HEAD",
		},
		{
			name: "missing default and no branches",
			arrange: func(t *testing.T, repository *git.Repository, _ plumbing.Hash) {
				removeFixtureBranch(t, repository, "main")
				setFixtureHEAD(
					t,
					repository,
					plumbing.NewSymbolicReference(
						plumbing.HEAD,
						plumbing.NewBranchReferenceName("missing"),
					),
				)
			},
			wantDefaultBranch: "",
			wantDefaultRef:    "",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, credentials, head := repositoryAPIFixture(t)
			repository := fixtureRepository(t, service)
			test.arrange(t, repository, plumbing.NewHash(head))

			output, err := service.listRepositoryBranches(
				context.Background(),
				&repositoryBranchesInput{
					AuthInput:  credentials,
					Repository: "engineering/api",
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if output.Body.DefaultBranch != test.wantDefaultBranch ||
				output.Body.DefaultRef != test.wantDefaultRef {
				t.Fatalf(
					"default branch/ref = %q/%q, want %q/%q",
					output.Body.DefaultBranch,
					output.Body.DefaultRef,
					test.wantDefaultBranch,
					test.wantDefaultRef,
				)
			}
			if !output.Body.CanWrite || !output.Body.CanManage {
				t.Fatalf(
					"owner permissions = write:%t manage:%t, want both true",
					output.Body.CanWrite,
					output.Body.CanManage,
				)
			}
			if test.wantDefaultRef != "" {
				if _, err = service.listRepositoryTree(
					context.Background(),
					credentials,
					"engineering/api",
					test.wantDefaultRef,
					"",
				); err != nil {
					t.Fatalf("default ref %q is not browsable: %v", test.wantDefaultRef, err)
				}
			}
		})
	}
}

func requireDefaultBranchStatus(t *testing.T, err error, want int) {
	t.Helper()
	var statusError huma.StatusError
	if !errors.As(err, &statusError) || statusError.GetStatus() != want {
		t.Fatalf("status error = %v, want HTTP %d", err, want)
	}
}

func TestMaintainerCanChangeRepositoryDefaultBranch(t *testing.T) {
	service, _, head := repositoryAPIFixture(t)
	repository := fixtureRepository(t, service)
	master := setFixtureBranch(t, repository, "master", plumbing.NewHash(head))
	if _, err := repository.CreateTag("v1", plumbing.NewHash(head), nil); err != nil {
		t.Fatal(err)
	}
	document, err := service.Resolver.Controls.Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	document.Members["bob"] = control.RoleMaintainer
	if err = service.Storage.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate("engineering")
	service.Resolver.Directory = testIdentityProvider{
		"alice": "secret",
		"bob":   "bob-secret",
	}
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("bob", "bob-secret")
	credentials := AuthInput{Authorization: request.Header.Get("Authorization")}

	output, err := service.updateRepositoryDefaultBranch(
		context.Background(),
		&updateRepositoryDefaultBranchInput{
			AuthInput:  credentials,
			Repository: "engineering/api",
			Body:       updateRepositoryDefaultBranchBody{Branch: "master"},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if output.Body.DefaultBranch != "master" || output.Body.DefaultRef != "master" {
		t.Fatalf("unexpected update response: %#v", output.Body)
	}
	symbolicHEAD, err := repository.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatal(err)
	}
	if symbolicHEAD.Type() != plumbing.SymbolicReference ||
		symbolicHEAD.Target() != master {
		t.Fatalf("HEAD = %s, want symbolic %s", symbolicHEAD, master)
	}

	for _, branch := range []string{"v1", "missing"} {
		_, err = service.updateRepositoryDefaultBranch(
			context.Background(),
			&updateRepositoryDefaultBranchInput{
				AuthInput:  credentials,
				Repository: "engineering/api",
				Body:       updateRepositoryDefaultBranchBody{Branch: branch},
			},
		)
		requireDefaultBranchStatus(t, err, http.StatusNotFound)
	}
}

func TestDeveloperCannotChangeRepositoryDefaultBranch(t *testing.T) {
	service, _, _ := repositoryAPIFixture(t)
	document, err := service.Resolver.Controls.Load(context.Background(), "engineering")
	if err != nil {
		t.Fatal(err)
	}
	document.Members["bob"] = control.RoleDeveloper
	if err = service.Storage.UpdateGroupControl("engineering", document, "alice"); err != nil {
		t.Fatal(err)
	}
	service.Resolver.Controls.Invalidate("engineering")
	service.Resolver.Directory = testIdentityProvider{
		"alice": "secret",
		"bob":   "bob-secret",
	}
	request, err := http.NewRequest(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.SetBasicAuth("bob", "bob-secret")
	credentials := AuthInput{Authorization: request.Header.Get("Authorization")}

	branches, err := service.listRepositoryBranches(
		context.Background(),
		&repositoryBranchesInput{
			AuthInput:  credentials,
			Repository: "engineering/api",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !branches.Body.CanWrite || branches.Body.CanManage {
		t.Fatalf(
			"developer permissions = write:%t manage:%t, want true/false",
			branches.Body.CanWrite,
			branches.Body.CanManage,
		)
	}

	_, err = service.updateRepositoryDefaultBranch(
		context.Background(),
		&updateRepositoryDefaultBranchInput{
			AuthInput:  credentials,
			Repository: "engineering/api",
			Body:       updateRepositoryDefaultBranchBody{Branch: "main"},
		},
	)
	requireDefaultBranchStatus(t, err, http.StatusForbidden)
}

func TestRepositoryDefaultBranchHTTPRoute(t *testing.T) {
	service, _, head := repositoryAPIFixture(t)
	repository := fixtureRepository(t, service)
	setFixtureBranch(t, repository, "master", plumbing.NewHash(head))
	mux := http.NewServeMux()
	Register(mux, service)

	update := httptest.NewRequest(
		http.MethodPut,
		"/api/repositories/engineering%2Fapi/default-branch",
		strings.NewReader(`{"branch":"master"}`),
	)
	update.Header.Set("Content-Type", "application/json")
	update.SetBasicAuth("alice", "secret")
	updateResponse := httptest.NewRecorder()
	mux.ServeHTTP(updateResponse, update)
	if updateResponse.Code != http.StatusOK {
		t.Fatalf(
			"update status = %d, want %d: %s",
			updateResponse.Code,
			http.StatusOK,
			updateResponse.Body.String(),
		)
	}
	var updated struct {
		DefaultBranch string `json:"defaultBranch"`
		DefaultRef    string `json:"defaultRef"`
	}
	if err := json.Unmarshal(updateResponse.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.DefaultBranch != "master" || updated.DefaultRef != "master" {
		t.Fatalf("unexpected update response: %#v", updated)
	}

	list := httptest.NewRequest(
		http.MethodGet,
		"/api/repositories/engineering%2Fapi/branches",
		nil,
	)
	list.SetBasicAuth("alice", "secret")
	listResponse := httptest.NewRecorder()
	mux.ServeHTTP(listResponse, list)
	if listResponse.Code != http.StatusOK {
		t.Fatalf(
			"list status = %d, want %d: %s",
			listResponse.Code,
			http.StatusOK,
			listResponse.Body.String(),
		)
	}
	var listed struct {
		DefaultBranch string `json:"defaultBranch"`
		DefaultRef    string `json:"defaultRef"`
		CanManage     bool   `json:"canManage"`
	}
	if err := json.Unmarshal(listResponse.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if listed.DefaultBranch != "master" ||
		listed.DefaultRef != "master" ||
		!listed.CanManage {
		t.Fatalf("unexpected branch response: %#v", listed)
	}
}
