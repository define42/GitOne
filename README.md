# GitOne Server

Initial pure-Go Git Smart HTTP and Git LFS server. There is no SSH, database, native `git` binary, CGO, or UI.

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

## Administration endpoints

All administration calls use HTTP Basic authentication.

```text
GET  /healthz
POST   /api/groups
PATCH  /api/groups/{group...}
DELETE /api/groups/{group...}
POST   /api/repositories
PATCH  /api/repositories/{group...}/{repo}
DELETE /api/repositories/{group...}/{repo}
```

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

## Git Smart HTTP endpoints

```text
GET  /{group...}/{repo}.git/info/refs?service=git-upload-pack
POST /{group...}/{repo}.git/git-upload-pack
GET  /{group...}/{repo}.git/info/refs?service=git-receive-pack
POST /{group...}/{repo}.git/git-receive-pack
```

## Git LFS endpoints

```text
POST /{group...}/{repo}.git/info/lfs/objects/batch
PUT  /{group...}/{repo}.git/info/lfs/objects/{sha256}
GET  /{group...}/{repo}.git/info/lfs/objects/{sha256}
HEAD /{group...}/{repo}.git/info/lfs/objects/{sha256}
POST /{group...}/{repo}.git/info/lfs/objects/verify
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
