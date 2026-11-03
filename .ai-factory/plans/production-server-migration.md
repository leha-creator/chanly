# Implementation Plan: Fresh production installation and backend automatic deployment

Branch: feature/checks-and-access (existing working branch; no branch switch performed)
Created: 2026-11-03
Status: Host provisioning and reboot verification complete. Application activation awaits production service configuration; repository edits remain uncommitted/unpushed.
Mode: full; user invoked implementation of this plan. Separate plan preserves existing PLAN.md and unfinished work.

## Settings
- Testing: yes (plan default used for implementation)
- Logging: verbose during provisioning and rehearsals, production application INFO; no credentials, connection strings or payloads in logs (proposed).
- Docs: yes (existing operational docs updated through $aif-docs).
- Git: do not switch or merge the dirty working tree. Before implementation choose an isolated branch based on the agreed source revision and align the plan filename with that branch.
- Scope: authorized implementation, including production host provisioning and GitHub CI/CD configuration. External service configuration is required before application activation.

## Roadmap Linkage
Milestone: "none"
Rationale: User preference pending. Candidate: "Юридическая и эксплуатационная готовность, пилот"; this work covers deployment readiness, not completion of the entire milestone.

## Research Context
Source: .ai-factory/RESEARCH.md, Active Summary dated 2026-10-12.
Goal: MAX advertising marketplace with publishing, marking and settlement integrations.
Constraints relevant here: production MAX delivery uses HTTPS webhooks; integrations, object storage, mail and platform configuration must be prepared for production.
Open questions: the historical research includes unresolved integration and legal prerequisites. Do not interpret infrastructure readiness as approval to enable live payouts or production business operations. Preserve ROCKETWORK_MODE=disabled unless separately resolved.

## Inputs required before implementation
- Arrange secure interactive password access for root@194.58.120.196:22; inspect architecture, resources and existing services. Do not place private keys or passwords in this plan. Provision a dedicated deploy account and SSH key for GitHub Actions and verify it before changing administrative access.
- Confirm cabinet domain, optional CDN and email domains, DNS access and admin/VPN CIDRs. API target is https://chanly.ru; do not invent api/app/cdn subdomains.
- Fresh installation is confirmed: no source database or object transfer, write freeze or old-host traffic cutover is required.
- MockAPI is retired: remove its build/publish/deploy automation. Determine whether an old running mock container exists and stop it after checking it does not serve other workloads; no need to remove historical source or volumes as part of this plan. Keep staging only if still useful, with no automatic legacy mock deployment.
- Confirm S3, backup storage, SMTP, MAX/ORD credentials availability and source of secrets. Frontend deployment is outside the current repository and requires its own source if intended.
- Implementation follows the proposed tests/logging/docs defaults; no roadmap milestone is marked complete.

## Implementation progress (2026-11-03)
- Task 1 complete: DNS confirmed; root SSH verified against user-provided ED25519 fingerprint SHA256:SA59ZuoxO3T+TbT5e1c90aP0OzBcScfzLlttw/AQ9Fo. Server Ubuntu 26.04 LTS, x86_64, 3909 MiB RAM, 35 GiB free, no Docker or application services initially; SSH listens on 22.
- Task 2 complete: Docker 29.9.2 / Compose 5.7.0 installed, deploy key login verified, UFW allows 22/80/443, persistent Caddy running. Bootstrap rerun preserved env files. Reboot confirmed by changed boot ID; Docker/Caddy restarted automatically. Root password access intentionally remains enabled.
- Task 3 partial: production API and MAX webhook URLs set to chanly.ru. Database passwords, APP_SECRETS_KEY, AUTH_CODE_PEPPER and webhook secrets generated only on server; env permissions 600. TRUSTED_PROXY_CIDRS=172.18.0.0/16 matches edge; BACKUP_PREFIX=chanly-production. External credentials, platform identity, admin CIDRs and initial PostgreSQL image still require configuration.
- Task 6 partial: shellcheck, actionlint, env template checks, production/edge Compose config, Caddy validation, release guards and full release rehearsal passed. Backup rehearsal also passed: logical restore, PITR (rows 1,2), corrupted checksum rejection and pruning. Initial rehearsal invocation inherited APP_ENV_FILE from a preceding production-template check; rerun with isolated rehearsal environment passed. Server bootstrap/rerun/reboot also passed. Actual GitHub CI run remains pending publication of code.
- Task 7 partial: GitHub production Environment created with DEPLOY_HOST=194.58.120.196, DEPLOY_USER=deploy, DEPLOY_DOMAIN=chanly.ru. Custom deployment branch policy allows only master; no required reviewers. Dedicated DEPLOY_SSH_KEY and verified DEPLOY_KNOWN_HOSTS installed and deploy login verified. Production database initialization and real off-host backup restore remain pending.
- Task 9 partial: README, deployment and launch instructions updated; local rollback exercised. Actual production activation/recovery remains pending.
- Repository edits are uncommitted and unpushed; existing unrelated changes were preserved. Host bootstrap completed using a dedicated SSH key; no application production release has been performed.
- SSH access: Windows user-owned setup key is in %TEMP%/maxim-production-access-user-ed25519; verified hosts file %TEMP%/maxim-production-known-hosts; CI key %TEMP%/maxim-production-deploy-ed25519. Never print private key contents. Use elevated tools under the Windows PC account; sandbox account cannot read the user-owned keys. Windows ssh-keyscan cannot negotiate this server's preferred KEX; regular ssh works and host key was independently verified.
- Blocking inputs: real production app/stack settings (S3 including separate backups, SMTP, MAX/ORD, platform identity and admin allowlist). Do not mark remaining operational tasks complete without live evidence.

