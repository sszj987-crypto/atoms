# Atoms

Atoms is a local-first AI app-builder demo. The intended P0 journey is: configure a Responses-compatible model, describe an app, let the platform create and run it, then iterate from a preview.

## Current implementation status

P0 is implemented: Compose platform, PostgreSQL, Go + chi API, React/Vite/Tailwind UI, authentication, encrypted Responses-compatible model configuration, one project per user, isolated project database/runtime, persistent Codex CLI sessions, chat runs/SSE/cancellation, mandatory verification/repair, preview proxying, idle recovery, and Point & Edit.

## Run the implemented phases

```bash
cp .env.example .env
# Replace both key placeholders and the PostgreSQL password with different
# `openssl rand -base64 32` values.
docker compose up --build
```

Open [http://localhost:8080](http://localhost:8080), register, and configure **Base URL**, **API Key**, and **Model** in Settings. The connection test performs a small request to the provider's `/responses` endpoint; it therefore requires an OpenAI Responses API-compatible service.

Creating the first project initializes `/data/users/{user_id}/projects/{project_id}/` with `workspace`, `codex`, and `logs` directories; creates a dedicated PostgreSQL schema, role, and encrypted password; and serves the starter app through `p-{project_id}.localhost:8080`. Subsequent requests resume the same Codex session from the persistent `codex` directory. Each run is independently verified with `pnpm typecheck`, `pnpm test`, `pnpm build`, and an HTTP smoke test; failed checks receive up to two repair rounds.

PostgreSQL is intentionally not exposed to the host. For local inspection use `docker compose exec postgres psql -U atoms -d atoms`.

## Architecture constraints

The authoritative product and architecture constraints are in [DECISIONS.md](DECISIONS.md). Platform services are `atoms-app` and `postgres` on the internal `atoms-internal` network; only platform port `8080` is exposed. API keys are encrypted at rest, passwords use Argon2id, and authentication is an HttpOnly, SameSite=Lax signed cookie.

The Compose configuration mounts the Docker socket into `atoms-app`. This is a deliberate local-demo trade-off for Phase 2 runtime management and grants the container elevated host Docker authority. It is not a production deployment model.

## Point & Edit

In Project Workspace, choose **Inspect UI**, hover and click an element in Preview, then describe the desired change. The selected element’s rendered tag, role, text, classes, dimensions, and key style information are passed to the next run. P0 deliberately does not map DOM elements to source lines.

## Data removal

To remove all local data created by this demo, stop the stack and remove its named volumes:

```bash
docker compose down -v
```

Generated apps use the fixed Next.js App Router, TypeScript, Tailwind CSS, shadcn/ui-style starter components, Drizzle, PostgreSQL, pnpm, Vitest, and Testing Library stack. This platform does not expand that stack in P0.
