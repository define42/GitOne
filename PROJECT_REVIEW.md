# GitOne — Full Project Review

Date: 2026-08-01
Scope: complete codebase (~61k lines of Go across 20 internal packages, 6.4k-line TypeScript UI, CI/build system, deployment assets, docs). Method: parallel subsystem reviews (HTTP API/server core, auth/security, Git/storage/LFS, merge requests, CI/runner, web UI, operations/testing) plus three product-lens analyses (competitive, enterprise readiness, developer experience) and an adversarial completeness pass. File/line references were spot-verified against the current tree.

---

## Executive summary

GitOne is an unusually well-engineered young forge with a coherent, defensible niche: a **FIPS 140-3-enforced, SHA-256-only, pure-Go single-binary Git server with VM-isolated CI** aimed at regulated and air-gapped environments. The core write paths (quarantined receive-pack, staged-validate-publish imports, two-phase merge claims, commit-pinned approvals) show real correctness discipline, and the test culture is exceptional (36k test lines vs 25.5k code lines, 85% coverage gate, FIPS run in two modes, native-git e2e tests).

The gaps cluster in three places, in order of urgency:

1. **Day-2 operations** — no graceful shutdown, no audit/access logging, no metrics, no backup/restore story, no GC for normal push workloads, no migration tooling. These are what an operator hits in the first week and are mostly cheap relative to what is already built.
2. **Enforcement gaps that undermine the product's own story** — no protected branches (the entire approval system is bypassable by `git push origin main` or the direct-merge endpoint), no per-user credentials (developers embed their LDAP directory password in git credential stores), no session revocation.
3. **Missing table-stakes forge features** — tags/releases surface, webhooks, notifications, search, line-anchored review comments, CI status on merge requests.

The highest-leverage observation: GitOne already contains the hard building blocks for most of what is missing (a FIPS SSH stack, PBKDF2 token machinery, SSRF-hardened outbound HTTP, a durable job queue, git-backed config). Most top-value features are integration work, not new infrastructure.

---

## What is already strong

- **FIPS 140-3 enforcement is structural**, not a checkbox: startup fails without the CMVP-validated module, CI tests under `GODEBUG=fips140=only`, and even the runner's SSH transport is algorithm-restricted. No mainstream self-hosted forge ships this.
- **SHA-256-only object format with a verified one-way SHA-1 conversion** on import — essentially unique in the market.
- **Receive-pack quarantine** (`internal/githttp/quarantine.go`): incoming packs, LFS pointers, and control.json are validated in isolation before atomic publication.
- **Interrupted-merge recovery is production-grade**: persisted two-phase claims with crash reconciliation and an explicit ambiguous-outcome state (`internal/httpapi/reviews.go`).
- **Exact-commit approvals** that go stale on push — a change-control semantic regulated shops normally have to configure carefully elsewhere; here it is the default.
- **Git-backed authorization** (`control.git`, fast-forward-only, validated commits) gives every permission change a tamper-evident, diffable history.
- **CI isolation is best-in-class for self-hosted**: one-use KVM guests, per-VM in-memory Ed25519 identities, pinned SHA-512-verified base image, no host filesystem or docker-socket exposure; pre-claim capacity reservation means jobs are never claimed without a healthy VM.
- **SSRF-hardened imports** (private-range blocking, DNS-pinned dialing, redirect re-validation, allowlists) — an area where competitors have had repeated CVEs.
- **Defensive stores everywhere**: atomic temp-file+fsync+rename writes, offset-checked build logs, `DisallowUnknownFields`, size caps, deadlock-free ordered lock acquisition.
- **httpio.Protect** sliding-deadline I/O guard — allows long transfers while killing stalled connections.
- Disciplined UI: strict TypeScript, typed DOM construction, only two `innerHTML` sinks (both DOMPurify-sanitized), good ARIA baseline, no CDN dependencies.

---

## Part 1 — Top priorities

Ranked by (severity × user/operator value) ÷ effort.

