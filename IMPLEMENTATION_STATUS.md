# Implementation status

Implemented:

- Pure-Go HTTP server structure
- Nested group/repository path parsing and traversal protection
- Mandatory `control.git` creation for every group
- Initial `control.json` commit on `main`
- Main-only receive-pack ref validation for `control.git`
- Git Smart HTTP discovery, upload-pack, and receive-pack handlers using go-git
- Per-repository sibling LFS storage (`repo.git` next to `repo.lfs`)
- LFS Batch API and basic PUT/GET/HEAD/verify endpoints
- Group create/delete/rename endpoints
- Repository create/delete/rename endpoints
- Huma administration API with generated OpenAPI and interactive documentation
- Embedded TypeScript UI for browsing and creating groups and repositories
- Git-backed token and role loading with inheritance
- Bootstrap credentials through environment variables
- Go and TypeScript build validation plus a multi-stage Dockerfile

Not production-complete yet:

- Validate proposed `control.json` before accepting a control push
- Fast-forward-only ancestry enforcement for control `main`
- Restrict the control tree to only `control.json`
- Full Argon2id encoded-hash parsing (the initial bootstrap uses `sha256:`)
- Repository policy listing enforcement from `control.json`
- LFS quota enforcement and concurrent upload reservations
- LFS pointer validation during Git receive-pack
- Cross-process locking and atomic multi-path rename/delete transactions
- Complete Git compatibility matrix and protocol-v2 testing
- TLS termination, rate limits, audit logs, and observability

Run `make test` to compile the TypeScript UI and execute the complete Go test suite.
