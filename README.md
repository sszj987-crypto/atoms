# Atoms

Atoms is a local-first AI app-builder demo. The intended P0 journey is: configure a Responses-compatible model, describe an app, let the platform create and run it, then iterate from a preview.

## Current implementation status

P0 is implemented: Compose platform, PostgreSQL, Go + chi API, React/Vite/Tailwind UI, authentication, encrypted Responses-compatible model configuration, up to two projects per user, isolated project database/runtime, persistent Codex CLI sessions, chat runs/SSE/cancellation, mandatory verification/repair, authenticated private previews and explicit port publication, read-only source files/export, version history/source restore, idle recovery, and Point & Edit.

## Run the implemented phases

```bash
cp .env.example .env
# Replace both key placeholders and the PostgreSQL password with different
# `openssl rand -base64 32` values.
docker compose up --build
```

Open [http://localhost:8080](http://localhost:8080), register, and configure **Base URL**, **API Key**, and **Model** in Settings. The connection test performs a small request to the provider's `/responses` endpoint; it therefore requires an OpenAI Responses API-compatible service.

Creating a project initializes `/data/users/{user_id}/projects/{project_id}/` with `workspace`, `codex`, and `logs` directories; creates a dedicated PostgreSQL schema, role, and encrypted password; and reserves a stable deployment port from that user's range. New applications run only inside the Docker network, without publishing that port. The workspace embeds the private platform preview proxy; clicking **发布** (Publish) recreates the shared Runtime with the reserved application port published. The same button then becomes **取消发布** (Unpublish), which recreates the Runtime without a public port while preserving source, data, and authenticated preview. Publication changes briefly interrupt preview; failed changes restore the previous publication state. Subsequent requests resume the same Codex session from the persistent `codex` directory. Each run is independently verified with `pnpm typecheck`, `pnpm test`, `pnpm build`, a clean preview restart, and an HTTP smoke test; failed checks receive up to two repair rounds.

Project naming requests use an API JSON schema, disable reasoning when supported, and use an 8-second total timeout. Chinese and English names are supported; local name validation checks the 1–16 character limit and single-line format. Invalid, incomplete, refused, or failed naming responses fall back to `未命名项目` so creation can continue; the name can be edited later. A saved valid model configuration is still required.

The runtime image downloads and installs starter dependencies during its build. New projects with unchanged starter dependency files copy these preinstalled dependencies before starting; customized projects install normally with the image's pnpm cache. The store path is explicitly fixed so mounted project volumes reuse that cache instead of downloading the starter packages again. New dependencies can still require network downloads.

To prepare this cache on an existing server after updating the code, run `docker compose build runtime-image` before creating another project. The image build pays the download cost once and Docker caches that layer for subsequent builds. Newly created runtime containers use the updated image; already running containers continue with their existing image.

PostgreSQL is intentionally not exposed to the host. For local inspection use `docker compose exec postgres psql -U atoms -d atoms`.

## Architecture constraints

The authoritative product and architecture constraints are in [DECISIONS.md](DECISIONS.md). Platform services are `atoms-app` and `postgres` on the internal `atoms-internal` network. The control plane uses port `8080` and private previews under the same `8080` entry point. Generated applications publish their assigned project ports only after explicit deployment. API keys are encrypted at rest, passwords use Argon2id, and authentication is an HttpOnly, SameSite=Lax signed host-only cookie that works with localhost, a server IP, or a domain. HTTPS requests, including those forwarded with `X-Forwarded-Proto: https`, receive `Secure` cookies.

For a Tencent Cloud host, keep PostgreSQL private and allow inbound TCP `8080`, plus the application ports you explicitly publish beginning at `DEPLOY_PORT_BASE`. Preview needs no additional public ports or separate TLS configuration. Keep `DEPLOY_PORT_SPAN` at least `2`; each registered user reserves that many consecutive ports. Use persistent, distinct values for `APP_MASTER_KEY`, `APP_SESSION_KEY`, and `POSTGRES_PASSWORD`.

Preview runs only inside the project workspace, using a scoped path under the same platform origin. An owner-authenticated API activates/renews a 30-minute preview lease; logout revokes it. Direct browser document navigation is denied, including plain HTTP server-IP access without Fetch Metadata. The preview document is sandboxed with an opaque origin so app JavaScript cannot access platform pages, credentials, APIs, or storage. A parent request/storage bridge is restricted to the exact project path; app cookies are scoped to that path. Next routes, assets and HMR use an enforced private basePath in a separate build directory without changing app source/config. Published containers also run the app's public root-path server, which is the only mapped app port.

Upgrade by rebuilding both images and recreating the platform: `docker compose build runtime-image atoms-app`, then `docker compose up -d`. This removes the old `8081–8100` mappings. Existing project runtimes are recreated lazily on preview access to adopt the private viewer. Source, data and publication intent are retained; preview startup may briefly interrupt a published app. Remove obsolete preview-port security-group rules if desired. Docker Engine 26 or newer is required for project volume subpaths.

Deployment state survives Runtime recreation, source restore and idle recovery. Upgrading from the older always-published preview implementation makes existing projects private and closes their legacy application ports at platform startup; explicitly deploy them again to reopen those ports. This minimum implementation shares source and Runtime between preview and deployment: later development and source restore also affect a deployed application. Deployment is not a frozen release snapshot.

The Compose configuration mounts the Docker socket into `atoms-app`. This is a deliberate local-demo trade-off for Phase 2 runtime management and grants the container elevated host Docker authority. It is not a production deployment model.

## Source files and export

In Project Workspace, the right pane defaults to **预览** (Preview). Choose **文件** (Files) to browse the collapsible file tree and read source with line numbers. You can hide/show the entire tree and search file names or relative paths (case-insensitive, not file contents). Search reveals matching paths automatically; clearing it restores previous directory folds. Switching tabs preserves the preview iframe, file selection, tree visibility, and search. Files are read directly from the same persistent workspace mounted by Preview; viewing does not require a running project container. During development you can refresh to see changing files, and the file panel refreshes automatically when work ends.

The panel is read-only. **下载文件** downloads the selected file; **导出项目** exports source, static resources, dependency manifests, and lockfiles as a ZIP archive. Downloads and exports are disabled during active work; the server also checks under the same project lock as run creation/deletion and prepares a complete temporary snapshot before releasing the lock. Source access requires project ownership and is not cached.

All file operations exclude dependencies (`node_modules`), generated build directories (`.next`, `.next-dev`, `.next-preview`, `dist`, `build`, `out`), coverage/caches, Git/development-session state, logs, environment files, package-registry credentials, and private-key files. `.env.example`, `.env.sample`, and `.env.template` remain included; these templates must not contain real credentials. Symbolic links and non-regular files cannot be viewed or exported. Filtering known sensitive paths is not a secret scanner: do not put credentials into ordinary source files or environment templates.

Online viewing is limited to UTF-8 text up to 1 MiB. Binary/larger files show metadata and can be downloaded while idle. The file list is capped at 20,000 entries; ZIP export is capped at 10,000 files and 100 MiB of uncompressed content; a single-file download is capped at 100 MiB. Limit failures do not produce partial downloads. Source editing is not supported. File viewing pauses while a version is being restored.

## Project version history and restore

**版本历史记录** beside the workspace project name opens a left-side history drawer. New projects save an initial source version; existing projects save current source as a baseline before their first new run. Verified successful runs save an immutable ZIP and file-hash manifest before being marked complete. Failed/cancelled runs do not add versions. Versions increase monotonically; only the latest 10 are retained, protecting the current version. History shows the saved homepage screenshot, time and request description—not historical source browsing or a separate historical Preview. Missing screenshots are explicitly labelled.

Select a record, then choose **恢复到此版本** and confirm. This replaces the actual workspace, installs locked dependencies at Runtime startup, typechecks/builds, and checks its homepage and source hash without repeating the dependency install or an intermediate Runtime restart before declaring success. Restore does not create another version; subsequent successful runs use the restored version as parent. Business databases, current private configuration, and visible chat history are **not rolled back**. Unversioned source changes are overwritten without creating a history backup. The old complete workspace is retained temporarily only for compensation and restart recovery. Ordinary preview refresh reloads only the iframe, without reacquiring the preview URL or refreshing version history; failed preview startup still retries the full access flow. Successful restore isolates the old development session so the next run starts from restored source.

Versions live in the project's sibling `versions/` directory, never mounted into Runtime. Snapshots use the same exclusions and 10,000-file/100-MiB limits as export. Homepage images are captured in the non-root Runtime with headless Chromium at fixed `http://127.0.0.1:3000/`, 1280×720, with a 10-second timeout and no platform cookies. Screenshot failure does not prevent saving source. Rebuild the Runtime image to enable screenshots; existing containers keep their old image until recreated. Screenshots are only historical homepage pictures, **not historical business data**.

Restore uses a persisted asynchronous operation and the project advisory lock, revision conflicts and idempotent submissions. While restoring, task submission, deletion, download/export, restart/deploy, automatic Preview startup and idle cleanup cannot mutate the project. Cancellation stays busy until its Runtime/process has stopped. Failed verification restores the old workspace/Preview; failed compensation keeps the project protected until service startup can retry. Startup compensates interrupted directory replacement before allowing writes. Project deletion also removes snapshots.

Authenticated private/no-store APIs: `GET /api/project/{id}/versions`, `GET /api/project/{id}/versions/{versionID}/thumbnail`, `POST /api/project/{id}/restore` (`version_id`, `expected_revision`, UUID `request_id`), and `GET /api/project/{id}/restore/{operationID}`. Restore acceptance returns HTTP 202 and `operation_id`; acceptance is not completion. The database migration is applied automatically at startup.

Backend tests run with `go test ./...`; optional real-PostgreSQL file/version API tests run with `ATOMS_FILES_TEST_DATABASE_URL` set to a **dedicated disposable test database**. Frontend checks use `cd web && npm test && npm run build`; the search tests use Node's TypeScript type stripping.

`TestVersionsRealRuntime` is opt-in with `ATOMS_VERSIONS_REAL_RUNTIME=1`. Run the compiled Linux test binary in a disposable control-plane container with the Docker socket, a dedicated `/data` volume, a private network with the test PostgreSQL host aliased `postgres`, and a disposable `atoms` database. Set `ATOMS_VERSIONS_TEST_DATABASE_URL`, `ATOMS_VERSIONS_TEST_IMAGE` to the newly built Runtime image, and `ATOMS_VERSIONS_TEST_VOLUME` / `ATOMS_VERSIONS_TEST_NETWORK` (both must begin `atoms-versions-test-`). It creates only its own project, verifies two successful versions, restores old/new and the initial template, and checks source hash, file API, actual homepage, secrets and a business-data sentinel. Set `ATOMS_VERSIONS_REQUIRE_SCREENSHOTS=1` to require real Chromium captures. It deliberately does **not** call normal startup's orphan-runtime sweep and uses no paid model. Do not point this harness at normal platform data.

For real-Runtime browser checks, also set `ATOMS_VERSIONS_BROWSER_FIXTURE=1`, mount built `web/dist` read-only at `/test-web`, and publish `127.0.0.1:19091:19091`. After automated checks the harness prints its `versions-fixture.localhost:19091` URL and fixture-only login, then serves for up to 15 minutes. The test Runtime uses isolated project port `19410`; keep that port free. `/__fixture` provides authenticated history-read failure/recovery and stop controls. Remove only the dedicated test containers/network/volume afterward, never existing user runtimes.

For manual browser checks without changing existing projects or using a paid model, set that test database URL and run `ATOMS_FILES_BROWSER_FIXTURE=1 go test ./internal/platform -run '^TestSourceBrowserFixture$' -v -timeout 20m`. The opt-in fixture serves the built frontend and real file APIs at `http://files-fixture.localhost:19081`, with a clearly labelled fake preview at port `19082`. Use this separate hostname to keep the normal app's login cookie unchanged. The test prints its fixture-only login; `/__fixture` provides controls for simulated work and file-list failure/recovery. The fixture stops after 15 minutes. It does not verify real model generation or a generated application's runtime.

## Point & Edit

In Project Workspace, choose **Inspect UI**, hover and click an element in Preview, then describe the desired change. The selected element’s rendered tag, role, text, classes, dimensions, and key style information are passed to the next run. P0 deliberately does not map DOM elements to source lines.

## Data removal

To remove all local data created by this demo, stop the stack and remove its named volumes:

```bash
docker compose down -v
```

Generated apps use the fixed Next.js App Router, TypeScript, Tailwind CSS, shadcn/ui-style starter components, Drizzle, PostgreSQL, pnpm, Vitest, and Testing Library stack. This platform does not expand that stack in P0.

A socket-free browser fixture is available via `TestPreviewGatewayBrowserFixture`: use an explicitly disposable fixture database and private Next container, mount the compiled test program and built frontend read-only, and publish only the local platform test port `19091`. It verifies real homepage/API traffic, anonymous/standalone preview denial, and simulated publication lifecycle without a Docker socket. Use the printed isolated fixture hostname/account. `/__fixture/stop` ends the fixture; clean up only its dedicated resources.