| # | Finding | Kind | Effort |
|---|---------|------|--------|
| 1 | **Graceful shutdown**: `cmd/gitone/main.go:31` is `log.Fatal(server.ListenAndServe())` — no signal handling, no `Server.Shutdown`. Every deploy/restart hard-kills in-flight pushes, LFS uploads, merges, and imports. The runner already does this correctly; the web server should too. | reliability | small |
| 2 | **Protected branches / push rules**: receive-pack enforces fast-forward-only solely for `control.git` (`internal/githttp/handler.go`); any developer can force-push or delete the default branch, or push/merge directly around the whole MR approval system. Add branch-protection rules to `control.json` (no force-push/delete on default branch, require-MR, minimum approvals), enforced in receive-pack and the merge/branch-delete APIs. Without this, GitOne's compliance story is advisory. | security/product | medium |
| 3 | **Per-user personal access tokens**: the only per-user credential is the raw LDAP directory password sent as Basic auth on every Git/LFS request. Group tokens exist but are shared, group-scoped secrets — not personal, and unusable for audit attribution. The PBKDF2 machinery already exists; add a user-owned token store checked before the LDAP fallback in `internal/auth/auth.go`. This is the single largest adoption blocker for the target market. | security/product | medium |
| 4 | **Audit logging**: ~20 unstructured `log.Printf` sites, none recording principal/action/outcome. No login success/failure, push (the pusher's identity is discarded in receive-pack), permission denial, token use, or deletion event is recorded anywhere. For a FIPS-targeted product this is a hard blocker. Adopt `log/slog` JSON, add request-ID middleware, and emit structured security events from the auth resolver, receive-pack, LFS handler, and every mutating API. | security/ops | medium |
| 5 | **Observability**: `/healthz` returns a constant; no readiness probe, no `/metrics`, no pprof, no access log. Add Prometheus counters (auth failures, push/fetch ops, queue depth, KDF limiter saturation, LFS bytes), and a readiness variant that checks storage-root writability and LDAP reachability. | ops | medium |
| 6 | **Git GC never runs for normal workloads**: repack/prune fires only on force-push/delete/tag pushes and skips repos over 1 GiB (`internal/githttp/maintenance.go:30`) — exactly the repos that need it most. Every fast-forward push adds a pack forever. Needs a periodic/threshold maintenance loop decoupled from push semantics. Related: clone/fetch takes no lock while maintenance deletes packs (`handler.go:139-209` vs `368-372`) — a live race against concurrent clones. | reliability | medium |
| 7 | **CI status on merge requests + merge gating**: builds and MRs are both fully built but never joined — an approval auto-merges regardless of failing tests, and `canMerge` (`reviews.go:1417`) checks only approvals/threads/conflicts. Surfacing per-job status for the head commit and an optional required-green gate is wiring, not infrastructure, and is the highest-leverage CI integration available. | product | medium |
| 8 | **Backup/restore/DR story**: state spans `repo.git` + `.lfs` + `.build` + `.reviews` + `control.git` + `acme/`, mutated under in-memory locks; a live rsync can capture torn state. No backup command, no quiesce mode, no restore-verify (fsck exists but is only invoked during imports), no docs. Minimum: a `gitone backup`/`gitone fsck` subcommand and a documented snapshot procedure. Also: add a `flock` on the storage root at startup — today nothing prevents accidentally starting two instances against the same root (a stated corruption hazard with no enforcement). | ops | medium |
| 9 | **Trusted-proxy support for rate limiting**: the attempt limiter keys on the direct socket peer and ignores forwarded headers by design (`internal/auth/limit.go:272`), while the README tells operators to deploy external TLS termination. Behind any proxy/NAT, all users share one bucket (4 concurrent binds) — one attacker's failures back off the whole organization. A `GITONE_TRUSTED_PROXIES` CIDR setting is small and high-value. | security | small |
| 10 | **Tags API and UI**: tags are pushable, resolvable, imported, and archivable — but invisible: no list/create/delete endpoint (no `Tags()` call anywhere in `internal/httpapi`) and no UI. A read-only list mirroring the branches endpoint is small; releases (notes + artifacts) build on it. | product | small |

---

## Part 2 — Detailed findings by theme

Tags: **[priority / effort]**

### 2.1 Security and authentication

- **[high/medium] No session revocation.** Sessions are stateless securecookies; `DELETE /api/session` only clears the client cookie. A stolen cookie or a user deprovisioned in LDAP retains access until expiry (12h default, `GITONE_SESSION_MAX_AGE` configurable). Add a session ID plus a small revocation store (or per-user not-before timestamp), and support secondary decode keys so session-key rotation doesn't force a global logout (gorilla/securecookie already supports multi-codec rotation; GitOne doesn't use it).
- **[medium/small] CSRF rests entirely on `SameSite=Strict`.** No Origin/Sec-Fetch-Site validation on cookie-authenticated mutations, no `__Host-` cookie prefix, and unauthenticated `POST /api/session` allows login CSRF. A small origin-check middleware closes this.
- **[medium/small] No security headers anywhere.** `internal/webui/webui.go` sets only Cache-Control/Content-Type: no CSP, no `frame-ancestors`, no `X-Content-Type-Options` (except archive downloads), no HSTS on the ACME path. The UI has no external resources, so a strict CSP (`default-src 'self'; img-src 'self' data:`) is nearly free and converts any future sanitizer bypass into a non-event. DOMPurify runs with defaults, so read-level users can currently embed external image beacons in Markdown that leak viewer IPs.
- **[high/small] No static-certificate TLS mode.** `GITONE_TLS_MODE` supports exactly `unencrypted` and `acme` (`cmd/gitone/tls.go`). Internal-PKI shops — the core FIPS audience — cannot terminate TLS in GitOne at all, and fronting with a non-FIPS proxy undermines the headline FIPS claim. A `static` mode with cert/key files and reload, plus optional client-CA (mTLS/CAC — see roadmap), completes the FIPS story end-to-end.
- **[medium/medium] Group token design needs a ceiling and attribution.** Tokens can carry the `owner` role, so a leaked CI token can rewrite `control.json`, flip visibility to public, and mint more tokens. Verification honors the iteration count embedded in the stored hash (100k–2M), so anyone who can write `control.json` can plant 2M-iteration hashes that amplify the KDF-DoS surface ~20×. There is no last-used tracking. Consider: a maximum token role (developer), server-enforced iteration bounds on verify, and last-used timestamps.
- **[medium/medium] Runner trust boundary.** One static all-repo bearer token shared by every runner, rotatable only by restarting everything, passable via CLI flag (visible in process listings), transported over plain HTTP by default in compose. Runner identity is a self-asserted string; two hosts with the default `-runner-id` silently steal each other's leases. Add per-runner registration/credentials (or at least multiple named tokens + duplicate-ID rejection) and a TLS-required guard.
- **[medium/medium] Identity lifecycle is keyed to a mutable email.** Members, MR authors, approvals, and comments all key on the LDAP `mail` value. An email change silently severs all memberships; an email reassigned to a new hire silently inherits the old person's roles everywhere. Members are never validated against the directory when added, and there is no offboarding sweep. At minimum document this; better, key on a stable directory attribute (entryUUID/objectGUID) with mail as display.
- **[medium/small] LDAP deployment hazards.** Plain `ldap://` without StartTLS sends passwords cleartext with no warning or require-TLS mode; only a single LDAP URL is supported (no failover), so a directory outage takes down all auth including Git/LFS traffic; direct-bind-only excludes search-then-bind schemas. The shipped compose sets `LDAP_SKIP_TLS_VERIFY=true` with `ldaps://ldap:389` (the plaintext port) — confusing at best.
- **[medium/medium] Anonymous surface is unthrottled.** `public` visibility exposes clone, LFS download, browse, archives, and MR listing unauthenticated — including the most expensive endpoints (blame, divergence walks, MR list with merge simulation under an exclusive lock). The attempt limiter guards only credentialed auth; there is no anonymous rate limiting. Also inconsistent: builds require auth on public repos while code and MRs do not.

