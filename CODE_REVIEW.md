# GitOne Code Review — Full Report

_Comprehensive review of correctness, security, concurrency, resource-safety, and
product completeness. Every finding below was independently re-verified against the
source by a second adversarial pass; the verdict (CONFIRMED / PLAUSIBLE) is noted
inline. Findings: **5 High, 10 Medium, 14 Low, 1 Info** (0 refuted)._

## 1. Executive summary

GitOne is a coherent, well-factored self-hosted Git forge with genuinely careful
security work in its core authorization path — but its production readiness is
undermined by a pervasive absence of resource limits and request timeouts, which is
by far the dominant risk. There are no Critical findings, but the five High-severity
issues all cluster around a single structural weakness: GitOne is a single-process
server with almost no backpressure, so any one unbounded operation (an unthrottled
Argon2id/LDAP flood, a giant diff buffered in memory, a slowloris trickle, an
exclusive lock held during a full-tree scan, or an unsandboxed build container) can
degrade or take down every repository on the storage root. The standout strengths
are the disciplined per-request re-authorization model, the deliberate use of
Argon2id with fail-closed identity handling, and an SSRF allowlist on imports — which
makes the gaps read as unfinished hardening rather than design blindness.

## 2. Strengths

- **Authorization is re-evaluated per request** against `control.json` (`internal/auth/auth.go:118`), so role/membership revocation takes effect immediately even though sessions are stateless — a deliberate, correct separation of identity from authorization.
- **Password handling is strong by design**: Argon2id at m=19 MiB, t=2 (`internal/auth/auth.go:19`), fail-closed identity canonicalization, and proper `ldap.EscapeFilter` usage in the live path (`internal/auth/ldap.go:242`).
- **The MR merge path itself is rigorous**: distinct-author approval, resolved-thread checks, and exact-commit mergeability are all enforced at the merge choke point (`internal/httpapi/reviews.go:901`). The gate logic is sound; only its authoritativeness over the branch is incomplete (M6).
- **Import already has an SSRF allowlist** (`internal/storage/import_network.go` blocks loopback/private/link-local/metadata ranges) with DNS-pinned dialing and per-redirect revalidation — the team knows how to do egress policy well.
- **Data-format discipline**: `control.json` is versioned (v1→v4) with strict rejection of unknown versions; review records use `DisallowUnknownFields`.
- **The runner binary already models graceful shutdown correctly** via `signal.NotifyContext` (`cmd/gitone-runner/main.go`) — the pattern the web server should copy.
- **Streaming Git/LFS paths are protected** by sliding read/write deadlines via `httpio.Protect` (`internal/githttp/handler.go:57`); the gap is that this was not extended to the JSON API and UI.

## 3. Findings by severity

### High (5)

**H1 — Unthrottled authentication enables Argon2id/LDAP resource exhaustion and brute force** · `internal/httpapi/session.go:76` · CONFIRMED
No login, REST Basic-auth (`internal/httpapi/api.go:1095`), or git/LFS authorizer (`internal/server/server.go:44`) path is rate-limited, backed off, or locked out. Every unauthenticated request drives an LDAP bind, and guessing a valid group plus a human-readable token key (e.g. `ci`, `deploy`) forces a full ~19 MiB Argon2id computation per request. ~100–200 concurrent `Basic ci:x` requests transiently consume 2–4 GB and saturate cores; failed-login spraying exhausts LDAP connections and can lock out real users on directories with lockout policies.
**Fix:** per-IP and per-username rate limiting with backoff before any bind/Argon2id call; a global semaphore bounding concurrent Argon2id computations; return 429 when exceeded; cache short-lived negative-auth results.

**H2 — Commit-diff and branch-compare responses are unbounded and fully buffered in memory** · `internal/httpapi/merge.go:547` · CONFIRMED
`compareTrees` builds one entry per changed file with no file-count cap; each patch is individually clamped to 1 MiB but the aggregate is `#files × 1 MiB`, and the whole structure plus its JSON is assembled before the first byte is sent. Reachable at `RoleRead` (and anonymously on public repos). A repo whose root commit adds ~30,000 large text files makes any reader's `GET .../commits/{hash}/diff` allocate several GiB and OOM the shared process — killing every repo on the storage root. A legitimate monorepo triggers the same crash accidentally.
**Fix:** cap file count and total aggregate patch bytes with a `truncated` flag, and/or stream the diff.