- HTTPS verified by Linux curl and external Windows curl; the latter required revocation-best-effort because the local revocation endpoint was offline. chanly.ru returns intentional HTTP 503 from /opt/edge/sites/maxim-production.caddy until first real application release. No backend containers or production database are running yet.

## Confirmed target (user, 2026-11-03)
- Server: 194.58.120.196, Ubuntu 26.04 LTS.
- Production domain: chanly.ru; DNS zones were just changed. Resolution and HTTPS readiness are not yet verified.
- Trigger: every push to master, including merges, after required checks succeed for that exact commit. PRs run checks without deployment. Retain manual recovery/rollback.
- Deploy the real Go backend; the mockAPI service is no longer needed.
- Initial server authentication: root, SSH port 22, password. CI authentication: provision dedicated deploy account and SSH key during bootstrap; do not store the root password in workflow code or use root as the routine CI account.
- Installation: clean production installation, no existing application data to migrate. Initialize a new database and production storage; do not import development data.
- DNS checks must inspect authoritative/public A and AAAA records and, if nameservers changed, delegation. Expected IPv4 is 194.58.120.196; do not invent an IPv6 address. Host preparation can proceed before propagation; public TLS/readiness is a required activation gate, not a warning-only success.

## Verified repository findings
- `.github/workflows/deploy.yml` deploys mockAPI to dev on master; it must not become the production backend pipeline by merely replacing its host.
- `.github/workflows/release.yml` builds and manually releases the real backend to staging/production. Environment variables: DEPLOY_HOST, DEPLOY_USER, DEPLOY_DOMAIN. Secrets: DEPLOY_SSH_KEY, DEPLOY_KNOWN_HOSTS. Server configuration lives under `/opt/maxim-production`.
- Release currently has no dependency on backend/ops check success; build and deploy independently resolve a mutable ref. CI path filters omit changes to release.yml.
- `deploy/bootstrap.sh` prepares the dev directory `/opt/maxim`; edge provisioning currently relies on another repository. The production stack needs a persistent Caddy edge and external Docker network `edge`.
- `deploy/app/release.sh` already supports two slots, configuration validation, migration, readiness and application rollback. Reusing the active tag returns early, so it cannot reliably reapply domain/admin config as currently documented.
- Production environment templates contain explicitly assumed domains, storage and mail values. Workflow DEPLOY_DOMAIN and server DOMAIN are separate inputs.
- Backup/WAL/PITR and release rehearsals already exist; reuse them. Rollback of an application slot is not rollback of a migration or server cutover.

## Commit Plan
- After tasks 1–3: `feat(deploy): provision production host and edge configuration`
- After tasks 4–6: `ci: gate production releases on verified revisions`
- After tasks 7–9: `docs(deploy): document production cutover and recovery`
Operational changes do not constitute commits; commit only scripts, sanitized templates, checks and runbooks. Include only changes owned by this plan.

## Tasks