### 2.2 Reliability and operations

- **[high/medium] No fsync discipline on the Git write path.** `quarantine.Promote` publishes packs via bare `os.Rename` without file/dir fsync; loose refs/objects and control.git commits are likewise unsynced. After power loss, refs can point at missing objects with no fsck to detect it. Fsync-on-publish at promote/ref boundaries is cheap relative to push cost.
- **[medium/small] Crash leftovers are never swept.** LFS staging (`.gitone/uploads`), import staging, receive quarantine dirs, and temp dirs are removed only by in-process defers; a crash during a large upload leaks the full staged size forever. A startup mtime-aged sweep is small and safe under the single-process constraint.
- **[medium/small] `.trash` grows unbounded and has no restore path.** Deletes move data to `<root>/.trash/<timestamp>/` but nothing purges it, no API restores it (undelete requires undocumented filesystem surgery), and trashed LFS stops counting against quota while still consuming disk. Needs retention, a restore/list/purge surface, and docs.
- **[high/medium] Upgrades are manual per-group surgery.** `control.json` version bumps require hand-editing every group's document via owner git pushes while the server refuses to serve the group (README documents this as the upgrade procedure); the FIPS migration additionally invalidates all Argon2id tokens with no inventory of which ones. A `gitone migrate` subcommand that rewrites all control documents and reports tokens needing rotation would make upgrades safe and repeatable.
- **[medium/small] Containers run as root with no HEALTHCHECK**; compose bind-mounts host `/data` by default and falls back to a publicly-known runner token (`gitone-development-runner-token`) — a foot-gun for anyone copying compose toward production. Web container should get a non-root UID, HEALTHCHECK, and read-only-rootfs guidance.
- **[high/small] CI never runs the race detector** despite the architecture leaning entirely on in-memory synchronization — and the git log shows this bug class is live (`5ced316` lost-update race). Add a `go test -race ./...` job (and `-shuffle=on`).
- **[medium/medium] Release engineering gaps.** Images push to GHCR on every main push but there are no GitHub Releases, no CHANGELOG, no SBOM/signing/provenance, amd64-only images (the server has no x86 requirement — arm64 is free), and `:latest` silently moves. Dependabot covers gomod/actions but not npm (`web/package-lock.json`) or Docker base images.
- **[medium/medium] Docs contradict each other.** `IMPLEMENTATION_STATUS.md:21` still claims "Argon2id automation tokens with repository scopes" (the code is PBKDF2 and scopes were removed at schema v2); README's boundaries section lists LFS-pointer push validation as future work while it is implemented and tested. No SECURITY.md (notable for a FIPS product), CONTRIBUTING, architecture doc, or ops guide; the 690-line README carries everything.
- **[low/small] Dead code:** `internal/config` (JSON config loader) is imported nowhere; the in-process `runner.Runner` (~600 lines, divergent behavior from the remote path) is constructed only in tests. Either wire them up or delete them. No `-version` flag exists.