**H3 — Runner claim holds a process-global exclusive lock during an O(all repos × all jobs) scan; jobs are never pruned** · `internal/runner/coordinator.go:131` · CONFIRMED
`Coordinator.Claim` takes the process-wide catalog lock `Exclusive` and, while holding it, walks the entire storage tree and JSON-unmarshals every historical job file. All git push/receive-pack and LFS traffic takes that lock `Shared`, so they block for the full scan. No job pruning exists anywhere, and each idle remote runner re-runs this every 2s. On a mature server (many repos, thousands of accumulated job files) every 2s scan blocks all git/LFS traffic for an ever-growing window — a self-inflicted DoS that worsens over time; a client accelerates it by pushing many branches.
**Fix:** maintain a claimable-job index/queue guarded only by the queue lock so `Claim` is O(pending); add job retention/pruning; return fast on idle polls; at minimum take the catalog lock `Shared` for the read-only scan.

**H4 — Build containers run with no network isolation or resource limits** · `internal/runner/executor.go:63` · CONFIRMED
The docker argv sets no `--network`, `--memory`, `--pids-limit`, `--cpus`, `--cap-drop`, `--read-only`, or `--user`. Build script and image come from a repo's `.gitone.yaml`, and a build is scheduled on push, so any Developer controls the executed code. A Developer can push a build script that curls `http://169.254.169.254/.../iam/security-credentials/` to exfiltrate cloud IAM credentials — bypassing the very SSRF allowlist the import path enforces — or run a fork bomb / memory hog. (Server-process blast radius applies specifically when the runner is co-located; the metadata/credential exposure is unconditional.)
**Fix:** default `--network none` (opt-in, admin-gated egress only); always apply memory/swap/pids/cpu limits, `--cap-drop=ALL`, `--security-opt no-new-privileges`, and a non-root `--user`; make these non-overridable by repo config.

**H5 — JSON API and UI endpoints have no active-request I/O deadline (unauthenticated slowloris)** · `cmd/gitone/main.go:99` · CONFIRMED
The server sets only `ReadHeaderTimeout` and `IdleTimeout`; `ReadTimeout`/`WriteTimeout` are unset, and the Huma API, SPA, and the raw runner-source tarball handler (`internal/httpapi/runner.go:216`) get no per-request deadline or global `TimeoutHandler`. An attacker opens many connections to unauthenticated `POST /api/session`, completes headers within 10s, then trickles the JSON body one byte every few minutes; with no body-read deadline and no connection cap, each goroutine/FD is pinned indefinitely until exhaustion. Slow-reading clients pin response writes the same way. This is the same defect class already fixed for Git, left unaddressed for API/UI.
**Fix:** wrap non-Git handlers with `httpio.Protect` or a global `http.TimeoutHandler` plus modest `ReadTimeout`/`WriteTimeout`; add `MaxBytesReader` to JSON bodies.

### Medium (10)

