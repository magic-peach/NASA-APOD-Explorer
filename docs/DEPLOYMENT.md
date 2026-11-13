# Deployment

This guide covers getting the secrets, running locally (directly and with Docker), and deploying to Vercel (with a Turso database) by hand or from CI. The Docker image still runs anywhere a container does; section 6 keeps the Fly.io setup for reference.

## 1. Get the keys and secrets

| Variable | Required | Where it comes from |
|---|---|---|
| `NASA_API_KEY` | Recommended | Free from <https://api.nasa.gov> (fill in the form; the key is emailed and shown immediately). Without it the app uses NASA's shared `DEMO_KEY`, which is heavily rate-limited. |
| `SESSION_SECRET` | Yes, in production | **You generate it yourself.** See below. |
| `SOLAR_SYSTEM_API_KEY` | Optional | Free from <https://api.le-systeme-solaire.net/generatekey.html>. Without it the "solar system in numbers" panel says the figures are unavailable. |
| `OPENAI_API_KEY` | Optional | Enables live answers for the explanation tools; without it they give built-in answers. |
| `DB_PATH` + `DB_AUTH_TOKEN` | Yes, on Vercel | The Turso database URL (`libsql://...`) and a token for it. See section 4. Locally `DB_PATH` is just a SQLite file. |

### `SESSION_SECRET`

This is the key the server uses to HMAC session tokens before storing them. It isn't issued by any service: it's just a long random string that only your server knows. The app requires at least 32 characters.

```sh
openssl rand -base64 48
```

That prints 64 random characters. Rules:

- Use a **different** secret for local development and production.
- Treat it like a password: never commit it or paste it into chat or issues.
- Keep it **stable**. If it changes, every existing login becomes invalid and everyone has to log in again. That's also how to force-logout all users if the secret ever leaks: set a new one.
- If it's unset, the server generates a random one at startup and logs a warning. That's fine for a quick local run, but each restart logs everyone out.

## 2. Run locally

The app reads plain environment variables; it does not load `.env` files by itself.

```sh
cp .env.example .env          # .env is git-ignored; fill in your values
set -a; source .env; set +a   # export everything in .env into this shell
make run                      # or: go run ./cmd/server
```

Open <http://localhost:8080>.

To run without calling NASA at all (offline, or for screenshots), use demo mode:

```sh
make demo                     # DEMO_MODE=1 go run ./cmd/server
```

## 3. Run with Docker

```sh
make docker-build             # docker build -t nasa-apod-explorer:local .
make docker-run               # runs it with your .env and a volume for SQLite
```

Or with Compose, which also keeps the database in a named volume:

```sh
docker compose up --build
docker compose down           # stop (the apod-data volume is kept)
docker compose down -v        # stop and delete the database volume
```

The image is about 21 MB:

- **Build:** a static binary (`CGO_ENABLED=0`; the SQLite driver is pure Go).
- **Runtime:** `gcr.io/distroless/static-debian12:nonroot`, which has no shell or package manager and runs as a non-root user.
- **Health check:** the binary checks its own `/healthz` (`/app/server healthcheck`).
- **Stopping:** `docker stop` sends SIGTERM, and the server drains in-flight requests before exiting.

## 4. Deploy to Vercel + Turso (manual, first time)

### Why the database moved

Vercel runs the app as a **serverless function**: an instance starts on demand, may be frozen or thrown away between requests, and several can run at once. Its disk is temporary and private to one instance, so a SQLite file there would lose accounts and favourites at random. The data needs to live outside the function.