### 2.3 Scalability and performance

- **[high/medium] CI claim path is O(all repos × all jobs ever) under a global exclusive lock.** `Coordinator.Claim` walks every group/repo and JSON-decodes every historical job file, polled every 2s per runner worker; job history is never pruned. Needs a queued-job index and retention policy before a few hundred repos.
- **[high/medium] 10,000-commit cliff breaks MRs and compare.** `commitDifference` enumerates full reachable sets and hard-fails past 10k commits — MR creation, MR view, and compare all 500 on any real imported repo. Compute ahead/behind via bounded merge-base walk or degrade to "unknown" instead of failing.
- **[high/medium] MR list does full merge simulation per MR per request, under an exclusive lock** — and `Store.List/Get` take the per-repo review lock exclusively even for reads. Cache mergeability keyed by (head, target), skip simulation in list views, use shared locks for reads.
- **[medium/medium] Auth amplification.** Basic-auth traffic pays a fresh LDAP bind (or 100k-iteration PBKDF2 through a 4-slot fail-fast limiter that 429s when full) per request; one group page can trigger ~21 binds via per-subgroup authorization. (Session-cookie browser traffic is fine — it reads cached control docs.) A short-TTL credential-hash→identity cache removes almost all of this; modest CI parallelism on group tokens will hit instant 429s today.
- **[medium/medium] LFS quota accounting is a full filesystem walk per upload**, taken under the exclusive group LFS lock; pushes similarly walk the objects dir twice for the Git quota. Persist per-group usage counters, rebuilt on startup.
- **[medium/medium] Unbounded lists.** Pagination exists only for commits; groups, branches, builds (entire job history), and MRs return complete arrays that the UI re-fetches on poll. Adopt the commits envelope everywhere before clients depend on full arrays.
- **[medium/small] Build log mismatch:** appends store 10 MiB but reads cap at 1 MiB — bytes 1–10 MiB are written yet unreachable via the API; the UI re-downloads the full log every 3s. Align the caps and add an offset-based tail parameter (the store already tracks exact offsets); SSE later.
- **[low/medium] Blame and divergence walks are uncached and unbounded** (blame by file size only, not history depth) and reachable anonymously on public repos.