### Phase 1: Target and provisioning
- [x] Task 1: Record the confirmed deployment contract and inspect target readiness.
  - Files: this plan; implementation runbook `deploy/app/README.md`.
  - Resolve remaining inputs above, inspect OS/resources/services/ports and current DNS through root SSH on port 22. Establish API/cabinet/CDN/mail mapping for the confirmed clean installation. Resolve SSH host fingerprint through a trusted channel.
  - Deliverable: verified target inventory and installation prerequisites; no guessed production values. Dependencies: initial SSH access and required service settings.
  - Logging: INFO inventory/check outcomes, WARN unmet prerequisites; never dump environment or keys.
- [x] Task 2: Make production host and edge provisioning reproducible, then prepare the new host.
  - Files: `deploy/bootstrap.sh`, new `deploy/app/bootstrap.sh`, new `deploy/edge/compose.yml` and `deploy/edge/Caddyfile`, `deploy/app/README.md` (choose minimal reuse after inspection).
  - Prepare Docker/Compose, deploy account and keys, firewall, required directories/permissions, persistent edge TLS storage and external network. Parameterize production directory and SSH port if needed. Keep administrative recovery access while verifying SSH changes. Preserve any existing Docker workloads.
  - Deliverable: rerunnable setup on the confirmed OS, edge validation/reload and restart after reboot; no dependency on an unavailable tracker repository. Dependencies: 1.
  - Logging: INFO step/result; DEBUG nonsecret diagnostics; ERROR failed prerequisites. Avoid printing failing command text if it contains secrets.
- [ ] Task 3: Prepare complete production configuration and domain topology.
  - Files: `deploy/app/env/production/app.env.example`, `stack.env.example`, `deploy/scripts/check-env.sh`, `deploy/certs/README.md`; server `/opt/maxim-production/{app.env,stack.env}`.
  - Set confirmed DOMAIN/PUBLIC_BASE_URL/MAX_WEBHOOK_URL/CABINET_BASE_URL and applicable S3/mail URLs independently. Set narrow trusted proxy/admin CIDRs. Prepare MAX CA, S3 and distinct backup access, SMTP, real required configuration, pinned initial PostgreSQL image and protected secret files. No secrets enter git. Validate all api/worker/migrate roles.
  - Deliverable: configuration passes checks with no placeholders; domain consistency enforced. Dependencies: 1, 2.
  - Logging: INFO validated field names and roles; ERROR missing/invalid names only; never values of secrets.

### Phase 2: Release automation and regression checks
- [x] Task 4: Gate production deployment on checks for one immutable revision.
  - Completed locally: deploy now calls the real release on master pushes; backend/ops are reusable required gates, checkout is pinned to resolved SHA, automatic stale revisions are rejected. actionlint passed. Changes are not yet committed/pushed or run in GitHub Actions.
  - Files: `.github/workflows/deploy.yml`, `release.yml`, `backend.yml`, `ops.yml`.
  - Replace legacy mockAPI automation with one production entry point on every push to master (including merges). Reuse the real backend release mechanism; remove mockAPI build/push/deploy jobs and avoid two competing deploy workflows. PRs check only. Resolve source once to full SHA and use that revision for checks, image and uploaded scripts; bind the deployed artifact to that revision. Reuse/factor backend and operations checks so production cannot deploy on failed, skipped-required or unrelated checks. Do not let path filters silently omit a required gate or the promised master deployment. Keep PR runs away from deployment secrets, shared environment concurrency and rollback independent of new builds. Prevent queued older revisions from superseding an already newer deployed revision. Configure production branch restrictions and environment settings to support the requested automatic deployment without an unexpected manual approval requirement.
  - Deliverable: every eligible master push deploys the checked real backend to 194.58.120.196 at https://chanly.ru; mockAPI is never built/published/deployed by the replacement automation. Dependencies: 1.
  - Logging: INFO environment, source SHA, artifact identity, gate results; ERROR blocking checks. No tokens or secret files in output.
- [x] Task 5: Support explicit configuration reapplication and robust target validation.
  - Verified: domain mismatch/missing edge reject before state changes, --reapply deploys unchanged image with updated settings. Docker rehearsal passed first release, upgrade, rollback and configuration reapply: 685 requests, zero unexpected/5xx/connection failures.
  - Files: `deploy/app/release.sh`, `deploy/app/maxim-app.caddy`, `.github/workflows/release.yml`, `deploy/local/rehearse-release.sh`.
  - Resolve same-tag early return with an explicit safe configuration-apply path or equivalent documented behavior. Validate DEPLOY_DOMAIN against target configuration before mutation, then Caddy validate/reload and public readiness. Keep previous slot/config available on failure. Distinguish application rollback from database recovery.
  - Deliverable: changing domain/admin settings with unchanged application image works and failure does not silently report success. Dependencies: 3, 4.
  - Logging: INFO tag/config changes and rollback state; ERROR failed validations; DEBUG sanitized readiness attempts.
