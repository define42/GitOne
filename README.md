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
export LDAP_CANONICAL_ATTRIBUTE='mail'
export GITONE_SESSION_HASH_KEY='<base64-encoded 64-byte key>'
export GITONE_SESSION_BLOCK_KEY='<base64-encoded 32-byte key>'
make run RUN_ARGS="-root ./data -listen :8080 -public-url http://localhost:8080"
```

The included development directory uses a self-signed certificate, so its compose configuration sets `LDAP_SKIP_TLS_VERIFY=true`. Configure `LDAP_CA_FILE` instead for a trusted deployment. Users must log in with a complete address in `LDAP_USER_DOMAIN`; short names and other domains are rejected. After binding and finding exactly one entry with `LDAP_USER_FILTER`, GitOne uses the entry's single `LDAP_CANONICAL_ATTRIBUTE` value as the authorization and audit principal. The canonical attribute defaults to `mail`; canonical mail values are normalized to lowercase and must use `LDAP_USER_DOMAIN`. Set `LDAP_STARTTLS=true` when using StartTLS over an `ldap://` URL. `LDAP_CONNECTION_TIMEOUT` defaults to `5s`.

The session keys encrypt and authenticate browser cookies and can be generated with `openssl rand -base64 64` and `openssl rand -base64 32`. When they are omitted, GitOne generates ephemeral keys and existing browser sessions end on restart. Sessions last 12 hours by default; configure `GITONE_SESSION_MAX_AGE` with a Go duration such as `8h`. Cookie `Secure` mode follows an HTTPS public URL and can be overridden with `GITONE_SESSION_SECURE`.

Remote HTTP(S) repository imports block loopback, private, link-local, metadata, shared, multicast, documentation, and reserved address ranges by default. DNS is checked again when connecting, the validated numeric address is dialed directly, and every redirect is subject to the same policy. Administrators can set `GITONE_IMPORT_ALLOWLIST` or `-import-allowlist` to a comma-separated list of exact hostnames, IP addresses, or CIDR prefixes, such as `git.internal.example,10.20.0.0/16`. Allowlist entries explicitly permit otherwise blocked destinations.

The development web server and LDAP directory can be started with:

```bash
docker compose up --build
```

The KVM runner is an opt-in Compose profile because it needs a prepared
libvirt host. Its ephemeral in-memory SSH identity and pinned Flatcar base
image are provisioned automatically. See Repository builds below; `make docker`
enables the profile.

GitOne currently supports exactly one web-server process for each storage root.
Mutation coordination is in memory and is scoped to the affected group,
repository, LFS quota, queue, or build job so unrelated work can proceed in
parallel. Do not run multiple GitOne web instances against the same root or
modify that root from another process. Remote `gitone-runner` workers remain
supported because they mutate server state only through the web API.

## Web UI