### 2.4 Correctness and cross-cutting risks

- **[medium/small] Remote imports bypass the group LFS policy.** `internal/storage/import_lfs.go` enforces only a hard-coded 100 GiB cap and never consults `control.LFSPolicy` — an import can land LFS content into a group with LFS disabled and blow past `MaximumStorageBytes`. The only existing quota is bypassable via this path.
- **[medium/small] Storage-suffix namespace collisions.** `repopath` reserves only `control`; a group named `app.build` or `app.reviews` can be created next to repo `app`, after which lazily-created build/review state collides with the group directory. Names ending in `.git` shadow the URL namespace. Reserve the sidecar suffixes in name validation.
- **[medium/medium] Rename/delete orphans running builds.** Runner heartbeat/log/complete address jobs by repository path; a rename mid-build 409s every subsequent call, the VM burns its full timeout, and the moved job file is stranded in `running` (no retry cap or dead-runner aging exists either — poison jobs are reclaimed forever, and with no runner connected, queued jobs wait forever with no age-out or UI hint).
- **[medium/small] Multi-ref pushes are non-atomic with no `atomic` capability**, and a succeeding ref triggers builds even when a sibling ref fails (documented). Advertising and honoring `atomic` is a contained change.
- **[medium/medium] go-git v6.0.0-alpha.5 is load-bearing.** The Git core rides a pre-release with local workarounds for alpha defects; server-side pack generation has no bitmaps or pack reuse (every clone re-encodes history). Pin, vendor, and build the client compatibility matrix the status file already promises.
- **[medium/small] Merge commits are authored as `name <name@localhost>`** rather than the canonical LDAP identity — pollutes history for mirroring and downstream tooling.
- **[medium/small] Ambiguous interrupted-merge state has no operator escape hatch** except closing the MR (which silently discards the claim). Add an audited "abandon merge claim" action.
- **[low/small] Hard caps without overrides:** receive-pack rejects pushes over 1 GiB (while allowing 20 GiB repos — initial migration of a large repo by push is impossible), and CI jobs cap at 3600s with no operator override.

### 2.5 CI/build system product gaps

