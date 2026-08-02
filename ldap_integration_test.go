package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/define42/GitOne/internal/auth"
	"github.com/define42/GitOne/internal/control"
	gitoneserver "github.com/define42/GitOne/internal/server"
	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	gittransport "github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const glauthImage = "glauth/glauth@sha256:b3efd79fc32ac626ad1b18e36ab42fac2e2ac662454582fdfa21cc82efab786b"

func TestGitOneEndToEndWithLDAP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	ldapURL := startGlauth(ctx, t)
	directory, err := auth.NewLDAPAuthenticator(auth.LDAPConfig{
		URL:                ldapURL,
		BaseDN:             "dc=glauth,dc=com",
		UserDomain:         "example.com",
		UserFilter:         "(mail=%s)",
		CanonicalAttribute: "mail",
		SkipTLSVerify:      true,
		ConnectionTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("configure LDAP: %v", err)
	}
	sessions, err := auth.NewSessionManager(auth.SessionConfig{
		HashKey:  []byte(strings.Repeat("h", 64)),
		BlockKey: []byte(strings.Repeat("b", 32)),
		MaxAge:   time.Hour,
	})
	if err != nil {
		t.Fatalf("configure sessions: %v", err)
	}

	root := t.TempDir()
	server := httptest.NewServer(gitoneserver.New(gitoneserver.Config{
		Root:      root,
		PublicURL: "http://gitone.test",
		Directory: directory,
		Sessions:  sessions,
	}))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("create cookie jar: %v", err)
	}
	client := server.Client()
	client.Jar = jar

	t.Run("health and authentication", func(t *testing.T) {
		response := request(t, client, http.MethodGet, server.URL+"/healthz", nil)
		requireStatus(t, response, http.StatusOK)

		response = request(t, client, http.MethodGet, server.URL+"/api/groups", nil)
		requireStatus(t, response, http.StatusUnauthorized)

		response = requestJSON(t, client, http.MethodPost, server.URL+"/api/session", map[string]string{
			"username": "johndoe@example.com",
			"password": "wrong",
		})
		requireStatus(t, response, http.StatusUnauthorized)
		if len(jar.Cookies(mustParseURL(t, server.URL))) != 0 {
			t.Fatal("invalid LDAP credentials created a browser session")
		}

		response = requestJSON(t, client, http.MethodPost, server.URL+"/api/session", map[string]string{
			"username": "johndoe",
			"password": "dogood",
		})
		requireStatus(t, response, http.StatusUnauthorized)

		response = requestJSON(t, client, http.MethodPost, server.URL+"/api/session", map[string]string{
			"username": "johndoe@example.com",
			"password": "dogood",
		})
		requireStatus(t, response, http.StatusOK)
		var login struct {
			Username string `json:"username"`
		}
		decodeJSON(t, response, &login)
		if login.Username != "johndoe@example.com" {
			t.Fatalf("login returned username %q", login.Username)
		}
		cookies := jar.Cookies(mustParseURL(t, server.URL))
		if len(cookies) != 1 || cookies[0].Name != auth.SessionCookieName {
			t.Fatalf("login returned unexpected cookies: %#v", cookies)
		}

		response = request(t, client, http.MethodGet, server.URL+"/api/session", nil)
		requireStatus(t, response, http.StatusOK)
		var currentSession struct {
			Username string `json:"username"`
		}
		decodeJSON(t, response, &currentSession)
		if currentSession.Username != "johndoe@example.com" {
			t.Fatalf("session returned username %q", currentSession.Username)
		}
	})

	t.Run("group and repository workflow", func(t *testing.T) {
		response := request(
			t,
			client,
			http.MethodPost,
			server.URL+"/api/groups/engineering?description=Engineering",
			nil,
		)
		requireStatus(t, response, http.StatusCreated)
		document, loadErr := control.NewStore(root).Load(ctx, "engineering")
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if len(document.Members) != 1 ||
			document.Members["johndoe@example.com"] != control.RoleOwner {
			t.Fatalf("group owner is not the canonical LDAP identity: %#v", document.Members)
		}

		response = request(
			t,
			client,
			http.MethodPost,
			server.URL+"/api/groups/engineering%2Fbackend?description=Backend",
			nil,
		)
		requireStatus(t, response, http.StatusCreated)

		response = request(
			t,
			client,
			http.MethodPost,
			server.URL+"/api/repositories/engineering%2Fbackend%2Fapi?initializeReadme=true&description=Backend%20API",
			nil,
		)
		requireStatus(t, response, http.StatusCreated)

		response = request(
			t,
			client,
			http.MethodGet,
			server.URL+"/api/repositories/engineering%2Fbackend%2Fapi/blob/main/README.md",
			nil,
		)
		requireStatus(t, response, http.StatusOK)
		var initialBlob struct {
			Content string `json:"content"`
			CanEdit bool   `json:"canEdit"`
		}
		decodeJSON(t, response, &initialBlob)
		if initialBlob.Content != "api\n" || !initialBlob.CanEdit {
			t.Fatalf("unexpected initial README: %#v", initialBlob)
		}

		checkout := filepath.Join(t.TempDir(), "api")
		credentials := &gittransport.BasicAuth{
			Username: "johndoe@example.com",
			Password: "dogood",
		}
		repository, err := git.PlainClone(checkout, false, &git.CloneOptions{
			URL:  server.URL + "/engineering/backend/api.git",
			Auth: credentials,
		})
		if err != nil {
			t.Fatalf("clone through GitOne: %v", err)
		}
		readmePath := filepath.Join(checkout, "README.md")
		updatedReadme := "api\n\nUpdated through Git Smart HTTP.\n"
		if err = os.WriteFile(readmePath, []byte(updatedReadme), 0o644); err != nil {
			t.Fatalf("update checkout: %v", err)
		}
		worktree, err := repository.Worktree()
		if err != nil {
			t.Fatalf("open worktree: %v", err)
		}
		if _, err = worktree.Add("README.md"); err != nil {
			t.Fatalf("stage README: %v", err)
		}
		pushedCommit, err := worktree.Commit("Update README through Git", &git.CommitOptions{
			Author: &object.Signature{
				Name:  "John Doe",
				Email: "johndoe@example.com",
				When:  time.Now().UTC(),
			},
		})
		if err != nil {
			t.Fatalf("commit README: %v", err)
		}
		if err = repository.Push(&git.PushOptions{Auth: credentials}); err != nil {
			t.Fatalf("push through GitOne: %v", err)
		}

		response = request(
			t,
			client,
			http.MethodGet,
			server.URL+"/api/repositories/engineering%2Fbackend%2Fapi/blob/main/README.md",
			nil,
		)
		requireStatus(t, response, http.StatusOK)
		var pushedBlob struct {
			Commit  string `json:"commit"`
			Content string `json:"content"`
		}
		decodeJSON(t, response, &pushedBlob)
		if pushedBlob.Commit != pushedCommit.String() || pushedBlob.Content != updatedReadme {
			t.Fatalf("repository browser did not expose pushed content: %#v", pushedBlob)
		}

		response = request(
			t,
			client,
			http.MethodGet,
			server.URL+"/api/repositories/engineering%2Fbackend%2Fapi/commits/main",
			nil,
		)
		requireStatus(t, response, http.StatusOK)
		var history struct {
			Total   int `json:"total"`
			Commits []struct {
				Hash    string `json:"hash"`
				Message string `json:"message"`
			} `json:"commits"`
		}
		decodeJSON(t, response, &history)
		if history.Total != 2 ||
			len(history.Commits) != 2 ||
			history.Commits[0].Hash != pushedCommit.String() ||
			history.Commits[0].Message != "Update README through Git" {
			t.Fatalf("unexpected repository history: %#v", history)
		}
	})

	t.Run("logout revokes browser access", func(t *testing.T) {
		response := request(t, client, http.MethodDelete, server.URL+"/api/session", nil)
		requireStatus(t, response, http.StatusNoContent)

		response = request(t, client, http.MethodGet, server.URL+"/api/groups/engineering", nil)
		requireStatus(t, response, http.StatusUnauthorized)
	})
}