- [ ] Task 6: Verify provisioning and release failure scenarios.
  - Files: `.github/workflows/ops.yml`, `deploy/local/rehearse-release.sh`, `deploy/local/rehearse-backup.sh`, new focused provisioning/workflow checks under `deploy/tests/` if needed.
  - Run shellcheck, template/config validation and existing release/backup restore rehearsals. Add focused cases for provisioning rerun, same-tag config apply, domain mismatch, failed CI blocking deploy, revision consistency and rollback. Verify master push/merge triggers real backend, PR never deploys, workflow-only changes are covered, legacy mock jobs are absent and unresolved DNS/TLS cannot pass activation. Verify production and staging isolation. Tests proposed pending user preference.
  - Deliverable: relevant checks pass before new production deployment; no full unrelated application test expansion. Dependencies: 2–5.
  - Logging: INFO named check outcomes; ERROR actionable failures; sanitized diagnostics only.

### Phase 3: Production activation and recovery
- [ ] Task 7: Prepare production GitHub Environment and recoverable initial data state.
  - Files/resources: GitHub production variables/secrets/protection, `/opt/maxim-production/`, `deploy/postgres/bin/*` (reuse), `deploy/app/README.md`.
  - Set production host/domain, dedicated deploy user/key and verified known hosts, SSH port 22; verify GHCR access. Initialize a new production database through migrations, fresh application encryption/auth secrets and production object storage without dev seed data. Use a distinct production backup prefix. Verify backup restoration in an isolated database before opening service to users; never restore rehearsal data over production.
  - Deliverable: deploy credentials work, clean production state is initialized and data recovery has been demonstrated. Dependencies: 3, 6.
  - Logging: INFO revision, backup identifiers/counts and verification outcomes; never copied data, keys or credential URLs.
- [ ] Task 8: Perform first production release and activate domain/webhooks.
  - Files/resources: `.github/workflows/release.yml`, server configuration, DNS provider, external webhook configuration; runbook `deploy/app/README.md`.
  - Deploy verified revision, verify chanly.ru DNS resolves to the target and establish valid HTTPS before activation. Register the production MAX webhook; check whether the same bot is used in development before worker startup so production does not steal an active development subscription. Configure Rocket Work callback only if enabled. Verify readiness, worker heartbeat, auth/cookies, media and notification URLs, inbound events and backups. Verify service recovery after reboot at the agreed point.
  - Deliverable: production traffic and subsequent releases reach the confirmed new host/domain. Dependencies: 7; DNS/TLS and provider access available.
  - Logging: INFO cutover stages, version and health outcomes; ERROR acceptance failures; redact webhook secrets and user data.
- [ ] Task 9: Finalize the operational runbook and exercise rollback.
  - Files: `deploy/app/README.md`, `deploy/README.md`, `docs/launch/README.md`; docs through $aif-docs.
  - Document first install, future releases, secrets ownership, DNS/webhook settings, restore and monitoring. Verify a subsequent release uses the new production target and exercise rollback to its previous slot. Explicitly handle first-release failure when no previous slot exists: diagnose/fix and retry rather than claim an available rollback. Rehearse database recovery separately; application rollback does not revert migrations. Retire only confirmed legacy mock service resources without deleting unrelated workloads or volumes.
  - Deliverable: repeatable release/recovery instructions and verified deployment destination; this is operational work, not a separate summary-report task. Dependencies: 8.
  - Logging: INFO rollback/recovery check outcomes and retained targets; WARN outstanding prerequisites without claiming pilot/business readiness.

## Acceptance criteria
- Target is 194.58.120.196 / Ubuntu 26.04 LTS / chanly.ru; every push or merge into master deploys the real backend after successful CI.
- New host runs real backend with valid TLS, healthy API/worker and verified backups/restoration.
- Production release uses the same checked revision throughout; failed required CI prevents deployment.
- Domain configuration, external callbacks and public links agree; DNS and TLS are verified; mockAPI build/publish/deploy automation is retired.
- Clean production initialization contains no imported development data; backup restoration, subsequent-release rollback and first-release failure handling are verified.
- No secrets are committed or disclosed; existing unrelated working-tree changes remain intact.
