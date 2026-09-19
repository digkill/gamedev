# Go backend

The API stores validated GameProject documents in PostgreSQL, creates immutable revisions, applies an atomic command set, signs users in by email, and builds complete games through a conveyor of AI agents.

Requirements: Go 1.27.1, PostgreSQL (target 18.6), Goose 3.x. `.env.example` contains local placeholders only. Keep real values in `.env` or environment variables; never commit `.env`.

## Running

`Dockerfile` sits next to the code it builds, so the build context is the
`backend` directory and nothing outside it is needed.

From the repository root:

```sh
docker compose -f deploy/compose/docker-compose.yml up -d --build
```

This starts PostgreSQL 18.6 on host port `55432` and the API on `18081` (host `8080` is often already taken). The API container runs Goose migrations on startup. A USB-connected phone reaches it with `adb reverse tcp:8080 tcp:18081` and app URL `http://127.0.0.1:8080`.

To run the API on the host instead, copy `.env.example` to `.env` and point `DATABASE_URL` at `localhost:55432`. Production requires HTTPS and PostgreSQL `sslmode=verify-full`. Run migrations once as a deployment job with a DDL-capable account; the API account should lack DDL rights.

Tests that need a database are skipped unless `TEST_DATABASE_URL` points at a migrated, disposable PostgreSQL:

```sh
TEST_DATABASE_URL="postgres://sandbox:change-me@localhost:55432/sandbox?sslmode=disable" go test ./...
```

## Identity

`POST /api/v1/auth/register` creates a pending account and emails a six-digit code; `POST /api/v1/auth/verify` confirms it and returns a session. A session is an opaque access token plus a refresh token — only SHA-256 digests are stored. Passwords are hashed with Argon2id (64 MiB, t=3, p=2) in the PHC string format, so the cost parameters can be raised later without invalidating existing hashes.

| Method | Path | Purpose |
| --- | --- | --- |
| POST | `/api/v1/auth/register` | Create an account, send a confirmation code |
| POST | `/api/v1/auth/verify` | Confirm the address, return a session |
| POST | `/api/v1/auth/resend` | Send the confirmation code again |
| POST | `/api/v1/auth/login` | Sign in with email and password |
| POST | `/api/v1/auth/login/code` | Send a one-time sign-in code |
| POST | `/api/v1/auth/login/code/verify` | Sign in with that code |
| POST | `/api/v1/auth/password/forgot` | Send a password-reset code |
| POST | `/api/v1/auth/password/reset` | Set a new password, end other sessions |
| POST | `/api/v1/auth/refresh` | Rotate the session |
| POST | `/api/v1/auth/logout` | Revoke this session, or all of them |
| GET/PATCH | `/api/v1/auth/me` | Read or rename the current account |

Refresh tokens rotate on every use. Replaying a rotated token revokes the whole family, which is the standard response to a stolen token. Sign-in, code requests, and code checks are rate limited per address and per client address; the counters live in the database, so a restart does not clear them. `X-Forwarded-For` is honoured only when `TRUST_PROXY_HEADERS=true`, because a direct client can otherwise forge it.

Without SMTP credentials the codes are written to the log instead of being sent, which keeps local sign-up working. `devseed` still issues a local token without an account:

```sh
docker compose -f deploy/compose/docker-compose.yml exec api /app/devseed
```

## Model provider

Model calls go through kie.ai when `KIE_API_KEY` is set. The primary model (`KIE_MODEL`, default `claude-opus-5`) is reached at `/claude/v1/messages` in the Anthropic Messages schema. When it is unavailable — out of credit, retired, rate limited, or erroring — the same request is repeated against `KIE_FALLBACK_MODEL` (default `gpt-5-6-terra`) at `/codex/v1/responses`, which speaks the Responses schema. Both use the same key.

Requests stream over Server-Sent Events. This is not an optimisation: the gateway closes a non-streaming request after roughly two minutes, and the level generation routinely takes longer. Set `KIE_TIMEOUT_SECONDS` for the overall budget. With `KIE_API_KEY` empty, a direct `OPENAI_API_KEY` is used instead; with neither, the AI endpoints answer `503 AI_UNAVAILABLE` and everything else keeps working.

## The agent conveyor

`POST /api/v1/games` takes a prompt and returns a job. `GET /api/v1/ai/jobs/{job_id}` reports the stage, and the finished job carries the project id. The work is asynchronous because a full run takes minutes.

Six agents run in order, and one deterministic stage sits in the middle of them:

