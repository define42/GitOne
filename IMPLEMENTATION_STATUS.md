# Implementation status

Implemented:

- Pure-Go HTTP server structure
- Nested group/repository path parsing and traversal protection
- Mandatory `control.git` creation for every group
- Initial `control.json` commit on `main`
- Main-only receive-pack ref validation for `control.git`
- Git Smart HTTP discovery, upload-pack, and receive-pack handlers using go-git
- Native Git protocol-v2 client fallback coverage for Smart HTTP clones
- Per-repository sibling LFS storage (`repo.git` next to `repo.lfs`)
- LFS Batch API and basic PUT/GET/HEAD/verify endpoints
- Receive-pack validation of LFS pointer object existence and size
- Group create/delete/rename endpoints
- Repository create/delete/rename endpoints
- Huma administration API with generated OpenAPI and interactive documentation
- Embedded TypeScript UI for browsing and creating groups and repositories
- LDAP user authentication with Git-backed username roles and inheritance
- LDAP login with signed and encrypted Gorilla securecookie browser sessions
- Git-backed Argon2id automation tokens with repository scopes
- Stale-ref rejection and validated fast-forward-only control updates
- Group-wide repository visibility and LFS policy enforcement
- Go and TypeScript build validation plus a multi-stage Dockerfile
- Separate `gitone` web and `gitone-runner` applications and container images, connected through an authenticated build API with durable leases, exact-commit source archives, remote log streaming, branch filters, timeouts, build APIs, and a live Builds UI
- Repository ZIP/tar.gz downloads and branch-safe UI/API operations for creating, editing, renaming, and deleting files
- Repository blame views and complete commit-history navigation with paginated API results
- Persisted merge requests with exact-commit approvals, threaded discussions, and approval-gated merging

Not production-complete yet:

- Restrict the control tree to only `control.json`
- Cross-process locking and atomic multi-path rename/delete transactions
- Complete Git client compatibility matrix
- TLS termination, rate limits, audit logs, and observability
- Build cancellation, secrets, artifacts, caches, runner capability matching, and log-retention policies

Run `make test` to compile the TypeScript UI and execute the complete Go test suite.