[Turso](https://turso.tech) hosts libSQL, a fork of SQLite reached over HTTP. It uses the same SQL dialect, so every query and migration runs unchanged; only `store.Open` picks a different driver when `DB_PATH` is a `libsql://` URL (`internal/store/store.go`). Locally and in tests the app still uses a plain SQLite file.

### How the code runs on Vercel

- Vercel's Go preset runs the normal server binary: `vercel.json` builds `./cmd/server` into `$VERCEL_OUTPUT_FILE`, and Vercel routes every path to it with the original URL, so there is no separate handler or rewrite. The server listens on the `PORT` Vercel gives it.
- `regions: ["bom1"]` runs the function in Mumbai, next to the Turso database (`aws-ap-south-1`). Every request makes a few database round trips, so keeping the two in one region matters more than anything else for latency. Vercel's default, Washington, would add an ocean crossing to each one.
- Instances start on demand, can be paused between requests and several can run at once. That is why the data lives in Turso and not on disk.
- When `VERCEL=1` (set by Vercel), the server refuses to start without `SESSION_SECRET`, because each instance would invent its own and reject the others' logins, or with a local `DB_PATH`.
- The in-memory APOD cache and rate limiter are per instance. The persistent APOD cache in Turso is shared, so NASA is still called at most once per date.

### Steps

```sh
# 1. Turso: create the database and a token (CLI: https://docs.turso.tech/cli/installation,
#    or use the dashboard at https://app.turso.tech).
turso auth login
turso db create apod-explorer --location aws-ap-south-1   # Mumbai
turso db show apod-explorer --url          # libsql://apod-explorer-<you>.turso.io
turso db tokens create apod-explorer       # prints the token once

# 2. Vercel: log in and link this directory to a new project.
vercel login
vercel link

# 3. Set the environment variables (each command prompts for the value,
#    so secrets never appear in shell history).
vercel env add DB_PATH production
vercel env add DB_AUTH_TOKEN production
vercel env add SESSION_SECRET production          # openssl rand -base64 48
vercel env add NASA_API_KEY production
vercel env add TRUSTED_PROXY_HEADER production    # X-Real-IP
vercel env add LOG_FORMAT production              # json

# 4. Deploy and check.
vercel deploy --prod
curl https://<your-project>.vercel.app/readyz
```

The migrations run on the first request against the new database.

**Rate limiting** reads the client IP from `X-Real-IP`, which Vercel's edge sets itself and doesn't take from the client, never from the spoofable `X-Forwarded-For`.

## 5. Deploy automatically from GitHub Actions

`.github/workflows/ci.yml` runs on every push and pull request:

| Job | What it does |
|---|---|
| Lint | `gofmt` check, `go vet`, `golangci-lint` |
| Test | `go test -race` with coverage; the summary is shown on the run page and `coverage.out` is uploaded as an artifact |
| Docker build | Builds the image (not pushed), with layer caching |
| Deploy | Only on pushes to `main`, only after the three jobs above pass. Runs `vercel deploy --prod` |

`vercel.json` sets `git.deploymentEnabled: false`, so Vercel doesn't deploy on its own when you push. The CI job is the only path to production, and it only runs once the checks are green.

To turn on automatic deploys, give GitHub three secrets:

```sh
# A token from https://vercel.com/account/tokens
gh secret set VERCEL_TOKEN
# Both IDs are in .vercel/project.json after `vercel link`.
gh secret set VERCEL_ORG_ID
gh secret set VERCEL_PROJECT_ID
```

Until they exist, the deploy job skips itself with a note instead of failing.

The deploy job uses a GitHub environment called `production`. In **Settings → Environments → production** you can require a manual approval before each deploy.

## 6. Fly.io (previous host, kept for reference)

The app ran on Fly.io until the free trial ended. Nothing here is needed for Vercel; it still works if you'd rather run the container.

Install `flyctl` (<https://fly.io/docs/flyctl/install/>), then:

```sh
# 1. Log in (opens a browser)
fly auth login

# 2. Create the app. The name must be globally unique; put the same name
#    in the `app = "..."` line of fly.toml.
fly apps create <your-app-name>

# 3. Create the 1 GB volume that holds the SQLite database, in the same
#    region as primary_region in fly.toml (`fly platform regions` lists them).
fly volumes create apod_data --region sin --size 1 --app <your-app-name>

# 4. Set the secrets (they're encrypted and never shown again).
fly secrets set --app <your-app-name> \
  NASA_API_KEY=<your-nasa-key> \
  SESSION_SECRET="$(openssl rand -base64 48)" \
  SOLAR_SYSTEM_API_KEY=<your-solar-system-key>

# 5. Deploy. The image is built on Fly's builders.
fly deploy --remote-only

# 6. Check it
fly status --app <your-app-name>
fly logs --app <your-app-name>
curl https://<your-app-name>.fly.dev/healthz
```

What `fly.toml` sets up:

- **HTTPS only**, with Fly's proxy checking `GET /healthz` every 15s.
- **Rate limiting** reads the real client IP from `Fly-Client-IP` (`TRUSTED_PROXY_HEADER`), never from the spoofable `X-Forwarded-For`.
- **Sleeps when idle** and wakes on the next request (`auto_stop_machines`), which keeps it within the free allowance.
- **The database** lives on the `apod_data` volume at `/data/app.db`.

**Run exactly one machine.** A Fly volume is attached to a single machine, and SQLite is a single-writer database, so don't run `fly scale count` above 1. Scaling out would mean moving to a networked database (for example Postgres) or replicating SQLite with LiteFS.

**Demo mode.** `fly.toml` sets `DEMO_MODE = "1"` because NASA's API currently returns placeholder entries for past days and HTTP 500 for today. Once it returns real pictures again, set it to `"0"` and push to `main` (CI deploys it); the app then calls NASA with the `NASA_API_KEY` secret.

**Backups.** Fly takes daily snapshots of volumes automatically:

```sh
fly volumes list --app <your-app-name>
fly volumes snapshots list <volume-id>
```

To deploy to Fly from CI instead of Vercel, swap the `deploy` job in `ci.yml` back to `flyctl deploy --remote-only` with a `FLY_API_TOKEN` secret (`fly tokens create deploy`).

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Every request returns 503 on Vercel | Startup failed. `vercel logs` shows why: usually `SESSION_SECRET` or `DB_PATH` is missing, or the Turso token is wrong. |
| Everyone gets logged out after a deploy | `SESSION_SECRET` changed, or (outside Vercel) isn't set, so a random one is generated on each start. |
| Login works locally over plain HTTP on another device, but the cookie never sticks | Session cookies are `Secure`. Browsers allow that on `http://localhost` only. Use HTTPS, or set `COOKIE_SECURE=false` for that test. |
| Every picture shows the NASA logo | The science.nasa.gov feed is down and the app fell back to `api.nasa.gov`, which returns placeholders. It recovers on its own; `DEMO_MODE=1` shows sample images meanwhile. |
| `/readyz` returns 503 | The database can't be reached. On Vercel, check `DB_PATH` and `DB_AUTH_TOKEN`; in Docker, check the volume is mounted at `/data`. |
| 429 Too Many Requests | The per-IP rate limit (`RATE_LIMIT_RPS`, `RATE_LIMIT_BURST`). |