| Stage | Who | What it produces |
| --- | --- | --- |
| architect | model | Scope: mode, genre, core loop, what was asked for that the engine cannot do |
| game_designer | model | Design: palette, player numbers, the role of each level |
| developer | model | The level spec: coordinates, sizes, patrol routes |
| compiler | Go | The GameProject document, with camera, colliders, HUD, and scene transitions |
| tester | model + Go | Playability: the automatic geometry checks are authoritative |
| reviewer | model | Whether what was built is what was asked for |
| auditor | model + Go | Platform rules and budgets; the deterministic screen cannot be overruled |

The split between the developer and the compiler is the reason the result is a game rather than a pile of JSON. A model is good at deciding that a level needs a moving platform over a pit and bad at emitting hundreds of correlated UUIDs, collision masks, and component fields without a mistake. So the model writes a compact spec, and Go turns it into the strict document: every solid gets its collider, every level gets a camera that follows the player, a win condition, a lose condition, a kill line below the level, and a HUD. A document that reaches storage is complete by construction.

The geometry checks are real, not decorative. From the player's own jump speed and the level's gravity the checker derives how far and how high the player can jump, then walks the platforms left to right and reports every gap that is too wide and every ledge that is too high. It also finds levels with no floor, win conditions with nothing to win, survival levels with no threat, and spawn points inside a wall. Fatal defects go back to the developer agent with their codes, up to `AI_MAX_REPAIRS` times. If the budget runs out, a deterministic repair builds the missing pieces — a floor, a reachable goal, stepping platforms across the gap — and the job reports `degraded: true` rather than shipping a level that cannot be finished. If the model provider is down entirely, a template game is compiled instead, also marked degraded.

`POST /api/v1/projects/{id}/ai/pipeline` runs the same conveyor over an existing project and stores the result as a new revision, so the history keeps both versions.

## Context

Every stage writes what it decided to `ai_context_entries`, filed under the job while it runs and under the project once it succeeds. The next request about that project reads those entries back into the prompts, so a later change knows the palette, the player numbers, and the design decisions of the earlier run instead of reinventing them. `GET /api/v1/projects/{id}/ai/context` returns the same material the agents read. `ai_job_steps` keeps the per-stage audit trail: which agent, which model, which provider, how long, and what it answered.

## Project documents

`CreateRequest.manifest` carries the complete GameProject JSON document. Its `manifest.project_id` becomes the database project ID, which allows offline-created projects to retain their identity.

The accepted subset has 1–20 scenes and no user scripts, graphs, prefabs, or external assets. Beyond the render-only components (`Transform`, `Sprite`, `Mesh`, `Camera`, `Light`) it accepts the gameplay layer: `Collider`, `Body`, `CharacterController`, `Health`, `Damage`, `Collectible`, `Goal`, `Patrol`, `Chase`, `Spawner`, `Timer`, `Tag`, `CameraFollow`, and `UIWidget`, plus per-scene `rules` that state how the level is won and lost. Every one of these is a closed enumeration with bounded numbers: a document that validates describes behaviour a trusted runtime can execute without sandboxing user code. Collider sizes are in the entity's local space — the visual primitive is one unit across and `Transform.scale` gives it its world size.

`engine/godot-host` still renders only `Transform`, `Sprite`, and `Mesh`. It ignores the gameplay components, so a generated game is stored and validated but not yet playable in the bundled host; the runtime work is tracked separately.

## HTTP surface

`GET /health/live`, `GET /health/ready`, the `/api/v1/auth/*` methods above, `POST|GET /api/v1/projects`, `GET|PATCH|DELETE /api/v1/projects/{id}`, `POST /api/v1/projects/{id}/restore`, `GET /api/v1/projects/{id}/revisions[/{revision_id}]`, `POST /api/v1/projects/{id}/changes`, `POST /api/v1/projects/{id}/ai/generations`, `POST /api/v1/games`, `POST /api/v1/projects/{id}/ai/pipeline`, `GET /api/v1/ai/jobs[/{job_id}]`, `POST /api/v1/ai/jobs/{job_id}/cancel`, and `GET /api/v1/projects/{id}/ai/context`.

All project and job methods need `Authorization: Bearer <token>`. Changing POST/PATCH/DELETE/restore/AI methods additionally need `Idempotency-Key`. DELETE needs `If-Match: <lock_version>`. A `409 REVISION_CONFLICT` keeps both copies and does not last-write-wins. The implemented surface is described in `contracts/openapi/openapi.yaml`.