- **[high/medium] Only the libvirt/KVM executor exists.** Requires `/dev/kvm`, libvirt, and a Flatcar pipeline — impossible on most cloud VMs, containers, or dev machines. The `Executor` interface is tiny; an opt-in plain-Docker executor would dramatically widen who can run this at all.
- **[high/medium] Build source silently omits LFS content and submodules.** `WriteSourceArchive` streams raw tree blobs, so LFS files arrive as pointer text — on a server whose headline feature is LFS, builds of exactly those repos break confusingly. Submodules are silently dropped; there is no `.git` dir (so `git describe` fails) and no per-build scoped token for fetching from the server.
- **[high/large] Jobs cannot build images or exchange outputs.** No docker socket/DinD in the guest (despite the disposable VM making that comparatively safe) and nothing persists between jobs, so `needs` is ordering-only. Artifacts + DinD are what make multi-job pipelines worth defining.
- **[medium/large] Trigger surface is branch pushes only.** No tag pipelines (release builds are impossible), no schedules, no MR-head pipelines, no run-pipeline button, no badges, no completion webhooks.
- **[medium/medium] No runner/queue observability** — the server cannot say whether any runner is connected; "why is my build queued forever" is undiagnosable. Record last-poll per runner and expose an admin runners/queue view.
- **[medium/large] Every build pays full image pull cost** (VM destroyed per job, empty Docker cache each time; idle VMs could pre-pull popular images). Runner restart also preheats the entire pool synchronously before claiming anything.

### 2.6 Web UI