Open [http://localhost:8080](http://localhost:8080) and sign in with a full-domain LDAP identity such as `alice@example.com`. After LDAP validation, GitOne stores only the canonical directory identity in a Gorilla securecookie that is signed, encrypted, `HttpOnly`, and `SameSite=Strict`; the password is not retained. Every authenticated LDAP user can create a top-level group and becomes its owner. Creating a subgroup requires maintainer access inherited from its parent; the subgroup starts without direct members, so its creator and all other users keep the roles they inherit from parent groups. The GitOne-branded TypeScript UI uses the Huma API to list and create groups, subgroups, and repositories. Dark is the default color theme; the header selector persists Light, Dark, Steampunk, Windows, Mac OS X, Ubuntu, Solaris, GitHub, and GitLab palettes in the browser.

The main page lists only top-level groups and their descriptions. Select a group to see its immediate subgroups and repositories. Group maintainers can create repositories, mirror every Git ref and tag plus every reachable Git LFS object from an HTTP(S) remote, or upload a ZIP/TAR archive containing a bare Git repository. Archive uploads contain Git data only. Group maintainers can open Settings to change the group name and description, inheritance, and non-owner group tokens. Only group owners can change members and roles, repository visibility, the LFS policy, or owner tokens. Every save creates a commit in `control.git`, and renaming a group updates descendant control documents. Repository pages let maintainers rename the repository, provide a copyable `git clone` command containing the authenticated identity, such as `git clone http://alice%40example.com@localhost:8080/engineering/api.git`, and download the selected branch, tag, or commit as ZIP or tar.gz.

The repository viewer follows each repository's symbolic `HEAD`, can browse files with server-side Chroma syntax highlighting, show line-by-line blame attribution, page through the complete selected branch history, expand any commit to inspect its file statistics and unified diff, create a branch from any existing branch, and compare two branches. Maintainers can select an existing branch as the repository default. Its Builds tab shows manual, queued, running, successful, failed, and canceled jobs, polls active jobs automatically, and exposes expandable live logs. Developers can start manual jobs, cancel queued or running jobs, and rerun terminal jobs at the original commit. Users with developer access can create, edit, rename, and delete UTF-8 files up to 1 MiB directly on a named branch and review edited contents as a unified diff; each operation creates one commit and rejects the update if the branch changed after the editor was opened. GitOne fast-forwards linear histories and creates a two-parent merge commit for clean divergent histories; conflicting branches are never moved. Repositories can be deleted from the group danger zone only after entering the exact repository name. Groups can be deleted after all repositories and subgroups have been removed and the exact full group path is entered.

Comparisons can be saved as merge requests with durable Markdown descriptions and threaded, resolvable discussions. Approvals are bound to the exact source commit, so a new push requires a new approval. Authors cannot approve their own changes unless they are a group maintainer or owner. An approval merges automatically when the request is conflict-free and all discussions are resolved; an explicit retry action is available after clearing a previous blocker.

Build the UI separately with:

```bash
make ui
```

## Repository builds

GitOne builds are split across two applications and container images. The
`gitone` web server owns repositories, the durable build queue, logs, and the
runner API. The separate `gitone-runner` controller owns a libvirt warm pool.
It reserves an already-running, SSH-and-Docker-ready KVM before claiming a job,
downloads the exact-commit source archive, transfers it into that VM over SSH,
and runs the build container against the VM's Docker daemon. Output is streamed
back to GitOne. The disposable qcow2 overlay, domain, and Ignition file are
deleted after one assigned job, while a replacement VM is started immediately.
The libvirt host's Docker daemon and filesystem are never mounted into a build.
Branches created, edited, or merged through the API trigger builds too.
Rerunning a terminal build creates a new queued job for the same branch, commit,
and commit-pinned build configuration while preserving the original job and log.
Canceling a running job marks it terminal immediately; its remote executor stops
when the next lease heartbeat observes the cancellation and cannot overwrite the
canceled result.

```yaml
description: Backend API
build:
  image: golang:1.25
  script:
    - go test ./...
    - go build ./...
  manual: true
  branches:
    - main
    - release/*
  environment:
    CGO_ENABLED: "0"
  timeoutSeconds: 1200
```

`image` and at least one non-empty `script` command are required. Commands run in order through `/bin/sh -ec` with the repository at `/workspace`. Before each command runs, GitOne writes it to the build log with a `$ ` command marker; successful commands such as `go build` may otherwise produce no output. Set `manual: true` to record each matching build in the `manual` state without making it claimable by a runner; a developer must start it from the Builds tab or API. `manual` defaults to `false`. `branches` contains path-style glob patterns and defaults to every branch. `timeoutSeconds` defaults to 900 and is capped at 3600. Repository variables cannot replace reserved `CI_*` or `GITONE_*` variables; GitOne provides `CI_COMMIT_SHA`, `CI_COMMIT_BRANCH`, `CI_PROJECT_PATH`, `GITONE_BUILD_ID`, and equivalent GitOne commit variables.

The remote runner API is disabled until `GITONE_RUNNER_TOKEN` is configured on the GitOne server:

```bash
export GITONE_RUNNER_TOKEN="$(openssl rand -hex 32)"
make run RUN_ARGS="-root ./data"
```

### Libvirt/KVM runner host

The runner currently requires x86-64 Linux, libvirt/QEMU 8.3 or newer with
working hardware virtualization, and a writable libvirt directory storage
pool. KVM is mandatory;
the runner deliberately has no silent TCG fallback. On a host that is itself a
VM, nested virtualization must be enabled. A typical host preparation is:

```bash
sudo apt-get install -y libvirt-daemon-system libvirt-clients qemu-kvm
test -c /dev/kvm
sudo virsh pool-info default
```

At startup the runner checks
`flatcar_production_qemu_image.img` in the configured pool. If it is absent,
the runner downloads the version-pinned Flatcar 4593.2.4 QEMU image over HTTPS,
streams it into a private temporary file, verifies the pinned SHA-512 from the
[official digest file](https://stable.release.flatcar-linux.net/amd64-usr/4593.2.4/flatcar_production_qemu_image.img.DIGESTS),
and publishes it atomically before refreshing libvirt. Concurrent runner
starts share a filesystem lock, so only one download is performed. An existing
image is verified on every startup; a mismatch fails safely and is never
overwritten. An existing image must be owned by root or the runner user and
must not be group- or world-writable. This path is implemented entirely in Go
and does not need `curl`, `gpg`, or `ssh-keygen`.

Downloads are staged under a runner-owned `0700` subdirectory that libvirt does
not scan as a volume. Crash leftovers for the selected base name are removed
under the same lock before retrying. The pool path and all of its ancestors
must be real directories owned by root or the runner user; the pool itself must
not be group- or world-writable. This trusted namespace is required for atomic
no-replace publication of a verified image.

The image is pinned rather than resolved through Flatcar's moving `current`
alias. For an intentional upgrade, set both `-libvirt-base-image-url` and
`-libvirt-base-image-sha512`; use a new `-libvirt-base-volume` name while old
VM overlays may still exist. Guest Ignition disables automatic Flatcar updates
so they cannot reboot a disposable VM during a long build. Never replace a
backing image in place while overlays still reference it.

QEMU reads each generated Ignition file directly through `fw_cfg`. On an
SELinux or AppArmor host, permit QEMU read access to `*.ign` beneath the pool
path before starting the runner. For SELinux, label the pool files
`virt_content_t`; for AppArmor, add the pool's Ignition path to the
`libvirt-qemu` abstraction and reload the profile. The exact commands and
policy examples are in Flatcar's
[libvirt guide](https://www.flatcar.org/docs/latest/deploy/virt-options/libvirt/).
The provided Compose service disables its SELinux process label so the trusted
controller can use the existing libvirt socket and storage-pool labels without
relabeling those host resources. Do not add `:Z` to the libvirt socket or pool:
private relabeling can disrupt libvirtd. Disabling the label weakens container
isolation and is appropriate only for this privileged controller on a dedicated
runner host; deployments that prohibit it need a site-specific SELinux policy.

Run the controller as a user allowed to connect to the local `qemu:///system`
socket and write the trusted configured pool path. Each runner process creates
a fresh Ed25519 identity with Go. The private key never leaves process memory;
only its public key is placed in each disposable VM through Ignition. Guest
host keys are pinned per VM in memory and forgotten only after teardown
completes. Libvirt `+ssh` URIs are rejected because they would require an
external SSH transport; expose a local libvirt socket to the controller instead.
The GitOne server and runner use the same API token:

```bash
go build -o gitone-runner ./cmd/gitone-runner
sudo env GITONE_RUNNER_TOKEN="$GITONE_RUNNER_TOKEN" ./gitone-runner \
  -runner-url https://gitone.example \
  -runner-id build-server-1 \
  -runner-workers 4 \
  -runner-work-root /var/lib/gitone-runner \
  -libvirt-uri qemu:///system \
  -libvirt-pool-name default \
  -libvirt-pool-path /var/lib/libvirt/images \
  -libvirt-base-volume flatcar_production_qemu_image.img \
  -libvirt-network-cidr 10.240.0.0/20 \
  -libvirt-idle-count 4 \
  -libvirt-max-instances 8
```

The important libvirt options are:

| Option | Default | Purpose |
| --- | --- | --- |
| `-libvirt-base-image-url` | pinned Flatcar 4593.2.4 image | HTTPS source used only when the base volume is absent. |
| `-libvirt-base-image-sha512` | pinned release digest | Required digest for downloaded and existing base images. |
| `-libvirt-idle-count` | `4` | Number of pre-heated, ready, unassigned VMs. |
| `-libvirt-max-instances` | `8` | Hard cap across creating, idle, and assigned VMs. |
| `-libvirt-vcpus` | `2` | vCPUs per KVM guest. |
| `-libvirt-memory-mib` | `4096` | Guest memory in MiB. |
| `-libvirt-disk-size-gib` | `20` | Disposable overlay virtual size. |
| `-libvirt-network-cidr` | deterministic `/20` | Dedicated guest subnet; set an unused host CIDR explicitly. |
| `-libvirt-ready-timeout` | `10m` | Deadline for DHCP, authenticated SSH, and Docker health. |
| `-libvirt-registry-mirrors` | empty | Comma-separated mirrors written to guest `daemon.json`. |

All creating, ready, and assigned VMs count toward the maximum. Reserving idle
capacity happens before `/jobs/claim`, and the domain, SSH, and Docker health
are rechecked, so a runner that cannot provide an isolated VM does not take a
server lease. Assignment triggers replenishment;
an unassigned reservation is returned without creating another VM. Each
assigned VM is used once, even if source download or execution fails. This is
the equivalent of Fleeting's `capacity_per_instance = 1`, `max_use_count = 1`,
positive `idle_count`, and preemptive mode. Use a registry pull-through cache or
bake common layers into a versioned base image for container-image warmth; do
not replace a backing image in place while overlays use it.

The VM layout and policy are modeled on the public-domain
[Fleeting libvirt plugin](https://gitlab.com/fleetingplugin/fleeting-plugin-libvirt),
adapted to GitOne's lease and log protocol rather than loaded as a GitLab Runner
plugin.

The controller creates a dedicated `gitone-runner` NAT network when necessary.
It uses a `/20` DHCP pool with five-minute leases so one-use VM MAC addresses
cannot exhaust the pool under normal churn. The deterministic default may
overlap Docker, VPN, or site routes; inspect the host's `ip route` output and
set `-libvirt-network-cidr` to an unused, aligned IPv4 `/20` in production. An
existing network must match the requested bridge, NAT, CIDR, DHCP range, and
lease policy exactly. Libvirt bridge-port isolation blocks direct traffic
between concurrent build guests while preserving access to the NAT gateway.
That port setting does not block a guest from initiating connections to the
libvirt host or routed management networks. Production runners must use a
dedicated, hardened virtualization host and a host firewall or libvirt
`nwfilter` policy that allows guest DHCP/DNS and required outbound NAT plus
controller-to-guest SSH, while denying guest-initiated access to host and
management services. Do not co-locate sensitive services on the runner host.
Generated domains, overlays, and Ignition files are scoped to the libvirt URI,
storage pool, and runner ID; ownership metadata and the overlay path are
verified before teardown. Stale resources from a previous controller
generation are reconciled at startup. VM readiness requires both SSH
authentication and `docker info`, not only a DHCP address. SSH authentication,
command execution, streaming, liveness probes, and host-key pinning use the Go
SSH library; no private identity or `known_hosts` file is written. Normal
teardown asks libvirt to remove the one-use QEMU log. The image's configured
non-root user can use the transferred workspace. Build containers receive
neither the libvirt socket nor the host filesystem.

To run the packaged controller container, mount the libvirt socket and pool
path at the same absolute path seen by the host daemon. No SSH-key volume is
needed. `make docker` enables and starts this runner profile. On its first
successful start, expect one roughly 500 MB Flatcar download before the runner
creates its four pre-heated VMs. It can also be started explicitly with:

```bash
GITONE_LIBVIRT_POOL_PATH=/var/lib/libvirt/images \
GITONE_LIBVIRT_NETWORK_CIDR=10.240.0.0/20 \
docker compose --profile libvirt-runner up --build
```

`GITONE_LIBVIRT_IDLE_COUNT`, `GITONE_LIBVIRT_MAX_INSTANCES`,
`GITONE_LIBVIRT_BASE_IMAGE_URL`, and `GITONE_LIBVIRT_BASE_IMAGE_SHA512` expose
the matching Compose overrides.

The Compose service allows three minutes for an orderly stop with the default
30-second per-VM cleanup timeout. Increase `stop_grace_period` when configuring
a materially larger `-libvirt-cleanup-timeout`.

Unit tests use fake hypervisor and SSH transports and need no libvirt daemon.
An opt-in end-to-end smoke test boots real KVM guests and runs a container:

```bash
sudo make test-libvirt
```

Run that target only on a dedicated runner host; normal CI deliberately does
not assume nested KVM, a privileged network, or permission to populate a
libvirt pool. The target disables Go's test-result cache and should run as the
service account that can write the pool and use `qemu:///system` (`sudo` in the
example above). Its SSH identity is generated in memory for that test process.

The runner makes outbound HTTP(S) requests to GitOne and SSH connections to its
private guests; it never mounts GitOne's `/data`. `-runner-workers` controls
concurrent builds and defaults to one. `-runner-command` selects the
Docker-compatible command inside each guest. The old host-Docker executor is
available only as the explicit migration option `-runner-executor docker`.
The web image is built from `Dockerfile`; the controller image is built from
`Dockerfile.runner` and contains `virsh`. Guest SSH is implemented by the
compiled Go runner, so the image does not contain an OpenSSH client.

The GitOne server retains repositories, durable queue state, and logs beside
each bare repository under `<root>/<group>/<repository>.build`; stored logs are
capped at 10 MiB and API log responses at 1 MiB. Leases allow another runner to
reclaim work after a runner failure or network loss.

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
| `POST` | `/api/runner/jobs/heartbeat` | Renew a claimed build lease and report server-side cancellation. |
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
| `GET` | `/api/groups/{path}/settings` | Get editable group settings for a maintainer-authorized group. Token hashes and secrets are omitted. |
| `POST` | `/api/groups/{path}` | Create a group. Any LDAP user may create a top-level group; nested groups require parent maintainer access. Optional query parameter: `description`. |
| `PUT` | `/api/groups/{path}/settings` | Replace group control settings and optionally rename the group through the `name` field. Changing members, visibility, LFS policy, or owner tokens requires owner access. |
| `PATCH` | `/api/groups/{path}` | Rename or move a group. JSON field: `newPath`. A cross-parent move requires maintainer access to the source group and both non-root parent groups. |
| `DELETE` | `/api/groups/{path}` | Delete an empty group. |
| `POST` | `/api/repositories/{path}` | Create a repository. `path` is the URL-encoded full `group/repository` path. Optional query parameters: `description`, and `initializeReadme=true` to create `README.md` on `main`. A description is stored in `.gitone.yaml`. |
| `POST` | `/api/repositories/{path}/import` | Mirror all Git refs and tags plus every reachable Git LFS object from an HTTP or HTTPS remote into a new bare repository. LFS objects are authenticated with the supplied credentials, downloaded through the same network policy as Git, and verified by size and SHA-256 before the repository is published. A missing or corrupt object fails the complete import. Non-public network destinations are blocked unless administratively allowlisted. JSON fields: `url`, optional `username`, and optional `password` or access token. |
| `POST` | `/api/repositories/{path}/import-archive?filename=repository.tar.gz` | Upload a `.zip`, `.tar`, `.tar.gz`, or `.tgz` file as the raw request body. The archive must contain one bare Git repository at its root or in one enclosing folder and may be up to 1 GiB compressed. Git LFS objects are not imported. |
| `PATCH` | `/api/repositories/{path}` | Rename a repository. JSON field: `newName`. |
| `DELETE` | `/api/repositories/{path}` | Delete a repository. |

### Repository builds

Build reads require repository read access. Rerun and cancellation require developer access and an enabled remote runner. Existing build history remains readable while the runner is disabled.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/api/repositories/{repository}/builds` | List builds newest-first with manual, queued, running, succeeded, failed, or canceled status and the viewer's `canManage` capability. |
| `GET` | `/api/repositories/{repository}/builds/{id}` | Get one build and its captured log. |
| `POST` | `/api/repositories/{repository}/builds/{id}/start` | Move a manual build into the runner queue. |
| `POST` | `/api/repositories/{repository}/builds/{id}/rerun` | Create a new queued build for the same branch, commit, and build definition as a terminal build. |
| `POST` | `/api/repositories/{repository}/builds/{id}/cancel` | Cancel a queued or running build. Repeated cancellation is idempotent. |

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
curl -u 'alice@example.com:directory-password' -X POST \
  'http://localhost:8080/api/groups/engineering?description=Engineering%20projects'
```

Create a subgroup as its parent owner:

```bash
curl -u 'alice@example.com:directory-password' -X POST \
  'http://localhost:8080/api/groups/engineering%2Fbackend?description=Backend%20services'
```

Create a repository:

```bash
curl -u 'alice@example.com:directory-password' -X POST \
  'http://localhost:8080/api/repositories/engineering%2Fbackend%2Fapi?initializeReadme=true&description=Backend%20API'
```

Import a bare repository archive:

```bash
curl -u 'alice@example.com:directory-password' -X POST \
  -H 'Content-Type: application/gzip' \
  --data-binary @api.git.tar.gz \
  'http://localhost:8080/api/repositories/engineering%2Fbackend%2Fapi/import-archive?filename=api.git.tar.gz'
```

Uploaded archives are extracted with entry-count and expanded-size limits. Paths that escape the archive root, symbolic or hard links, device files, and other special files are rejected. Imported hooks and object alternates are removed before the repository becomes available.

Clone the repository and enter the LDAP password when Git prompts:

```bash
git clone http://alice%40example.com@localhost:8080/engineering/backend/api.git
```

List top-level groups:

```bash
curl -u 'alice@example.com:directory-password' http://localhost:8080/api/groups
```

Read a nested group:

```bash
curl -u 'alice@example.com:directory-password' \
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
    "alice@example.com": "owner"
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

Member entries contain canonical LDAP identities and roles. GitOne requires the submitted login to include `LDAP_USER_DOMAIN`, binds and searches with its normalized full-domain form, then reads the unique `LDAP_CANONICAL_ATTRIBUTE` value from the matched entry. Only that directory-supplied value is used for member authorization, sessions, merge-request identity, and audit authorship; member passwords are never stored in `control.json`.

Group tokens are available for automation and use salted Argon2id hashes. GitOne generates every token secret from cryptographically secure randomness as exactly 32 URL-safe characters, returns it only in the successful create or regeneration response, and never accepts a user-chosen secret. Set `regenerate` to `true` on an existing token to rotate it. Direct `control.git` pushes may preserve or delete existing token hashes, but cannot add or replace one; token creation and rotation must use the settings API. A token's `key` is its HTTP Basic username, while `name` is only its display label. Its role applies to the whole group and follows the same `inherit` boundary as member access for subgroups.

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
| Start, rerun, and cancel repository builds | — | ✓ | ✓ | ✓ |
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
