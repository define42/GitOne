# GitOne Server

[![codecov](https://codecov.io/gh/define42/GitOne/graph/badge.svg?token=QQLLp3t2wD)](https://codecov.io/gh/define42/GitOne)

Pure-Go Git and Git LFS server with a Huma administration API and a small TypeScript UI. 

## Storage

```text
<root>/<group>/control.git
<root>/<group>/<subgroup>/control.git
<root>/<group>/<subgroup>/<repo>.git
<root>/<group>/<subgroup>/<repo>.lfs/objects/aa/bb/<sha256>
<root>/<group>/<subgroup>/<repo>.reviews/<merge-request-id>.json
```

Every repository belongs to at least one group. Every group must contain `control.git` and is the authorization source for that group.

## Run

```bash
export LDAP_URL='ldaps://localhost:389'
export LDAP_BASE_DN='dc=glauth,dc=com'
export LDAP_USER_DOMAIN='example.com'
export LDAP_USER_FILTER='(mail=%s)'
export GITONE_SESSION_HASH_KEY='<base64-encoded 64-byte key>'
export GITONE_SESSION_BLOCK_KEY='<base64-encoded 32-byte key>'
make run RUN_ARGS="-root ./data -listen :8080 -public-url http://localhost:8080"
```

The included development directory uses a self-signed certificate, so its compose configuration sets `LDAP_SKIP_TLS_VERIFY=true`. Configure `LDAP_CA_FILE` instead for a trusted deployment. `LDAP_USER_DOMAIN` is appended to usernames that do not already contain `@`. Set `LDAP_STARTTLS=true` when using StartTLS over an `ldap://` URL. `LDAP_CONNECTION_TIMEOUT` defaults to `5s`.

The session keys encrypt and authenticate browser cookies and can be generated with `openssl rand -base64 64` and `openssl rand -base64 32`. When they are omitted, GitOne generates ephemeral keys and existing browser sessions end on restart. Sessions last 12 hours by default; configure `GITONE_SESSION_MAX_AGE` with a Go duration such as `8h`. Cookie `Secure` mode follows an HTTPS public URL and can be overridden with `GITONE_SESSION_SECURE`.

The complete development stack can be started with:

```bash
docker compose up --build
```

GitOne currently supports exactly one web-server process for each storage root.
Mutation coordination is in memory and is scoped to the affected group,
repository, LFS quota, queue, or build job so unrelated work can proceed in
parallel. Do not run multiple GitOne web instances against the same root or
modify that root from another process. Remote `gitone-runner` workers remain
supported because they mutate server state only through the web API.

## Web UI

Open [http://localhost:8080](http://localhost:8080) and sign in with LDAP credentials. After LDAP validation, GitOne stores only the username in a Gorilla securecookie that is signed, encrypted, `HttpOnly`, and `SameSite=Strict`; the password is not retained. Every authenticated LDAP user can create a top-level group and becomes its owner. Creating a subgroup requires maintainer access inherited from its parent; the subgroup starts without direct members, so its creator and all other users keep the roles they inherit from parent groups. The GitOne-branded TypeScript UI uses the Huma API to list and create groups, subgroups, and repositories. Dark is the default color theme; the header selector persists Light, Dark, Steampunk, Windows, Mac OS X, Ubuntu, Solaris, GitHub, and GitLab palettes in the browser.

The main page lists only top-level groups and their descriptions. Select a group to see its immediate subgroups and repositories. Group maintainers can create repositories, mirror every Git ref and tag from an HTTP(S) remote, or upload a ZIP/TAR archive containing a bare Git repository; Git LFS objects remain separate and are not imported. Group maintainers can open Settings to change the group name and description, inheritance, and non-owner group tokens. Only group owners can change members and roles, repository visibility, the LFS policy, or owner tokens. Every save creates a commit in `control.git`, and renaming a group updates descendant control documents. Repository pages let maintainers rename the repository, provide a copyable `git clone` command containing the authenticated username, such as `git clone http://alice@localhost:8080/engineering/api.git`, and download the selected branch, tag, or commit as ZIP or tar.gz.

The repository viewer follows each repository's symbolic `HEAD`, can browse files with server-side Chroma syntax highlighting, show line-by-line blame attribution, page through the complete selected branch history, expand any commit to inspect its file statistics and unified diff, create a branch from any existing branch, and compare two branches. Maintainers can select an existing branch as the repository default. Its Builds tab shows queued, running, successful, and failed jobs, polls active jobs automatically, and exposes expandable live logs. Users with developer access can create, edit, rename, and delete UTF-8 files up to 1 MiB directly on a named branch and review edited contents as a unified diff; each operation creates one commit and rejects the update if the branch changed after the editor was opened. GitOne fast-forwards linear histories and creates a two-parent merge commit for clean divergent histories; conflicting branches are never moved. Repositories can be deleted from the group danger zone only after entering the exact repository name. Groups can be deleted after all repositories and subgroups have been removed and the exact full group path is entered.

Comparisons can be saved as merge requests with durable Markdown descriptions and threaded, resolvable discussions. Approvals are bound to the exact source commit, so a new push requires a new approval. Authors cannot approve their own changes unless they are a group maintainer or owner. An approval merges automatically when the request is conflict-free and all discussions are resolved; an explicit retry action is available after clearing a previous blocker.

Build the UI separately with:

```bash
make ui
```

## Repository builds

GitOne builds are split across two applications and container images. The `gitone` web server owns repositories, the durable build queue, logs, and the runner API. The separate `gitone-runner` worker claims jobs over that API and is the only application that needs access to Docker. After a successful branch update, the server reads the build definition from `.gitone.yaml` at the exact new commit and persists a queued job. A runner claims the job with a renewable lease, downloads an exact-commit source archive, runs the script in an ephemeral Docker-compatible container, and streams logs and completion state back to GitOne. Branches created, edited, or merged through the API trigger builds too.

```yaml
description: Backend API
build:
  image: golang:1.25
  script:
    - go test ./...
    - go build ./...
  branches:
    - main
    - release/*
  environment:
    CGO_ENABLED: "0"
  timeoutSeconds: 1200
```

`image` and at least one non-empty `script` command are required. Commands run in order through `/bin/sh -ec` with the repository at `/workspace`. `branches` contains path-style glob patterns and defaults to every branch. `timeoutSeconds` defaults to 900 and is capped at 3600. Repository variables cannot replace reserved `CI_*` or `GITONE_*` variables; GitOne provides `CI_COMMIT_SHA`, `CI_COMMIT_BRANCH`, `CI_PROJECT_PATH`, `GITONE_BUILD_ID`, and equivalent GitOne commit variables.

The remote runner API is disabled until `GITONE_RUNNER_TOKEN` is configured on the GitOne server:

```bash
GITONE_RUNNER_TOKEN="$(openssl rand -hex 32)" make run RUN_ARGS="-root ./data"
```

Start the worker on the runner server with the same token:

```bash
docker build -f Dockerfile.runner -t gitone-runner:local .
docker run --rm \
  -e GITONE_RUNNER_TOKEN="$GITONE_RUNNER_TOKEN" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /var/lib/gitone-runner:/var/lib/gitone-runner \
  gitone-runner:local \
    -runner-url https://gitone.example \
    -runner-id build-server-1 \
    -runner-work-root /var/lib/gitone-runner
```

The runner makes outbound HTTP(S) requests only and does not mount GitOne's `/data`. Its work root must be bind-mounted at the same absolute path on the runner host because the local Docker daemon bind-mounts each downloaded workspace into the build container. The default runner command is `docker`; use `-runner-command podman` for a Docker-compatible alternative. `-runner-workers` controls concurrency and defaults to one. The web image is built from `Dockerfile`; the worker image is built from `Dockerfile.runner`. `docker compose up --build` builds and starts both.

The Docker socket is a privileged host capability and belongs only on the runner server. Build containers do not receive that socket. The GitOne server retains repositories, durable queue state, and logs beside each bare repository under `<root>/<group>/<repository>.build`; stored logs are capped at 10 MiB and API log responses at 1 MiB. Leases allow another runner to reclaim work after a runner failure or network loss.

## Endpoint reference

`{path...}` and `{group...}` may contain multiple slash-separated group levels. Huma `{path}` parameters contain an entire group or repository path encoded as one URL segment, for example `engineering%2Fbackend`. Browser administration requests use the secure session cookie. The API also accepts HTTP Basic authentication for scripts and group automation tokens. Native Git and LFS operations continue to use HTTP Basic authentication. Git and LFS reads follow group visibility: `public` permits anonymous reads, `internal` accepts any authenticated LDAP identity, and `private` requires group access. Writes always require group developer access. A group's `control.git` remains private regardless of group visibility. Health and Huma documentation endpoints are public.

### Health

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Return `{"status":"ok"}`. |

### Remote runner

These endpoints require `Authorization: Bearer <GITONE_RUNNER_TOKEN>`.

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/api/runner/jobs/claim` | Claim the oldest queued or expired build lease. |
| `POST` | `/api/runner/jobs/heartbeat` | Renew a claimed build lease. |
| `POST` | `/api/runner/jobs/log` | Append an offset-checked build log chunk. |
| `POST` | `/api/runner/jobs/complete` | Record build success or failure. |
| `GET` | `/api/runner/source` | Download the exact leased commit as a source archive. |

### Web UI

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/` | Load the TypeScript UI at the top-level group view. |
| `GET` | `/groups/{path...}` | Load the TypeScript UI at a group or subgroup. |
| `GET` | `/repositories/{path...}` | Browse a repository tree, files, and recent commits. |
| `GET` | `/assets/{path...}` | Serve embedded compiled JavaScript and CSS assets. |

### Huma documentation

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/docs` | Interactive API documentation. |
| `GET` | `/openapi.json` | OpenAPI 3.1 document as JSON. |
| `GET` | `/openapi.yaml` | OpenAPI 3.1 document as YAML. |
| `GET` | `/openapi-3.0.json` | OpenAPI 3.0 document as JSON. |
| `GET` | `/openapi-3.0.yaml` | OpenAPI 3.0 document as YAML. |
| `GET` | `/schemas/{schema}` | JSON Schema generated for an API model. |

### Administration API

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/api/session` | Validate LDAP credentials and create the signed and encrypted browser session cookie. |
| `GET` | `/api/session` | Return the username from the current browser session. |
| `DELETE` | `/api/session` | Clear the current browser session. |
| `GET` | `/api/groups` | List accessible top-level groups. |
| `GET` | `/api/groups/{path}` | Get a group’s description, immediate subgroups, and repositories with their descriptions. |
| `GET` | `/api/groups/{path}/settings` | Get the complete `control.json` document for a maintainer-authorized group. |
| `POST` | `/api/groups/{path}` | Create a group. Any LDAP user may create a top-level group; nested groups require parent maintainer access. Optional query parameter: `description`. |
| `PUT` | `/api/groups/{path}/settings` | Replace group control settings and optionally rename the group through the `name` field. Changing members, visibility, LFS policy, or owner tokens requires owner access. |
| `PATCH` | `/api/groups/{path}` | Rename or move a group. JSON field: `newPath`. A cross-parent move requires maintainer access to the source group and both non-root parent groups. |
| `DELETE` | `/api/groups/{path}` | Delete an empty group. |
| `POST` | `/api/repositories/{path}` | Create a repository. `path` is the URL-encoded full `group/repository` path. Optional query parameters: `description`, and `initializeReadme=true` to create `README.md` on `main`. A description is stored in `.gitone.yaml`. |
| `POST` | `/api/repositories/{path}/import` | Mirror all Git refs and tags from an HTTP or HTTPS remote into a new bare repository. JSON fields: `url`, optional `username`, and optional `password` or access token. Git LFS objects are not imported. |
| `POST` | `/api/repositories/{path}/import-archive?filename=repository.tar.gz` | Upload a `.zip`, `.tar`, `.tar.gz`, or `.tgz` file as the raw request body. The archive must contain one bare Git repository at its root or in one enclosing folder and may be up to 1 GiB compressed. Git LFS objects are not imported. |
| `PATCH` | `/api/repositories/{path}` | Rename a repository. JSON field: `newName`. |
| `DELETE` | `/api/repositories/{path}` | Delete a repository. |

### Repository builds

Build endpoints require repository read access. Existing build history remains readable while the runner is disabled.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/api/repositories/{repository}/builds` | List builds newest-first with queued, running, succeeded, or failed status. |
| `GET` | `/api/repositories/{repository}/builds/{id}` | Get one build and its captured log. |

### Repository browser API

The `{repository}`, `{ref}`, and in-repository `{path}` parameters are URL-encoded as individual path segments. Blob responses use UTF-8 text when possible and base64 for binary content. Recognized UTF-8 source files up to 1 MiB also include Chroma-generated `language` and escaped `highlightedHtml` fields. Tree `canEdit` enables file creation, while blob `canEdit` enables text editing and `canManage` enables rename/delete; these capabilities require a named branch and developer access. Browsable files are limited to 10 MiB.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/api/repositories/{repository}/branches` | List branches and their tip commits, the detected `defaultBranch`, a browsable `defaultRef`, and write/manage capabilities. |
| `PUT` | `/api/repositories/{repository}/default-branch` | Set symbolic `HEAD` to an existing branch. Requires maintainer access and a JSON `branch` field. |
| `POST` | `/api/repositories/{repository}/branches/{branch}` | Create a branch. `branch` is URL-encoded and the required `from` query parameter names an existing source branch. |
| `GET` | `/api/repositories/{repository}/compare` | Compare the `head` branch with the `base` target branch. Returns ahead/behind counts, mergeability, conflict paths, and unified file diffs. |
| `POST` | `/api/repositories/{repository}/merges` | Merge a `source` branch into a `target` branch when no conflicts exist. The optional JSON `message` field sets the merge commit message. |
| `GET` | `/api/repositories/{repository}/merge-requests` | List persisted merge requests, filtered with `state=open`, `closed`, `merged`, or `all`. |
| `POST` | `/api/repositories/{repository}/merge-requests` | Create a merge request for a source and target branch. |
| `GET` | `/api/repositories/{repository}/merge-requests/{id}` | Get a request with its comparison, approvals, and discussions. |
| `PATCH` | `/api/repositories/{repository}/merge-requests/{id}` | Close or reopen a request. |
| `POST` | `/api/repositories/{repository}/merge-requests/{id}/threads` | Start a discussion thread. |
| `POST` | `/api/repositories/{repository}/merge-requests/{id}/threads/{threadId}/comments` | Reply to a discussion thread. |
| `PATCH` | `/api/repositories/{repository}/merge-requests/{id}/threads/{threadId}` | Resolve or reopen a discussion thread. |
| `POST` | `/api/repositories/{repository}/merge-requests/{id}/approvals` | Approve an exact source commit and merge automatically when ready. |
| `POST` | `/api/repositories/{repository}/merge-requests/{id}/merge` | Retry an approved merge after clearing a conflict or unresolved discussion. |
| `GET` | `/api/repositories/{repository}/tree/{ref}` | List the repository root at a branch, tag, hash, or `HEAD`. |
| `GET` | `/api/repositories/{repository}/tree/{ref}/{path}` | List a directory at a Git reference. |
| `GET` | `/api/repositories/{repository}/blob/{ref}/{path}` | Read a file as UTF-8 or base64. |
| `GET` | `/api/repositories/{repository}/blame/{ref}/{path}` | Attribute every line in a UTF-8 file up to 1 MiB to its introducing commit and author. |
| `GET` | `/api/repositories/{repository}/archives/{ref}?format=zip` | Download the selected branch, tag, hash, or `HEAD` tree as `zip` or `tar.gz`. |
| `POST` | `/api/repositories/{repository}/files/{ref}/{path}` | Create a UTF-8 file on a named branch. JSON fields: `content`, optional `message`, and required `expectedCommit`. |
| `PUT` | `/api/repositories/{repository}/files/{ref}/{path}` | Replace an existing UTF-8 file on a named branch and commit it. JSON fields: `content`, optional `message`, and required optimistic-lock `expectedCommit`. |
| `PATCH` | `/api/repositories/{repository}/files/{ref}/{path}` | Rename a file on a named branch. JSON fields: `newPath`, optional `message`, and required `expectedCommit`. |
| `DELETE` | `/api/repositories/{repository}/files/{ref}/{path}` | Delete a file on a named branch. JSON fields: optional `message` and required `expectedCommit`. |
| `GET` | `/api/repositories/{repository}/commits/{ref}` | List commits with one-based `page` and `perPage` (1–100, default 50), plus total and next/previous-page metadata. |
| `GET` | `/api/repositories/{repository}/commits/{commit}/diff` | Show the changes introduced by a full commit hash relative to its first parent, or an empty tree for an initial commit. |

### Git Smart HTTP

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/{group...}/{repo}.git/info/refs?service=git-upload-pack` | Advertise fetch and clone references. |
| `POST` | `/{group...}/{repo}.git/git-upload-pack` | Fetch or clone Git objects. |
| `GET` | `/{group...}/{repo}.git/info/refs?service=git-receive-pack` | Advertise push references. |
| `POST` | `/{group...}/{repo}.git/git-receive-pack` | Push Git objects and reference updates. |

GitOne currently serves Git Smart HTTP protocol v0. Clients requesting protocol v2
fall back to v0; this compatibility path is exercised with the native Git client.
Every successfully applied branch update triggers its build scheduling callback,
even when another reference update in the same non-atomic push fails.

### Git LFS

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/{group...}/{repo}.git/info/lfs/objects/batch` | Negotiate LFS uploads or downloads. |
| `PUT` | `/{group...}/{repo}.git/info/lfs/objects/{sha256}` | Upload an LFS object. |
| `GET` | `/{group...}/{repo}.git/info/lfs/objects/{sha256}` | Download an LFS object. |
| `HEAD` | `/{group...}/{repo}.git/info/lfs/objects/{sha256}` | Read LFS object metadata. |
| `POST` | `/{group...}/{repo}.git/info/lfs/objects/verify` | Verify an LFS upload. |

Upload negotiation advertises both upload and verify actions. Verification checks
that the requested OID exists and that its stored size matches the request. Git
pushes are rejected before references move when newly pushed history contains an
LFS pointer whose object is missing or has the wrong size.

## Administration examples

Create the first group with LDAP credentials. The authenticated user becomes its owner:

```bash
curl -u alice:directory-password -X POST \
  'http://localhost:8080/api/groups/engineering?description=Engineering%20projects'
```

Create a subgroup as its parent owner:

```bash
curl -u alice:directory-password -X POST \
  'http://localhost:8080/api/groups/engineering%2Fbackend?description=Backend%20services'
```

Create a repository:

```bash
curl -u alice:directory-password -X POST \
  'http://localhost:8080/api/repositories/engineering%2Fbackend%2Fapi?initializeReadme=true&description=Backend%20API'
```

Import a bare repository archive:

```bash
curl -u alice:directory-password -X POST \
  -H 'Content-Type: application/gzip' \
  --data-binary @api.git.tar.gz \
  'http://localhost:8080/api/repositories/engineering%2Fbackend%2Fapi/import-archive?filename=api.git.tar.gz'
```

Uploaded archives are extracted with entry-count and expanded-size limits. Paths that escape the archive root, symbolic or hard links, device files, and other special files are rejected. Imported hooks and object alternates are removed before the repository becomes available.

Clone the repository and enter the LDAP password when Git prompts:

```bash
git clone http://alice@localhost:8080/engineering/backend/api.git
```

List top-level groups:

```bash
curl -u alice:directory-password http://localhost:8080/api/groups
```

Read a nested group:

```bash
curl -u alice:directory-password \
  http://localhost:8080/api/groups/engineering%2Fbackend
```

## control.json

```json
{
  "version": 4,
  "group": "engineering/backend",
  "description": "Backend services",
  "inherit": true,
  "visibility": "private",
  "lfs": {
    "enabled": true,
    "maximumObjectBytes": 10737418240,
    "maximumStorageBytes": 107374182400
  },
  "members": {
    "alice": "owner"
  },
  "tokens": [
    {
      "name": "CI deploy",
      "key": "ci",
      "hash": "$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>",
      "role": "developer"
    }
  ]
}
```

Member entries contain only LDAP usernames and roles. GitOne binds directly with the submitted username plus the optional `LDAP_USER_DOMAIN`, searches for that authenticated identifier using `LDAP_USER_FILTER`, then matches the submitted username exactly against `members`; member passwords are never stored in `control.json`.

Group tokens are available for automation and use salted Argon2id hashes. The settings API accepts a new token secret only for the duration of an update and hashes it on the server. A token's `key` is its HTTP Basic username, while `name` is only its display label. Its role applies to the whole group and follows the same `inherit` boundary as member access for subgroups.

### Role permissions

Roles are cumulative: `owner` includes `maintainer`, `maintainer` includes `developer`, and `developer` includes `read`.

| Capability | `read` | `developer` | `maintainer` | `owner` |
|---|:---:|:---:|:---:|:---:|
| Browse repositories; clone and fetch | ✓ | ✓ | ✓ | ✓ |
| Read commits, diffs, builds, and merge requests | ✓ | ✓ | ✓ | ✓ |
| Create review threads and comments | ✓ | ✓ | ✓ | ✓ |
| Resolve a thread as its author or the merge-request author | ✓ | ✓ | ✓ | ✓ |
| Push Git changes and upload LFS objects | — | ✓ | ✓ | ✓ |
| Edit files and create branches through the web API | — | ✓ | ✓ | ✓ |
| Create or update merge requests; approve others; merge approved requests | — | ✓ | ✓ | ✓ |
| Resolve any review thread | — | ✓ | ✓ | ✓ |
| Create subgroups and repositories; import repository archives or mirrors | — | — | ✓ | ✓ |
| Rename repositories or groups; delete repositories or empty groups | — | — | ✓ | ✓ |
| Change group name, description, inheritance, and non-owner tokens | — | — | ✓ | ✓ |
| Move a group to a different parent[^cross-parent-move] | — | — | Conditional | Conditional |
| Change members and their roles | — | — | — | ✓ |
| Change repository visibility or LFS policy | — | — | — | ✓ |
| Create, modify, or delete owner tokens | — | — | — | ✓ |
| Push directly to the private `control.git` repository | — | — | — | ✓ |
| Approve one's own merge request | — | — | ✓ | ✓ |

[^cross-parent-move]: A cross-parent move requires maintainer-or-higher access to the source group and to both non-root parent groups.

The closest explicit group assignment wins. When `inherit` is enabled, GitOne searches parent groups until it finds an assignment; disabling inheritance stops that search. The same matrix applies to group tokens. Top-level groups and groups with inheritance disabled must retain at least one owner in `members`; inherited subgroups may have no direct members. Creating a top-level group is separate from this matrix: any authenticated LDAP user may create one and becomes its owner. Repository visibility can grant browsing and Git clone/fetch access to ordinary repositories without an explicit role—`public` permits anonymous reads and `internal` permits authenticated LDAP users—but never grants developer, maintainer, owner, or access to `control.git`.

New groups are private with Git LFS enabled and unlimited quotas. The group policy applies to every ordinary repository in the group. `maximumObjectBytes` limits each object, `maximumStorageBytes` limits aggregate LFS storage across the group's repositories, and zero means unlimited within the server's absolute upload guard.

Control schema version 4 renames the `write` role to `developer`. Before upgrading a populated version-3 server, replace every `write` member or token role with `developer` and set each document's `version` to `4`. When upgrading from version 2, also replace every `admin` role with `maintainer`. When upgrading from version 1, additionally add explicit group `visibility` and `lfs` policies, remove `repositories` from every token, and remove the top-level `repositories` map. Earlier schema versions are rejected rather than interpreted with potentially unsafe defaults.

## Tests

```bash
make test
```

## Current implementation boundaries

This is an initial server implementation, not yet a production release. LDAP connections support TLS certificate verification, a custom CA file, bounded search results, escaped filters, and configurable timeouts. Receive-pack rejects stale old SHAs, validates proposed `control.json` commits, and permits only fast-forward updates to `control.git`. LFS enablement, object limits, and storage quotas are enforced at upload time within one server process. Before production use, add LFS-pointer existence checks during Git pushes, multi-process upload reservations and ref coordination, rate limiting, HTTP TLS termination, audit logs, and broader compatibility tests against native Git clients.
