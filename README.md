# GitOne Server

Initial pure-Go Git Smart HTTP and Git LFS server with a Huma administration API and a small TypeScript UI. There is no SSH, database, native `git` binary, or CGO.

## Storage

```text
<root>/<group>/control.git
<root>/<group>/<subgroup>/control.git
<root>/<group>/<subgroup>/<repo>.git
<root>/<group>/<subgroup>/<repo>.lfs/objects/aa/bb/<sha256>
```

Every repository belongs to at least one group. Every group must contain `control.git`. Its only permitted branch is `main`; branch creation, deletion, tags, notes, and other refs are rejected by the server. `control.json` is read from `main` and is the authorization source.

## Run

```bash
export GITONE_BOOTSTRAP_USER=bootstrap
export GITONE_BOOTSTRAP_TOKEN='replace-me'
make run RUN_ARGS="-root ./data -listen :8080 -public-url http://localhost:8080"
```

## Web UI

Open [http://localhost:8080](http://localhost:8080) and enter your HTTP Basic authentication credentials when prompted. The GitOne-branded TypeScript UI uses the Huma API to list and create groups, subgroups, and repositories. Dark is the default color theme; the header selector persists Light, Dark, Steampunk, Windows, Mac OS X, Ubuntu, Solaris, GitHub, and GitLab palettes in the browser. The main page lists only top-level groups and their descriptions. Select a group to see its immediate subgroups and repositories. Group admins can open Settings to change the group name and description, inheritance, members and roles, tokens, and repository visibility and LFS policies; every save creates a commit in `control.git`, and renaming a group updates descendant control documents. Repository pages provide a copyable clone URL containing the authenticated username, such as `http://alice@localhost:8080/engineering/api.git`. The repository viewer can browse files with server-side Chroma syntax highlighting, show the latest 100 commits in the selected branch’s history, expand any commit to inspect its file statistics and unified diff, create a branch from any existing branch, and compare two branches. Users with write access can edit UTF-8 files up to 1 MiB directly on a named branch and review their uncommitted changes as a unified diff; each save creates a commit and rejects the update if the branch changed after the editor was opened. They can also merge a clean comparison after confirmation. GitOne fast-forwards linear histories and creates a two-parent merge commit for clean divergent histories; conflicting branches are never moved. Repositories can be deleted from the group danger zone only after entering the exact repository name. Groups can be deleted after all repositories and subgroups have been removed and the exact full group path is entered.

Build the UI separately with:

```bash
make ui
```

## Endpoint reference

`{path...}` and `{group...}` may contain multiple slash-separated group levels. Huma `{path}` parameters contain an entire group or repository path encoded as one URL segment, for example `engineering%2Fbackend`. UI, administration, Git, and LFS requests use HTTP Basic authentication. Health and Huma documentation endpoints are public.

### Health

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Return `{"status":"ok"}`. |

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
| `GET` | `/api/groups` | List accessible top-level groups. |
| `GET` | `/api/groups/{path}` | Get a group’s description, immediate subgroups, and repositories with their descriptions. |
| `GET` | `/api/groups/{path}/settings` | Get the complete `control.json` document for an admin-authorized group. |
| `POST` | `/api/groups/{path}` | Create a group. `path` is the URL-encoded full group path. Optional query parameter: `description`. |
| `PUT` | `/api/groups/{path}/settings` | Replace group control settings and optionally rename the group through the `name` field. |
| `PATCH` | `/api/groups/{path}` | Rename a group. JSON field: `newPath`. |
| `DELETE` | `/api/groups/{path}` | Delete an empty group. |
| `POST` | `/api/repositories/{path}` | Create a repository. `path` is the URL-encoded full `group/repository` path. Optional query parameters: `description`, and `initializeReadme=true` to create `README.md` on `main`. A description is stored in `.gitone.json`. |
| `PATCH` | `/api/repositories/{path}` | Rename a repository. JSON field: `newName`. |
| `DELETE` | `/api/repositories/{path}` | Delete a repository. |

### Repository browser API

The `{repository}`, `{ref}`, and in-repository `{path}` parameters are URL-encoded as individual path segments. Blob responses use UTF-8 text when possible and base64 for binary content. Recognized UTF-8 source files up to 1 MiB also include Chroma-generated `language` and escaped `highlightedHtml` fields. The `canEdit` field is true for editable files on named branches when the caller has write access. Browsable files are limited to 10 MiB.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/api/repositories/{repository}/branches` | List branches and their tip commits. The UI defaults to `main`. |
| `POST` | `/api/repositories/{repository}/branches/{branch}` | Create a branch. `branch` is URL-encoded and the required `from` query parameter names an existing source branch. |
| `GET` | `/api/repositories/{repository}/compare` | Compare the `head` branch with the `base` target branch. Returns ahead/behind counts, mergeability, conflict paths, and unified file diffs. |
| `POST` | `/api/repositories/{repository}/merges` | Merge a `source` branch into a `target` branch when no conflicts exist. The optional JSON `message` field sets the merge commit message. |
| `GET` | `/api/repositories/{repository}/tree/{ref}` | List the repository root at a branch, tag, hash, or `HEAD`. |
| `GET` | `/api/repositories/{repository}/tree/{ref}/{path}` | List a directory at a Git reference. |
| `GET` | `/api/repositories/{repository}/blob/{ref}/{path}` | Read a file as UTF-8 or base64. |
| `PUT` | `/api/repositories/{repository}/files/{ref}/{path}` | Replace an existing UTF-8 file on a named branch and commit it. JSON fields: `content`, optional `message`, and required optimistic-lock `expectedCommit`. |
| `GET` | `/api/repositories/{repository}/commits/{ref}` | List commits. Optional `limit` is 1–100 and defaults to 20. |
| `GET` | `/api/repositories/{repository}/commits/{commit}/diff` | Show the changes introduced by a full commit hash relative to its first parent, or an empty tree for an initial commit. |

### Git Smart HTTP

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/{group...}/{repo}.git/info/refs?service=git-upload-pack` | Advertise fetch and clone references. |
| `POST` | `/{group...}/{repo}.git/git-upload-pack` | Fetch or clone Git objects. |
| `GET` | `/{group...}/{repo}.git/info/refs?service=git-receive-pack` | Advertise push references. |
| `POST` | `/{group...}/{repo}.git/git-receive-pack` | Push Git objects and reference updates. |

### Git LFS

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/{group...}/{repo}.git/info/lfs/objects/batch` | Negotiate LFS uploads or downloads. |
| `PUT` | `/{group...}/{repo}.git/info/lfs/objects/{sha256}` | Upload an LFS object. |
| `GET` | `/{group...}/{repo}.git/info/lfs/objects/{sha256}` | Download an LFS object. |
| `HEAD` | `/{group...}/{repo}.git/info/lfs/objects/{sha256}` | Read LFS object metadata. |
| `POST` | `/{group...}/{repo}.git/info/lfs/objects/verify` | Verify an LFS upload. |

## Administration examples

Create the first group with the bootstrap credentials:

```bash
curl -u bootstrap:replace-me -X POST \
  'http://localhost:8080/api/groups/engineering?description=Engineering%20projects'
```

Create a subgroup using an owner/admin token inherited from its parent:

```bash
curl -u bootstrap:replace-me -X POST \
  'http://localhost:8080/api/groups/engineering%2Fbackend?description=Backend%20services'
```

Create a repository:

```bash
curl -u bootstrap:replace-me -X POST \
  'http://localhost:8080/api/repositories/engineering%2Fbackend%2Fapi?initializeReadme=true&description=Backend%20API'
```

Clone the repository and enter the bootstrap token when Git prompts for a password:

```bash
git clone http://bootstrap@localhost:8080/engineering/backend/api.git
```

List top-level groups:

```bash
curl -u bootstrap:replace-me http://localhost:8080/api/groups
```

Read a nested group:

```bash
curl -u bootstrap:replace-me \
  http://localhost:8080/api/groups/engineering%2Fbackend
```

## control.json

```json
{
  "version": 1,
  "group": "engineering/backend",
  "description": "Backend services",
  "inherit": true,
  "members": {
    "alice": "owner"
  },
  "tokens": [
    {
      "name": "ci",
      "key": "ci",
      "hash": "sha256:<hex>",
      "role": "write"
    }
  ],
  "repositories": {
    "api": {
      "visibility": "private",
      "lfs": {
        "enabled": true,
        "maximumObjectBytes": 10737418240,
        "maximumStorageBytes": 107374182400
      }
    }
  }
}
```

The initial implementation uses `sha256:` token hashes to keep bootstrap interoperability simple. Replace this with fully parsed Argon2id hashes before exposing the service publicly.

## Tests

```bash
make test
```

## Current implementation boundaries

This is an initial server implementation, not yet a production release. The receive-pack path uses go-git's pure-Go server implementation. Before production use, add pre-ref validation that reads the proposed `control.json`, fast-forward enforcement for `control.git`, LFS-pointer existence checks, quotas, coordinated rename/delete endpoints, upload reservations, rate limiting, TLS termination, and broader compatibility tests against native Git clients.
