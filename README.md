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

Open [http://localhost:8080](http://localhost:8080) and enter your HTTP Basic authentication credentials when prompted. The TypeScript UI uses the Huma API to list and create groups, subgroups, and repositories. The main page lists only top-level groups. Select a group to browse its immediate subgroups and repositories.

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
| `GET` | `/api/groups/{path}` | Get a group’s immediate subgroups and repositories. |
| `POST` | `/api/groups` | Create a group. URL-encoded field: `path`. |
| `PATCH` | `/api/groups/{path}` | Rename a group. JSON field: `newPath`. |
| `DELETE` | `/api/groups/{path}` | Delete an empty group. |
| `POST` | `/api/repositories` | Create a repository. URL-encoded fields: `group`, `name`. |
| `PATCH` | `/api/repositories/{path}` | Rename a repository. JSON field: `newName`. |
| `DELETE` | `/api/repositories/{path}` | Delete a repository. |

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
