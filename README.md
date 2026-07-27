# GitOne Server

Initial pure-Go Git Smart HTTP and Git LFS server. There is no SSH, database, native `git` binary, or CGO.

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
go run ./cmd/gitone -root ./data -listen :8080 -public-url http://localhost:8080
```

## Web UI

Open [http://localhost:8080](http://localhost:8080) and enter your HTTP Basic authentication credentials when prompted. The main page lists only top-level groups. Select a group to browse its immediate subgroups and repositories; subgroup names link to the next level. Top-level groups are created from the main page, while subgroups and repositories are created from the current group page. The UI uses standard HTML forms and does not require JavaScript.

## Endpoint reference

`{path...}` and `{group...}` may contain multiple slash-separated group levels. All UI, administration, Git, and LFS endpoints use HTTP Basic authentication. The health endpoint is public.

### Health

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Return `{"status":"ok"}`. |

### Web UI

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/` | List accessible top-level groups and show the top-level group form. |
| `GET` | `/groups/{path...}` | Show one group, its immediate subgroups, repositories, and creation forms. |
| `POST` | `/ui/groups` | Create a top-level group. URL-encoded field: `name`. |
| `POST` | `/ui/subgroups` | Create a subgroup. URL-encoded fields: `parent`, `name`. |
| `POST` | `/ui/repositories` | Create a repository. URL-encoded fields: `group`, `name`. |

### Administration API

| Method | Path | Description |
| --- | --- | --- |
| `POST` | `/api/groups` | Create a group. URL-encoded field: `path`. |
| `PATCH` | `/api/groups/{group...}` | Rename a group. JSON field: `newPath`. |
| `DELETE` | `/api/groups/{group...}` | Delete an empty group. |
| `POST` | `/api/repositories` | Create a repository. URL-encoded fields: `group`, `name`. |
| `PATCH` | `/api/repositories/{group...}/{repo}` | Rename a repository. JSON field: `newName`. |
| `DELETE` | `/api/repositories/{group...}/{repo}` | Delete a repository. |

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
curl -u bootstrap:replace-me -X POST http://localhost:8080/api/groups \
  --data-urlencode 'path=engineering'
```

Create a subgroup using an owner/admin token inherited from its parent:

```bash
curl -u bootstrap:replace-me -X POST http://localhost:8080/api/groups \
  --data-urlencode 'path=engineering/backend'
```

Create a repository:

```bash
curl -u bootstrap:replace-me -X POST http://localhost:8080/api/repositories \
  --data-urlencode 'group=engineering/backend' \
  --data-urlencode 'name=api'
```

## control.json

```json
{
  "version": 1,
  "group": "engineering/backend",
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
go test ./...
```

## Current implementation boundaries

This is an initial server implementation, not yet a production release. The receive-pack path uses go-git's pure-Go server implementation. Before production use, add pre-ref validation that reads the proposed `control.json`, fast-forward enforcement for `control.git`, LFS-pointer existence checks, quotas, coordinated rename/delete endpoints, upload reservations, rate limiting, TLS termination, and broader compatibility tests against native Git clients.
