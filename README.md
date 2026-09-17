# Atoms

Atoms is a local-first AI app-builder demo. The intended P0 journey is: configure a Responses-compatible model, describe an app, let the platform create and run it, then iterate from a preview.

## Current implementation status

P0 is implemented: Compose platform, PostgreSQL, Go + chi API, React/Vite/Tailwind UI, authentication, encrypted Responses-compatible model configuration, up to two projects per user, isolated project database/runtime, persistent Codex CLI sessions, chat runs/SSE/cancellation, mandatory verification/repair, published project previews, idle recovery, and Point & Edit.

## Run the implemented phases

```bash
cp .env.example .env
# Replace both key placeholders and the PostgreSQL password with different
# `openssl rand -base64 32` values.
docker compose up --build
```

Open [http://localhost:8080](http://localhost:8080), register, and configure **Base URL**, **API Key**, and **Model** in Settings. The connection test performs a small request to the provider's `/responses` endpoint; it therefore requires an OpenAI Responses API-compatible service.

Creating a project initializes `/data/users/{user_id}/projects/{project_id}/` with `workspace`, `codex`, and `logs` directories; creates a dedicated PostgreSQL schema, role, and encrypted password; and assigns a stable host port from that user's reserved range. The workspace preview embeds that same `http://host:assigned-port` application endpoint used by a new browser tab. Subsequent requests resume the same Codex session from the persistent `codex` directory. Each run is independently verified with `pnpm typecheck`, `pnpm test`, `pnpm build`, a clean preview restart, and an HTTP smoke test; failed checks receive up to two repair rounds.

Project naming requests use an API JSON schema, disable reasoning when supported, and use an 8-second total timeout. Chinese and English names are supported; local name validation checks the 1–16 character limit and single-line format. Invalid, incomplete, refused, or failed naming responses fall back to `未命名项目` so creation can continue; the name can be edited later. A saved valid model configuration is still required.

The runtime image downloads and installs starter dependencies during its build. New projects with unchanged starter dependency files copy these preinstalled dependencies before starting; customized projects install normally with the image's pnpm cache. The store path is explicitly fixed so mounted project volumes reuse that cache instead of downloading the starter packages again. New dependencies can still require network downloads.

To prepare this cache on an existing server after updating the code, run `docker compose build runtime-image` before creating another project. The image build pays the download cost once and Docker caches that layer for subsequent builds. Newly created runtime containers use the updated image; already running containers continue with their existing image.

PostgreSQL is intentionally not exposed to the host. For local inspection use `docker compose exec postgres psql -U atoms -d atoms`.

## Architecture constraints

The authoritative product and architecture constraints are in [DECISIONS.md](DECISIONS.md). Platform services are `atoms-app` and `postgres` on the internal `atoms-internal` network. The control plane uses port `8080`; each generated application also publishes its assigned project port. API keys are encrypted at rest, passwords use Argon2id, and authentication is an HttpOnly, SameSite=Lax signed host-only cookie that works with localhost, a server IP, or a domain. HTTPS requests, including those forwarded with `X-Forwarded-Proto: https`, receive `Secure` cookies.

For a Tencent Cloud host, keep PostgreSQL private and allow inbound TCP `8080` plus the project-port capacity beginning at `DEPLOY_PORT_BASE`. Keep `DEPLOY_PORT_SPAN` at least `2`; each registered user reserves that many consecutive ports. Use persistent, distinct values for `APP_MASTER_KEY`, `APP_SESSION_KEY`, and `POSTGRES_PASSWORD`—changing them after deployment invalidates encrypted model keys, sessions, or database authentication.

The Compose configuration mounts the Docker socket into `atoms-app`. This is a deliberate local-demo trade-off for Phase 2 runtime management and grants the container elevated host Docker authority. It is not a production deployment model.

## Source files and export

In Project Workspace, the right pane defaults to **预览** (Preview). Choose **文件** (Files) to browse the collapsible file tree and read source with line numbers. You can hide/show the entire tree and search file names or relative paths (case-insensitive, not file contents). Search reveals matching paths automatically; clearing it restores previous directory folds. Switching tabs preserves the preview iframe, file selection, tree visibility, and search. Files are read directly from the same persistent workspace mounted by Preview; viewing does not require a running project container. During development you can refresh to see changing files, and the file panel refreshes automatically when work ends.

The panel is read-only. **下载文件** downloads the selected file; **导出项目** exports source, static resources, dependency manifests, and lockfiles as a ZIP archive. Downloads and exports are disabled during active work; the server also checks under the same project lock as run creation/deletion and prepares a complete temporary snapshot before releasing the lock. Source access requires project ownership and is not cached.

All file operations exclude dependencies (`node_modules`), generated build directories (`.next`, `dist`, `build`, `out`), coverage/caches, Git/development-session state, logs, environment files, package-registry credentials, and private-key files. `.env.example`, `.env.sample`, and `.env.template` remain included; these templates must not contain real credentials. Symbolic links and non-regular files cannot be viewed or exported. Filtering known sensitive paths is not a secret scanner: do not put credentials into ordinary source files or environment templates.

Online viewing is limited to UTF-8 text up to 1 MiB. Binary/larger files show metadata and can be downloaded while idle. The file list is capped at 20,000 entries; ZIP export is capped at 10,000 files and 100 MiB of uncompressed content; a single-file download is capped at 100 MiB. Limit failures do not produce partial downloads. This release does not include source editing, version history, version switching, or rollback.

Backend tests run with `go test ./...`; optional real-PostgreSQL file API tests run with `ATOMS_FILES_TEST_DATABASE_URL` set to a **dedicated disposable test database**. Frontend checks use `cd web && npm test && npm run build`; the search tests use Node's TypeScript type stripping.

For manual browser checks without changing existing projects or using a paid model, set that test database URL and run `ATOMS_FILES_BROWSER_FIXTURE=1 go test ./internal/platform -run '^TestSourceBrowserFixture$' -v -timeout 20m`. The opt-in fixture serves the built frontend and real file APIs at `http://files-fixture.localhost:19081`, with a clearly labelled fake preview at port `19082`. Use this separate hostname to keep the normal app's login cookie unchanged. The test prints its fixture-only login; `/__fixture` provides controls for simulated work and file-list failure/recovery. The fixture stops after 15 minutes. It does not verify real model generation or a generated application's runtime.

## Point & Edit

In Project Workspace, choose **Inspect UI**, hover and click an element in Preview, then describe the desired change. The selected element’s rendered tag, role, text, classes, dimensions, and key style information are passed to the next run. P0 deliberately does not map DOM elements to source lines.

## Data removal

To remove all local data created by this demo, stop the stack and remove its named volumes:

```bash
docker compose down -v
```

Generated apps use the fixed Next.js App Router, TypeScript, Tailwind CSS, shadcn/ui-style starter components, Drizzle, PostgreSQL, pnpm, Vitest, and Testing Library stack. This platform does not expand that stack in P0.