- **[high/medium] Every navigation is a full page reload against uncacheable assets.** Nearly all links are plain anchors; `webui.go` sets `Cache-Control: no-cache` and embedded files have zero modtime so no ETag/Last-Modified is possible — ~1 MB re-downloaded per click (the hand-bumped `?v=18` query strings show cache-busting intent the no-cache policy defeats). Hashed filenames + immutable caching, or honest ETags, plus client-side routing.
- **[high/large] 6,434-line single file, zero frontend tests.** No test framework exists in `web/package.json`; the codecov badge measures Go only, and recent regressions (default-branch hardcoding, read-only buttons, tablet layout) are exactly what UI tests catch. `tsc` already emits ES modules — splitting into `api.ts`/`router.ts`/`views/*` plus vitest for pure helpers and a small Playwright smoke suite is incremental.
- **[medium/small] Binary files are a dead end** ("Content is available through the API") though the blob API already returns base64 — image preview via data: URI is nearly free. No per-file raw/download button exists at all.
- **[medium/medium] Markdown relative links and images are broken** (no base-URL rewriting, no raw endpoint) — most imported READMEs render visibly broken on the repo front door.
- **[medium/medium] No tags view, no browse-at-commit link, no line anchors/permalinks** (`#L42-L60` is small — the route already encodes repo/ref/path, and full-hash refs make permalinks immutable).
- **[medium/large] No search of any kind** — not even a client-side name filter over already-fetched group/repo lists (a large win before server-side code search).
- **[medium/large] No dashboard/activity feed** — the root page is a group list; "MRs awaiting my action" is the single highest-value dashboard element and can initially be built by fanning out the existing per-repo MR API.
- **[medium/small] Committed `dist/` artifacts can drift** from `web/src` — CI checks drift for app.js, but vendored marked/DOMPurify/diff copies update only when someone rebuilds and re-commits, so DOMPurify security fixes can silently lag (dependabot doesn't watch npm here either).

### 2.7 Merge request workflow

- **[medium/small] Required approvals hard-coded to 1** (`reviews.go:398`) with silent maintainer self-approval — a maintainer-authored MR needs zero independent reviewers. Make it a control.json policy.
- **[medium/small] Approving auto-merges with no opt-out.** A reviewer intending only to signal approval instantly lands the change. Separate approve from merge, or make auto-merge an explicit flag.
- **[medium/medium] MRs are immutable after creation:** no title/description edit, no retarget (close and recreate, losing threads/approvals), no draft state, no reviewers/assignees.
- **[medium/medium] Only implicit merge strategies:** no squash (cheap with the existing tree machinery), no rebase, no ff-only policy, no custom merge message on the MR path.
- **[medium/large] No line-anchored diff comments** — threads carry no file/line/commit anchor; all discussion is MR-global. The single biggest UX gap vs. reviewer expectations; needs anchor fields on `Thread` plus stale-position handling.
- **[medium/large] Conflict resolution is a dead end** — a filename list plus "resolve locally". Surfacing base/target/source versions and offering a reverse-merge update button would turn it into a guided flow.

---

## Part 3 — Feature roadmap: what would add the most product value

Ranked with GitOne's niche in mind (regulated / air-gapped / FIPS). The consistent theme: **the enclave forge is the only tool the team has** — features that are nice-to-have on the open internet (search, wiki, notifications, registry) are disproportionately valuable where SaaS alternatives are unreachable.

### Tier 1 — table stakes that unblock adoption (small→medium effort)

1. **Personal access tokens** — reuse the PBKDF2 token machinery as user-owned credentials; expiry + last-used for compliance review. Unblocks credential-policy compliance, IDE integration, and honest audit attribution.
2. **Protected branches & push rules** — generalize the existing control.git fast-forward enforcement into control.json-declared rules. Turns the approval system from advisory into enforceable change control (NIST 800-53 CM).
3. **Tags & releases** — tags API/UI first (small); then releases as notes + SHA-256-verified artifacts in a `<repo>.releases/` sidecar following the established pattern. In air-gapped orgs the forge releases page *is* the software distribution point.
4. **CI status on MRs + required-green merge gate** — pure wiring between two existing subsystems; the highest-leverage CI integration available.
5. **Webhooks (SSRF-hardened)** — reuse the import path's private-range blocking/DNS-pinning/allowlist verbatim for outbound deliveries with HMAC signatures and a durable retry queue modeled on the build queue. "Webhooks that are safe by default" turns an existing strength into a differentiator.
6. **Email notifications** — identities are already email addresses (LDAP canonical mail), so no address book is needed; MR events and build failures first. Without notifications, review latency is bounded by manual polling.
7. **Reviewer assignment on MRs** — small model change, outsized workflow value; the natural first notification trigger.
8. **Static TLS mode (+ trusted-proxy config)** — completes the FIPS boundary without a fronting proxy; unblocks internal-PKI and strict air-gap shops.

### Tier 2 — niche differentiators (medium effort, high strategic value)

9. **Audit log + SIEM export + access-review reporting** — structured security events plus an "effective access" report (identity × group × role × source, with token expiry) walked from control documents. SOX/ISO 27001/800-53 AC-2 recertification made push-button; control.git history already gives auditors "granted when, by whom" for free.
10. **mTLS client-certificate / smartcard (PIV/CAC) login** — no mainstream forge offers first-class CAC without a proxy; a pure differentiator in the defense market GitOne courts. GitOne already terminates TLS.
11. **Signed-commit verification (SSH signatures)** — verify against per-group allowed-signer registries in control.git, show verified badges, optional require-signed receive-pack policy. SSH signatures use exactly the FIPS-approved algorithms GitOne restricts itself to; "FIPS-verified signed commits" is marketing-grade. (Also: document prominently that SHA-1 import strips signatures — a migration blocker today.)
12. **Air-gap install bundle** — a make target producing transferable images + Flatcar image + checksums, a "no outbound network" startup flag, and an offline deployment doc. The product is nearly there; nobody has written it down.
13. **Push mirroring / bundle export** — the outbound complement of import, reusing its transport/credentials/allowlist; plus offline full-fidelity bundle export (git bundle + LFS manifest) for cross-domain sneakernet transfer. Cross-domain promotion is a defining regulated-shop workflow, and it also mitigates the SHA-256 one-way-door risk (below).
14. **SBOM / SLSA provenance for builds** — the one-use-KVM design is stronger than most commercial CI but produces no evidence; emit signed in-toto provenance per job (commit, config hash, runner ID, VM lifecycle) once artifacts exist. Converts an internal strength into an auditor checkbox (EO 14028/SSDF).
15. **Instance administration** — `GITONE_INSTANCE_ADMINS` with implicit owner, list-all-groups, ownership transfer, instance policies (visibility ceiling, top-level-group creation control). Today any directory user can create unlimited root groups, and no one — including the operator — can enumerate or recover them.

### Tier 3 — bigger bets (large effort; sequence deliberately)

16. **SSH Git transport** — the FIPS-constrained pure-Go SSH stack already exists for the runner; add a listener, per-user key registry (shared with signature verification), and reuse the transport-agnostic pack machinery. Standard workflow everywhere and reduces PAT urgency for humans.
17. **Line-anchored review comments + side-by-side diffs** — the largest review-UX gap; requires the thread-anchor schema plus diff-view work.
18. **Code search** — filename search first (cheap), then a pure-Go trigram/bleve index maintained from the existing post-receive callback, scoped by the existing authorization layer. In an enclave, the forge is the only place code lives — search is worth more here than anywhere.
19. **Repo-backed wiki** — remarkably cheap in this codebase (auto-created auxiliary repo + a Markdown-locked browser tab reusing the existing editor); enclaves have no Confluence.
20. **Lightweight issues** — the review subsystem (durable JSON sidecar, threads, Markdown, state machine) is 80% of an issue tracker; `closes #12` auto-close on merge.
21. **OCI container registry** — the runner docs already presuppose a registry the product doesn't provide; OCI is natively SHA-256-addressed, and the LFS content-addressed store/quota machinery generalizes. Collapses another server the enclave must accredit.
22. **Protocol v2 / partial clone** — track go-git server-side support; v0 hurts exactly the monorepo/LFS-heavy repos enterprises run.
23. **Pre-receive policy engine** — the quarantined receive-pack is an ideal, already-isolated interception point; control.git-declared rules (message format, path ownership, secret scanning, size limits, signatures) would subsume protected branches, push rules, and signing policy under one architectural investment. Consider before building those three separately.
24. **Forking / cross-repo MRs** — a deliberate product decision to make, not necessarily to build: read-role users currently have no contribution path at all. Rejecting forks is defensible for the niche; do it explicitly.

### Sequencing note

Items 1–8 are mutually reinforcing and mostly independent; a reasonable order is 1→2→4→3→6→7→5→8. Item 23 (policy engine) should be evaluated before items 2 and 11 are built separately. Item 16 (SSH) shares its key registry with 11 — design them together.

---

## Part 4 — One-way doors and strategic risks

- **SHA-256-only is a one-way door.** There is no export path back to the SHA-1 world: history can never move to GitHub/GitLab/Gitea, clients older than git 2.29 cannot clone, and JGit/libgit2-based tooling (IDEs, CI checkout actions) largely lacks SHA-256 support. This is the right bet for the niche, but it is currently undocumented as an adoption consideration — write the compatibility matrix (the status file already promises one), state minimum client versions, and treat bundle export (roadmap #13) as the mitigation.
- **go-git v6 alpha is load-bearing** for the entire Git core; upstream churn can change server behavior on any bump. Vendor and pin deliberately; expand the native-client e2e matrix.
- **Single-process design needs a bounded failure story**, not HA: startup flock, documented active/passive recovery with tested RTO, crash-recovery validation of staging dirs at boot. Enterprises accept single-process; they don't accept unrehearsed recovery.

---

## Part 5 — Documentation corrections

1. `IMPLEMENTATION_STATUS.md:21` — "Git-backed Argon2id automation tokens with repository scopes": tokens are PBKDF2-SHA256 (Argon2id is rejected) and repository scopes were removed at schema v2.
2. `README.md` boundaries section — lists "LFS-pointer existence checks during Git pushes" as pre-production work; this is implemented (`internal/githttp/lfs_validation.go`) and stated as implemented elsewhere in the same README.
3. `docker-compose.yml` — `ldaps://ldap:389` pairs the LDAPS scheme with the plaintext port; and the `GITONE_RUNNER_TOKEN` fallback is a publicly-known string.
4. Missing entirely: SECURITY.md (vulnerability disclosure policy — notable for a FIPS-marketed product), CONTRIBUTING.md, architecture overview, deployment/operations guide, backup procedure.

---

*Review method: 11 parallel analysis passes (7 subsystem code reviews, 3 product-lens analyses, 1 adversarial completeness critic) over the full tree, followed by manual verification of load-bearing claims. Findings are cited to files/lines in the current tree; four overclaims caught by the completeness pass were corrected before inclusion (notably: group automation tokens do exist as a non-password credential — the gap is per-user attribution, not absence).*