**M1 — Maintainer can flip the group `Inherit` flag (owner-only guard omits it)** · `internal/httpapi/api.go:630` · CONFIRMED
`ownerOnlyGroupSettingsChanged` compares only Members, Visibility, and LFS — not `Inherit`, which is copied verbatim from request input (`api.go:530`). A maintainer of an isolated subgroup submits unchanged members but `Inherit=true`; the guard passes, and parent members (e.g. a parent's developer) gain access to the previously isolated subgroup — an authorization-scope change the code explicitly reserves for owners.
**Fix:** include `Inherit` in the owner-only guard (require `RoleOwner` to change it).

**M2 — Default LFS policy is unlimited, removing the concurrent-upload staging bound** · `internal/lfs/lfs.go:369` · CONFIRMED
The per-group staging reservation only engages when `MaximumStorageBytes > 0`, but new groups default to `Enabled:true` with both limits `0`. With quota 0, `stageLimit` stays at 100 GiB and no reservation is taken. A Developer opens N concurrent PUTs each streaming up to 100 GiB into `.gitone/uploads`; the sha256 check runs only after the full body is written, so no valid OID is needed to fill the volume and down the server.
**Fix:** always take a staging reservation (treat unset quota as a finite server-wide default); cap concurrent LFS PUTs; reject when free disk is low.

**M3 — Receive-pack writes pack objects before validation, and no GC ever reclaims orphans** · `internal/githttp/handler.go:273` · CONFIRMED
`UpdateObjectStorage` persists incoming objects before LFS-pointer and control validation and before refs are applied; on validation failure the objects remain, unreferenced. There is no GC/prune/repack anywhere in the codebase. A Developer repeatedly pushes packs (up to 100 GiB) that intentionally fail LFS-pointer validation — objects land on disk, no branch is created, nothing is ever reclaimed, permanently consuming disk invisibly. Normal force-pushes leak similarly.
**Fix:** use git's quarantine model (stage into an alternate object dir, migrate only after all validation passes), add bounded periodic GC/prune and a per-repo git quota, and lower the 100 GiB ceiling.

**M4 — `listRepositoryCommits` walks the entire commit history on every page request** · `internal/httpapi/browser.go:932` · CONFIRMED
The `ForEach` callback increments an exact total for every reachable commit, never terminates early, and does not check `ctx`. On a repo with 10^5–10^6 commits, even `page=1&perPage=1` forces an O(total-history) object read that runs to completion after client disconnect; any reader can issue these repeatedly to saturate CPU/disk I/O.
**Fix:** read `perPage+1` for `HasNext` instead of an exact total (or bound the traversal), and honor `ctx` in the loop.

**M5 — Branch compare loads the full reachable-commit set of both branches into memory** · `internal/httpapi/merge.go:470` · CONFIRMED
`reachableCommits` builds an in-memory hash set of every commit reachable from each tip, for both base and head, with no `ctx` and no cancellation, running before the also-unbounded `compareTrees`. Reachable at `RoleRead`. A reader repeatedly hitting `/compare` on a large-history repo forces two full-graph traversals per request, spiking memory/CPU on the shared process.
**Fix:** cap the ahead/behind computation (report `>=N`), use a limited revlist walk, and respect `ctx`.

**M6 — MR approval gate is advisory: any developer can write the protected target branch directly** · `internal/httpapi/merge.go:100` · CONFIRMED
Approval/thread gating lives only inside the MR merge path. The same branch can be moved via the direct-merge endpoint `POST /.../merges` (`RoleDeveloper`) or plain `git push` (`RoleDeveloper`, `internal/server/server.go:68`), neither of which checks approvals — and there is no branch-protection concept anywhere. A developer authors a change and lands it on `main` via `POST /.../merges` or push, bypassing review entirely; separation of duties is unenforceable. (Even within the MR path, a maintainer author can self-approve+merge via the intended override flag.)
**Fix:** add protected-branch config enforced in both the direct-merge endpoint and the receive-pack authorizer; if MRs are intentionally advisory, document that.

**M7 — Remote repository import has no overall size or time bound** · `internal/storage/storage.go:322` · CONFIRMED
`PlainCloneContext` (Mirror) has no size cap; LFS import allows 100 GiB per object with unbounded object count; the only timeouts are per-request dial (30s) and response-header (60s), neither bounding the streaming body, and the import runs on the inbound request context with no overall deadline. Requires `RoleMaintainer`. A maintainer (or compromised token) points import at a huge or slow-loris remote — filling the disk under the storage root (breaking all writes) or pinning import goroutines/sockets/staging far beyond 60s.
**Fix:** wrap imports in `context.WithTimeout`, enforce a total-bytes budget across clone + all LFS objects, lower the per-object cap, and add a global concurrent-import limit and disk preflight.

**M8 — Server has no graceful shutdown; SIGTERM aborts in-flight receive-pack** · `cmd/gitone/main.go:28` · CONFIRMED
`main()` calls `log.Fatal(server.ListenAndServe())` with no signal handling and never calls `Shutdown()`. On `docker stop`/k8s termination/systemd restart, an in-flight push is cut mid-write between `UpdateObjectStorage` and ref application, leaving loose/dangling objects, an inconsistent bare repo, and no report-status to the client; in-flight API mutations are severed with no draining.
**Fix:** `signal.NotifyContext(SIGINT,SIGTERM)`, run `ListenAndServe` in a goroutine, and call `Shutdown(ctx)` with a bounded grace period — mirroring the runner.

**M9 — Containers run as root and base images are pinned only by tag** · `Dockerfile:17` · CONFIRMED
Neither Dockerfile declares `USER`, so server and runner run as UID 0; base images use mutable tags; the runner mounts `/var/run/docker.sock` as root (root-equivalent on the host). Any RCE/path-traversal bug in the server yields root inside the container with a root-owned `/data` bind mount, and a repointed upstream tag is pulled silently on the next build.
**Fix:** add a non-root `USER` to both images with `/data` owned accordingly, pin bases by sha256 digest, drop docker.sock from the server, and add read-only rootfs / no-new-privileges.

**M10 — No Content-Security-Policy or other security response headers on UI/API** · `internal/webui/webui.go:35` · CONFIRMED
`index.html` ships only Content-Type and Cache-Control; there is no security-header middleware, and the UI renders user-controlled markdown/blob content relying solely on DOMPurify at the sink (`web/src/app.ts:4652`). A single DOMPurify bypass, missed `innerHTML` sink, or dependency regression becomes full script execution in the authenticated origin with no CSP backstop; absent `frame-ancestors`/`X-Frame-Options` allows clickjacking of merge/settings actions.
**Fix:** response-header middleware setting a strict CSP, `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, and HSTS over HTTPS.

### Low (14)

**L1 — Repository file path parameter has no depth or length limit** · `internal/httpapi/browser.go:1044` · CONFIRMED
`cleanRepositoryPath` rejects traversal/NUL but not length or component count; tree-edit helpers recurse and write a tree object per component. A Developer POSTing a ~10^5-component path creates ~10^5 nested tree objects in one call, with attacker-controlled recursion depth. **Fix:** bound path length and component count (400 on violation); consider iterative tree rewrite.

**L2 — Body slowloris: sliding idle deadline has no absolute cap and no connection limit** · `internal/httpio/httpio.go:21` · PLAUSIBLE
The read/write deadline resets on every Read, bounding a single blocked read but not total request duration; no connection cap exists. A client trickling one byte every <30s holds a goroutine/socket/temp file indefinitely. Auth is required for the heavy paths and Go tolerates many idle connections, which tempers it. **Fix:** add an absolute per-request deadline or minimum-throughput requirement and bound concurrent in-flight transfers.

**L3 — HTTP server sets no WriteTimeout or overall request timeout** · `cmd/gitone/main.go:99` · CONFIRMED
No `WriteTimeout` or per-handler timeout middleware means CPU-bound handlers (H2/M4/M5) cannot be aborted and slow readers pin connections. Defense-in-depth backstop for the unbounded-work handlers. **Fix:** add a `WriteTimeout` and wrap the mux in `http.TimeoutHandler` (keep streaming endpoints on the sliding-deadline path).

**L4 — Stateless sessions cannot be revoked; logout leaves signed cookies valid** · `internal/auth/session.go:161` · CONFIRMED
Sessions are self-contained securecookies with no server store; logout only clears the client cookie, and a captured cookie stays valid until MaxAge (12h) despite logout, password change, or account disable. (Authorization changes do take effect immediately since the cookie carries identity only.) **Fix:** embed a per-user token version/issued-at checked against a server counter bumped on logout/password-change/disable, or shorten MaxAge; at minimum document non-revocability.

**L5 — Internal go-git error strings are echoed to clients in API error details** · `internal/httpapi/browser.go:1010` · CONFIRMED
Handlers pass raw underlying errors as the second arg to `huma.Error*`, which serializes their `Error()` text (object IDs, pack/loose state, filesystem messages) into the response body for low-trust callers. **Fix:** log the underlying error server-side and return generic client-facing messages for 5xx/not-found on low-trust endpoints.

**L6 — A single unreadable/invalid review record disables listing and all mutations for the repo's MRs** · `internal/review/store.go:437` · CONFIRMED
`listDirectory` aborts on the first record that fails parse/validate, and `Create`/`Update` re-list for a duplicate-branch scan — so one corrupt file breaks List, Create, and every open-MR mutation repo-wide. `DisallowUnknownFields` also makes a newer binary's records unreadable to older binaries (downgrade/mixed-version hazard). Trigger is out-of-band (manual edit, restore, version skew). **Fix:** skip-and-log bad records rather than aborting; reconsider `DisallowUnknownFields` behind a schema-version field.

**L7 — Atomic review-record writes and directory renames are not durably synced** · `internal/review/store.go:555` · CONFIRMED
The temp file is fsynced but the containing directory is never fsynced after rename (also in `relocate`/`rewriteDirectory`). On power loss after a successful return, a lost rename can silently revert an approval/comment/close transition the API already confirmed; only the merge-in-progress case is reconciled. **Fix:** fsync the parent directory after rename and document the durability contract.

**L8 — Group LFS usage is recomputed with a full filesystem walk on every quota-relevant request** · `internal/lfs/lfs.go:626` · CONFIRMED
`groupStorageUsage` walks the whole objects tree (twice per PUT, under the exclusive group lock) with no cached accounting — O(objects) per request, serializing concurrent PUTs. Only occurs when a storage quota is configured. **Fix:** maintain an incremental per-group byte counter updated under the existing lock.

**L9 — Token-key enumeration via Argon2id timing / non-constant-time key comparison** · `internal/auth/auth.go:62` · CONFIRMED
The short-circuiting `token.Key != user` check gates the expensive Argon2id call, so requests matching a valid token key are measurably slower — a clean latency oracle (especially for non-email keys with no LDAP round-trip) revealing configured automation token identifiers. Leaks non-secret labels only. **Fix:** always run one dummy Argon2id against a decoy hash on no-match and compare keys with `subtle.ConstantTimeCompare`.

**L10 — LDAP canonical identity is case-normalized only for the `mail` attribute** · `internal/auth/ldap.go:232` · CONFIRMED
For non-`mail` canonical attributes (uid, sAMAccountName), the directory value is used verbatim (trimmed) as `Principal.Name`, while `control.json` member lookups are exact case-sensitive map lookups — so `Alice` vs recorded `alice` misses (fail-closed but a confusing config trap). **Fix:** apply one documented normalization to all attribute types and normalize member keys identically.

**L11 — Session cookie `Secure` flag is off by default because the default public URL is http** · `internal/auth/session.go:38` · CONFIRMED
`Secure` keys off the `-public-url` scheme, which defaults to `http://localhost:8080`, and `GITONE_SESSION_SECURE=false` can force it off regardless. Exploitation requires a TLS-terminating proxy + unchanged http public-url + a downgrade request; SameSite=Strict and HttpOnly limit it. **Fix:** default `Secure` true unless explicitly disabled, refuse `SECURE=false` with an https public URL, and warn/fail on scheme mismatch at startup.

**L12 — Dead, build-excluded root `ldap.go` carries LDAP-injection and TLS-skip patterns** · `ldap.go:1` · CONFIRMED
The `//go:build ignore` root file (foreign `devboxgateway` imports, cannot compile) contains an unescaped `fmt.Sprintf` LDAP filter and a config-gated `InsecureSkipVerify`, sitting misleadingly beside the real, correct authenticator. Not exploitable. **Fix:** delete it (or move any reference material out of the module root, clearly marked).

**L13 — Stale foreign-project source file and unused config package** · `ldap.go:1` · CONFIRMED
The same file is a divergent second LDAP implementation, and `internal/config` (`Load`) is referenced only by tests while the server configures via flags — dead code obscuring how the server is actually configured. **Fix:** delete leftovers or consolidate to one authoritative LDAP implementation; remove or wire in `internal/config`.

**L14 — docker-compose ships insecure defaults** · `docker-compose.yml:13` · CONFIRMED
`LDAP_SKIP_TLS_VERIFY=true` over the password-bearing bind, a published default `GITONE_RUNNER_TOKEN`, and a host `/data` bind mount, with no in-file warning that these are dev-only. Risk materializes if copied toward production. **Fix:** split a labeled dev compose from a production example, require LDAP TLS verification, require a non-default runner token with no fallback, and fail fast if the token equals the known dev value.

### Info (1)

**I1 — `LDAP_SKIP_TLS_VERIFY` disables certificate verification for credential-bearing binds** · `internal/auth/ldap.go:117` · CONFIRMED
When set, disables cert validation (defeating ServerName pinning) for both ldaps:// and StartTLS while the plaintext simple bind sends the user password — an operator MITM footgun. **Fix:** treat as strictly a dev toggle: warn prominently at startup, forbid with an https public URL, and steer operators to `LDAP_CA_FILE`.

## 4. Cross-cutting themes

- **Missing resource limits is the defining pattern.** Well over half the findings (H1–H5, M2–M5, M7, L1–L3, L8) are the same story: no cap on request time, response/body size, object or file counts, concurrency, or growth-over-time. Every unbounded quantity terminates at the same failure — the shared single process.
- **No wall-clock request budget anywhere.** `cmd/gitone/main.go:99` recurs across findings: no `ReadTimeout`/`WriteTimeout`/`TimeoutHandler`, and the expensive handlers ignore `ctx`. Timeouts and a connection cap are the single missing backstop that would blunt H2, H5, M4, M5, L2, and L3 at once.
- **Single-process, single-writer blast radius.** One OOM, one filled disk, one held catalog lock, or one abrupt SIGTERM affects every repository on the storage root. This architecture (`lockmgr.Process` in-memory) is also the ceiling on HA and zero-downtime deploys.
- **Authorization gates that aren't authoritative.** The MR approval machinery (M6) and the owner-only settings guard (M1) are carefully written but bypassable, because there is no protected-branch concept and the guard omits `Inherit`. The controls exist; their coverage is incomplete.
- **Write-then-validate and durability gaps.** Receive-pack persists before validating (M3), review writes skip directory fsync (L7), and one bad record bricks the MR store (L6) — a recurring "the write path is optimistic about failure" theme distinct from the DoS cluster.
- **Deployment/ops hardening is unfinished.** Root containers (M9), tag-pinned bases (M9), no security headers (M10), no graceful shutdown (M8), and insecure compose/LDAP defaults (L14, I1) point to a codebase whose application security matured faster than its operational packaging.
- **Dead/duplicated code in the auth surface** (L12, L13) is a maintainability trap precisely where correctness matters most.

## 5. Extra value to add

Prioritized product and ops improvements. Effort is S/M/L/XL; value is medium/high/transformational. Items that also close a finding are flagged.

### Quick wins (small effort, outsized return)

- **Rate limiting & brute-force protection** — S, high. In-memory token-bucket keyed by IP/principal, stricter on `POST /api/session` and Basic-auth failures, plus a concurrency limiter on expensive ops. _Closes H1._
- **Graceful shutdown & connection draining** — S, high. `signal.NotifyContext` + `Shutdown(ctx)` with a drain timeout. _Closes M8; prerequisite for zero-downtime deploys._
- **Security-header middleware (CSP, HSTS, X-Frame-Options, nosniff, Referrer-Policy)** — S, high. One wrapper in `server.go`. _Closes M10._
- **Request deadlines + connection cap** — S, high. `WriteTimeout`/`ReadTimeout` + `TimeoutHandler` + `MaxBytesReader` on JSON bodies. _Closes H5, L2, L3; blunts H2/M4/M5._
- **Readiness endpoint `/readyz` with dependency checks** — S, high. Real LDAP/storage/control checks so LBs don't black-hole traffic (keep `/healthz` a static liveness ping).
- **MR merge gating on CI build status** — S, high. Fold the head commit's latest build status into the existing readiness gate in `reviews.go`.
- **Multi-arch (arm64) images** — S, medium. Add `platforms: linux/amd64,linux/arm64` in CI; static `CGO_ENABLED=0` builds need no code change.

### High-impact (medium effort, high value)

- **Protected branches & push rules** — M, high. Generalize the existing `control.git` ref validation in `githttp/handler.go` to glob-matched branches (block direct push, forbid force-push/delete, require approvals/build). _Closes M6._
- **Personal access tokens with self-service UI** — M, high. Adds the missing per-user git-backed store (reusing `HashSecret`/`VerifySecret`), removing the LDAP-password-in-URL smell and unblocking SSH/2FA later.
- **Structured logging + request IDs + panic-recovery middleware (slog)** — M, high. No access logging and no panic recovery today means a nil-deref crashes the whole process. One choke point in `server.go`; stdlib only.
- **Prometheus metrics `/metrics`** — S–M, high. RED metrics plus runner queue depth, LFS storage, and auth-failure gauges — needed to alert on the very saturation the DoS findings describe.
- **Audit logging (structured, append-only)** — M, high. Emit from `authorizeRepo` and the Huma mutation handlers; on the team's own gap list and a compliance gate for self-hosters.
- **Outbound webhooks (HMAC-signed, retried)** — M, high. Highest-leverage integration primitive; the push/MR/build event points already exist. Validate destinations through the existing SSRF policy.
- **Email notifications over SMTP** — M, high. Reviewers currently learn nothing without polling; the canonical LDAP identity is already an email address.
- **Backup/restore tooling + DR runbook** — M, high. Plain-filesystem storage with no documented consistent-snapshot path is unacceptable for a source-of-truth system; use the catalog lock for a consistent point-in-time view.
- **Supply-chain hardening (SBOM, cosign signing, provenance, digest-pinned bases/actions)** — M, high. Complements M9 and unblocks SLSA/Sigstore-conscious adopters.
- **Per-group quotas + global body/header limits** — M, medium–high. Repo-count and git-storage quotas plus `MaxHeaderBytes`/`MaxBytesReader`. _Reinforces M2, M3, M7, L1._
- **Build pipeline enhancements (manual/scheduled triggers, secrets, artifacts, caches)** — L, high. Makes the existing runner competitive; combine with H4's sandboxing.
- **SSH Git transport with per-user keys** — L, high. Expected default transport; eliminates password-in-URL; shares the per-user key store with PATs/2FA/signed commits.
- **Kubernetes/Helm chart (single-replica, RWO PVC, probes, limits)** — L, high. Encodes the single-writer constraint and pairs with readiness + graceful shutdown to lower adoption friction.

### Strategic (large effort, transformational or foundational)

- **Cross-process locking for HA / horizontal scaling** — XL, transformational. The hard production ceiling: `lockmgr.Process` forbids more than one web instance. Phase 1: OS advisory file locks so a second process blocks rather than corrupts; Phase 2: externalize coordination (Postgres/Redis) behind the existing `lockmgr.Request` seam.
- **Issue tracker with labels & milestones** — L, transformational. The biggest completeness gap vs. Gitea/Forgejo/GitLab; model on the review package (a `<repo>.issues` sidecar) with `Closes #N` auto-close on merge.
- **OCI container/package registry** — XL, transformational. Pairs with built-in CI as a standout differentiator; reuse the sha256-sharded LFS layout and quota accounting. Sequence after PATs and metrics.
- **Code & repository search** — L, medium. Phase 1 (S): authz-filtered name/description search; Phase 2 (L): incremental code index (bleve).
- **Distributed tracing (OpenTelemetry)** — M, medium. `otelhttp` is already an indirect dep; makes the request-ID logs actionable across LDAP→authz→go-git→LFS.
- **Rounding-out features** (each ~M, medium unless noted): CODEOWNERS-based required reviewers, signed-commit verification, TOTP 2FA, releases with artifacts, scheduled pull/push mirroring, per-user dashboard, cross-group teams, LFS management UX (usage + file locking), config-file loading + `_FILE` secrets, versioned secret rotation, automated schema migration, first-class TLS termination, CSRF defense-in-depth, and build job/log retention pruning (the last also mitigates H3's unbounded growth).

## 6. Suggested roadmap

**First — stop the bleeding (harden the single process against DoS; mostly quick wins):**
1. Add request deadlines, `WriteTimeout`, `TimeoutHandler`, `MaxBytesReader`, and a connection cap (H5, L2, L3; blunts H2/M4/M5).
2. Add auth rate limiting + an Argon2id concurrency semaphore (H1).
3. Cap diff/compare output and fix the history/reachable-set walks to honor `ctx` (H2, M4, M5).
4. Fix the runner claim to not hold the exclusive catalog lock during scans, and add job pruning (H3).
5. Sandbox build containers: `--network none` by default plus cgroup limits and non-root user (H4).
6. Land the remaining quick wins: graceful shutdown (M8), security headers (M10), `/readyz`.

**Next — close authorization and correctness gaps, and get observable:**
1. Protected branches enforced in both the direct-merge endpoint and receive-pack (M6); include `Inherit` in the owner-only guard (M1).
2. Always reserve LFS staging (M2); quarantine receive-pack objects + add GC and git quotas (M3); bound imports by size and time (M7).
3. Robustness: fsync review directories (L7), make the review store skip-and-log bad records (L6), and bound file-path depth (L1).
4. Add structured logging/request-IDs/panic recovery, Prometheus metrics, and audit logging.
5. Container/image hardening (M9), personal access tokens, and the cheap credential-hygiene fixes (L4, L5, L9, L11, L14, I1).
6. Delete the dead `ldap.go`/`internal/config` leftovers (L12, L13).

**Later — grow the product and remove the scaling ceiling:**
1. Cross-process locking (unlocks HA and zero-downtime deploys) plus the Helm chart and backup/restore tooling.
2. Feature depth: issues, SSH transport, protected-branch-driven CI gating, webhooks, and email notifications.
3. Strategic bets: OCI registry, code search, distributed tracing, and the rounding-out security/DX features.
