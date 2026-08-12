# Optional integrations

None of these are needed to run the app. Each one shows how the app fits into a common piece of team tooling, and each is small enough to read in a few minutes.

| File | What it is | Why you might want it |
|---|---|---|
| [`Jenkinsfile`](../Jenkinsfile) | The CI pipeline for Jenkins | Your team runs Jenkins rather than GitHub Actions |
| [`ops/airflow/apod_daily_dag.py`](../ops/airflow/apod_daily_dag.py) | A daily Airflow DAG | Pre-warm today's picture and keep a JSON archive |
| [`ops/n8n/daily-apod-digest.json`](../ops/n8n/daily-apod-digest.json) | An importable n8n workflow | Get today's picture in Discord or by email every morning |

## Jenkins pipeline

**What it does.** The same three gates as `.github/workflows/ci.yml`, in order:

1. **Lint:** `gofmt` check, `go vet`, `golangci-lint`, inside the official `golangci/golangci-lint` image.
2. **Test:** `go test -race` with coverage, inside `golang:1.24`. It's the Debian-based image because the race detector needs gcc. `coverage.out` is archived on the build.
3. **Build image:** `docker build`; the image is removed again afterwards so agents don't fill up.

Builds time out after 20 minutes, never run concurrently on the same branch, and only the last 20 are kept.

**Why.** Teams standardised on Jenkins can adopt the project without translating the workflow by hand. Running every step inside a pinned container image means the agent doesn't need Go installed, and the versions match the GitHub workflow.

Deploying is deliberately left out: production deploys belong to one system, and here that's GitHub Actions.

**How to run it.**

1. You need a Jenkins agent with Docker, plus the **Docker Pipeline** and **Pipeline** plugins.
2. Create a **Multibranch Pipeline** (or Pipeline) job pointing at this repository. Jenkins finds the `Jenkinsfile` at the repo root.
3. Run the job. Each stage appears in the stage view, and `coverage.out` is attached to the build.

## Airflow: daily cache warm-up and archive

**What it does.** Every day at 06:00 New York time (after NASA publishes the new picture), the `apod_daily` DAG runs three tasks:

```
check_ready  ->  warm_cache  ->  archive
GET /readyz      GET /api/apod     write <date>.json
                 ?date=<day>
```

1. **`check_ready`** fails fast if the app or its database is down.
2. **`warm_cache`** asks the app for the day's entry. The app fetches it from NASA once, and the singleflight-protected cache plus the permanent SQLite copy mean every visitor afterwards is served instantly. If NASA returns its placeholder page, the task fails on purpose, so Airflow retries later.
3. **`archive`** writes the entry's metadata to `APOD_ARCHIVE_DIR/<date>.json`, via a temporary file and a rename so a crash never leaves half a file.

Every task retries 4 times with exponential backoff (2, 4, 8, 16 minutes), because NASA's API is often slow. `catchup=False` stops the first deploy from backfilling every day since the start date. A manual run for an earlier interval warms that day instead of today.

**Why.** Without it, the first visitor each day waits on NASA, which can take more than 10 seconds. It also builds an independent archive of entries that doesn't depend on NASA keeping its API working, which matters given the current placeholder problem.

**How to run it.**

1. Copy the DAG into your Airflow `dags/` folder. It only uses the standard library plus `pendulum`, which ships with Airflow, so there's nothing to `pip install`:
   ```sh
   cp ops/airflow/apod_daily_dag.py "$AIRFLOW_HOME/dags/"
   ```
2. Point it at the app, using Airflow Variables or environment variables of the same name:
   ```sh
   airflow variables set APOD_BASE_URL https://<your-app-name>.fly.dev
   airflow variables set APOD_ARCHIVE_DIR /opt/airflow/data/apod
   ```
3. Unpause it in the UI, or try one run from the command line:
   ```sh
   airflow dags unpause apod_daily
   airflow dags test apod_daily 2026-11-02
   ```

## n8n: daily digest to Discord or email

**What it does.** Every morning at 08:00, the workflow runs these nodes in order:

1. **Every morning at 08:00:** a schedule trigger.
2. **Settings (edit me):** the app URL and your Discord webhook URL.
3. **Get today's APOD:** fetches `/api/apod`, retried 3 times.
4. **Real picture?:** skips the day if NASA sent a placeholder.
5. **Post to Discord** and **Send email:** both run on real pictures.

- **Discord:** posts an embed with the title, the first 1,000 characters of the explanation, the image (or the video's poster), the credit, and a link back to the app's deep link (`/?date=...`).
- **Email:** sends the same as an HTML message. Title and explanation are stripped of `<`, `>` and `&` so an entry can't inject markup. This node ships **disabled** because it needs SMTP credentials.

**Why.** It's a no-code way to share the daily picture with a team or community channel. It also shows the API working as a plain JSON service, outside the browser UI.

**How to run it.**

1. In n8n, go to **Workflows → Import from File** and choose `ops/n8n/daily-apod-digest.json`.
2. Open **Settings (edit me)** and set:
   - `apodBaseUrl`: your app's URL, for example `https://<your-app-name>.fly.dev`
   - `discordWebhookUrl`: from Discord, **Channel settings → Integrations → Webhooks → New webhook → Copy URL**. Treat it as a secret, since anyone with it can post to the channel.
3. For email, open **Send email**, add SMTP credentials, set the from and to addresses, and enable the node.
4. Click **Execute workflow** to test, then switch the workflow to **Active**.

## How these were checked

- **Jenkinsfile:** not run (there's no Jenkins locally). It mirrors the GitHub workflow step for step, using the same images and versions.
- **Airflow DAG:** compiled. Its three tasks were executed against a running instance of the app (in demo mode) with Airflow's decorators stubbed out. That covers the readiness check, warming the right New York date, writing the archive file, and failing on placeholders so Airflow retries. It hasn't been run inside a real Airflow scheduler.
- **n8n workflow:** validated as JSON with all connections pointing at real nodes. The Discord and email expressions were evaluated against real API responses (an image, a video, and an entry containing `<script>`). It hasn't been imported into a live n8n instance, so check the nodes after importing on your version.