func startGlauth(ctx context.Context, t *testing.T) string {
	t.Helper()

	request := testcontainers.ContainerRequest{
		Image:        glauthImage,
		ExposedPorts: []string{"389/tcp"},
		Env: map[string]string{
			"GLAUTH_CONFIG": "/app/config/config.cfg",
		},
		Files: []testcontainers.ContainerFile{
			{
				HostFilePath:      pathRelative(t, "testldap", "default-config.cfg"),
				ContainerFilePath: "/app/config/config.cfg",
				FileMode:          0o644,
			},
			{
				HostFilePath:      pathRelative(t, "testldap", "cert.pem"),
				ContainerFilePath: "/app/config/cert.pem",
				FileMode:          0o644,
			},
			{
				HostFilePath:      pathRelative(t, "testldap", "key.pem"),
				ContainerFilePath: "/app/config/key.pem",
				FileMode:          0o600,
			},
		},
		WaitingFor: wait.ForLog("LDAPS server listening").
			WithStartupTimeout(time.Minute).
			WithPollInterval(250 * time.Millisecond),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: request,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start GLAUTH: %v", err)
	}
	t.Cleanup(func() {
		terminateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Errorf("terminate GLAUTH: %v", err)
		}
	})

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get GLAUTH host: %v", err)
	}
	port, err := container.MappedPort(ctx, "389/tcp")
	if err != nil {
		t.Fatalf("get GLAUTH port: %v", err)
	}
	return fmt.Sprintf("ldaps://%s:%s", host, port.Port())
}

func request(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
	body io.Reader,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return response
}

func requestJSON(
	t *testing.T,
	client *http.Client,
	method string,
	url string,
	body any,
) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req, err := http.NewRequest(method, url, bytes.NewReader(encoded))
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	return response
}

func requireStatus(t *testing.T, response *http.Response, expected int) {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		_ = response.Body.Close()
		t.Fatalf("read HTTP %d response: %v", response.StatusCode, err)
	}
	if err = response.Body.Close(); err != nil {
		t.Fatalf("close HTTP %d response: %v", response.StatusCode, err)
	}
	response.Body = io.NopCloser(bytes.NewReader(body))
	if response.StatusCode != expected {
		t.Fatalf("expected HTTP %d, got %d: %s", expected, response.StatusCode, body)
	}
}

func decodeJSON(t *testing.T, response *http.Response, destination any) {
	t.Helper()
	defer func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close HTTP %d response: %v", response.StatusCode, err)
		}
	}()
	if err := json.NewDecoder(response.Body).Decode(destination); err != nil {
		t.Fatalf("decode HTTP %d response: %v", response.StatusCode, err)
	}
}

func mustParseURL(t *testing.T, value string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("parse URL %q: %v", value, err)
	}
	return parsed
}

func pathRelative(t *testing.T, elements ...string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join(elements...))
	if err != nil {
		t.Fatalf("resolve test file: %v", err)
	}
	return path
}
