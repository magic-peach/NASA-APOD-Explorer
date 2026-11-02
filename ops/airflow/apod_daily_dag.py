"""Daily APOD pre-warm and archive.

Runs once a day, shortly after NASA publishes the new picture, and:

1. checks the app is ready (GET /readyz),
2. requests today's entry (GET /api/apod?date=...), which makes the app
   fetch it from NASA and cache it, so the first visitor of the day gets
   an instant response instead of waiting on NASA, and
3. archives the entry's metadata as JSON, one file per day.

Every task retries with exponential backoff, because NASA's API is often
slow or briefly unavailable.

Configuration (Airflow Variables, or environment variables of the same name):
    APOD_BASE_URL      the app's URL, e.g. https://apod.example.com
                       (default http://localhost:8080)
    APOD_ARCHIVE_DIR   where JSON files are written
                       (default /opt/airflow/data/apod)

See docs/INTEGRATIONS.md for how to run it.
"""

from __future__ import annotations

import json
import os
import urllib.error
import urllib.request
from datetime import timedelta
from pathlib import Path

import pendulum
from airflow.decorators import dag, task
from airflow.models import Variable

# NASA decides what "today" is in US Eastern time, as does the app.
APOD_TZ = "America/New_York"
HTTP_TIMEOUT_SECONDS = 30


def _setting(name: str, default: str) -> str:
    """An Airflow Variable, falling back to an env var, then a default."""
    return Variable.get(name, default_var=os.environ.get(name, default))


def _get_json(url: str):
    """GET url and decode JSON. Any HTTP or network error raises, which
    makes Airflow retry the task."""
    request = urllib.request.Request(url, headers={"Accept": "application/json"})
    try:
        with urllib.request.urlopen(request, timeout=HTTP_TIMEOUT_SECONDS) as response:
            return json.load(response)
    except urllib.error.HTTPError as err:
        # The app returns {"error": {"code", "message"}}; surface it in the log.
        body = err.read().decode("utf-8", "replace")[:300]
        raise RuntimeError(f"GET {url} failed with HTTP {err.code}: {body}") from err


@dag(
    dag_id="apod_daily",
    description="Pre-warm the app's cache with today's APOD and archive its metadata.",
    # 06:00 New York time: NASA has published the new picture by then.
    schedule="0 6 * * *",
    start_date=pendulum.datetime(2026, 1, 1, tz=APOD_TZ),
    catchup=False,  # don't backfill every day since start_date on first deploy
    max_active_runs=1,
    default_args={
        "retries": 4,
        "retry_delay": timedelta(minutes=2),
        "retry_exponential_backoff": True,  # 2, 4, 8, 16 minutes
        "max_retry_delay": timedelta(minutes=30),
    },
    tags=["apod"],
)
def apod_daily():
    @task
    def check_ready() -> str:
        """Fail fast (and retry) if the app or its database is down."""
        base_url = _setting("APOD_BASE_URL", "http://localhost:8080").rstrip("/")
        status = _get_json(f"{base_url}/readyz")
        if status.get("status") != "ready":
            raise RuntimeError(f"app not ready: {status}")
        return base_url

    @task
    def warm_cache(base_url: str, data_interval_end=None) -> dict:
        """Fetch the day's entry through the app, which caches it.

        The date comes from the run's data_interval_end in New York time, so
        a manual re-run for an earlier day warms that day, not today.
        """
        day = pendulum.instance(data_interval_end).in_timezone(APOD_TZ).to_date_string()
        entry = _get_json(f"{base_url}/api/apod?date={day}")
        if entry.get("placeholder"):
            # NASA's API sometimes returns its generic page instead of the
            # real entry. The app doesn't store those permanently; retrying
            # later gives NASA a chance to recover.
            raise RuntimeError(f"NASA returned a placeholder for {day}; will retry")
        return entry

    @task
    def archive(entry: dict) -> str:
        """Write the entry's metadata to APOD_ARCHIVE_DIR/<date>.json.

        Writing to a temporary file then renaming makes the write atomic: a
        crash never leaves a half-written file behind.
        """
        archive_dir = Path(_setting("APOD_ARCHIVE_DIR", "/opt/airflow/data/apod"))
        archive_dir.mkdir(parents=True, exist_ok=True)

        keep = ("date", "title", "explanation", "url", "hdurl", "media_type", "copyright", "thumbnail_url", "sample")
        record = {k: entry[k] for k in keep if k in entry}

        target = archive_dir / f"{record['date']}.json"
        tmp = target.with_suffix(".json.tmp")
        tmp.write_text(json.dumps(record, indent=2, ensure_ascii=False), encoding="utf-8")
        tmp.replace(target)
        return str(target)

    archive(warm_cache(check_ready()))


apod_daily()
