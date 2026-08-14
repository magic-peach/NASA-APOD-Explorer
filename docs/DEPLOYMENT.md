# Deployment

This guide covers getting the secrets, running locally (directly and with Docker), and deploying to Fly.io by hand or from CI.

## 1. Get the keys and secrets

| Variable | Required | Where it comes from |
|---|---|---|
| `NASA_API_KEY` | Recommended | Free from <https://api.nasa.gov> (fill in the form; the key is emailed and shown immediately). Without it the app uses NASA's shared `DEMO_KEY`, which is heavily rate-limited. |
| `SESSION_SECRET` | Yes, in production | **You generate it yourself.** See below. |
| `SOLAR_SYSTEM_API_KEY` | Optional | Free from <https://api.le-systeme-solaire.net/generatekey.html>. Without it the "solar system in numbers" panel says the figures are unavailable. |
| `OPENAI_API_KEY` | Optional | Enables live answers for the explanation tools; without it they give built-in answers. |

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

While NASA's APOD API is returning placeholder data (see the README), use demo mode to see real images:

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

## 4. Deploy to Fly.io (manual, first time)

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

**Demo mode.** `fly.toml` currently sets `DEMO_MODE = "1"` because of NASA's upstream problem. Once NASA's API returns real entries again, set it to `"0"` and redeploy.

**Backups.** Fly takes daily snapshots of volumes automatically:

```sh
fly volumes list --app <your-app-name>
fly volumes snapshots list <volume-id>
```

## 5. Deploy automatically from GitHub Actions

`.github/workflows/ci.yml` runs on every push and pull request:

| Job | What it does |
|---|---|
| Lint | `gofmt` check, `go vet`, `golangci-lint` |
| Test | `go test -race` with coverage; the summary is shown on the run page and `coverage.out` is uploaded as an artifact |
| Docker build | Builds the image (not pushed), with layer caching |
| Deploy | Only on pushes to `main`, only after the three jobs above pass. Runs `flyctl deploy --remote-only` |

To turn on automatic deploys, give GitHub a Fly deploy token:

```sh
# A token that can only deploy this one app.
fly tokens create deploy --app <your-app-name>

# Store it as a repository secret (paste the token when prompted).
gh secret set FLY_API_TOKEN
```

Until `FLY_API_TOKEN` exists, the deploy job skips itself with a note instead of failing.

The deploy job uses a GitHub environment called `production`. In **Settings → Environments → production** you can require a manual approval before each deploy.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| Everyone gets logged out after a deploy | `SESSION_SECRET` isn't set as a Fly secret, so a random one is generated on each start. |
| Login works locally over plain HTTP on another device, but the cookie never sticks | Session cookies are `Secure`. Browsers allow that on `http://localhost` only. Use HTTPS, or set `COOKIE_SECURE=false` for that test. |
| Every picture shows the NASA logo | NASA's API is returning placeholder entries. Use `DEMO_MODE=1` until it's fixed. |
| `/readyz` returns 503 | The database can't be opened. Check the volume is mounted at `/data` (`fly volumes list`). |
| 429 Too Many Requests | The per-IP rate limit (`RATE_LIMIT_RPS`, `RATE_LIMIT_BURST`). |
