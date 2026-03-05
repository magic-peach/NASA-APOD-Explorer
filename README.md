# NASA APOD Explorer

NASA APOD Explorer is a full-stack web app that lets you browse NASA's **Astronomy Picture of the Day (APOD)** images with an infinite-scroll gallery, date and range search, favorites, and a compact space-facts dashboard.

The project uses a lightweight Go backend that serves both static frontend assets and JSON API endpoints. The backend also adds a simple in-memory cache to reduce repeated upstream API calls.

---

## Table of Contents

- [Features](#features)
- [Tech Stack](#tech-stack)
- [Project Structure](#project-structure)
- [How It Works](#how-it-works)
  - [Frontend Flow](#frontend-flow)
  - [Backend Flow](#backend-flow)
- [API Endpoints](#api-endpoints)
- [Getting Started](#getting-started)
  - [Prerequisites](#prerequisites)
  - [Installation](#installation)
  - [Run Locally](#run-locally)
- [Configuration](#configuration)
- [Usage Guide](#usage-guide)
- [Caching Behavior](#caching-behavior)
- [Error Handling](#error-handling)
- [Known Limitations](#known-limitations)
- [Future Improvements](#future-improvements)
- [Contributing](#contributing)
- [License](#license)

---

## Features

- 🌌 **Infinite APOD gallery** using randomized image batches.
- 📅 **Search by exact date** for a single APOD entry.
- 🗓️ **Search by date range** to browse APOD entries over time.
- ⭐ **Favorites support** stored in browser `localStorage`.
- 🌗 **Theme toggle** for quick light/dark preference switching.
- 🚀 **Space facts panel** showing aggregated metrics (planets, moons, average gravity, and average radius) from a solar-system dataset.
- ⚡ **In-memory backend cache** with 10-minute TTL for upstream API responses.

## Tech Stack

- **Backend:** Go (`net/http`) 
- **Frontend:** HTML, CSS, vanilla JavaScript
- **External APIs:**
  - NASA APOD API (`https://api.nasa.gov/planetary/apod`)
  - Solar System OpenData API (`https://api.le-systeme-solaire.net/rest/bodies/`)

## Project Structure

```text
.
├── go.mod
├── main.go               # Go HTTP server + API proxy + in-memory cache
└── static/
    ├── index.html        # Main UI
    ├── script.js         # Client-side behavior
    └── styles.css        # Styling
```

## How It Works

### Frontend Flow

1. On load, the app requests:
   - A first APOD image batch (`count=6`) for the gallery.
   - Aggregated space facts from `/api/space-facts`.
2. As the user scrolls near the bottom, more APOD images are fetched and appended.
3. Date and range inputs switch the app from infinite-scroll mode to targeted results.
4. Clicking a gallery card opens a modal with details and download/favorite actions.
5. Favorites are persisted in browser `localStorage` and can be rendered any time.

### Backend Flow

1. Serves static assets from `./static`.
2. Exposes API routes:
   - `/api/apod` for APOD passthrough queries.
   - `/api/space-facts` for computed solar-system metrics.
3. Uses a shared HTTP client timeout of 10 seconds.
4. Caches upstream payloads by full request URL for 10 minutes.
5. Adds permissive CORS headers for `GET` and `OPTIONS` requests.

## API Endpoints

### `GET /api/apod`

Proxy endpoint for APOD data.

Supported query params:

- `date=YYYY-MM-DD` → single APOD entry
- `start_date=YYYY-MM-DD&end_date=YYYY-MM-DD` → date-range APOD list
- `count=N` → random APOD entries

Examples:

```bash
curl "http://localhost:8080/api/apod?date=2024-01-01"
curl "http://localhost:8080/api/apod?start_date=2024-01-01&end_date=2024-01-07"
curl "http://localhost:8080/api/apod?count=6"
```

### `GET /api/space-facts`

Returns summary stats computed from solar-system bodies dataset.

Example response:

```json
{
  "planets": 8,
  "moons": 300,
  "avg_gravity": 8.57,
  "avg_radius": 2085.42
}
```

## Getting Started

### Prerequisites

- Go 1.22+
- Internet access (for upstream APIs)

### Installation

```bash
git clone <your-repo-url>
cd NASA-APOD-Explorer
```

### Run Locally

```bash
go run main.go
```

Then open:

- `http://localhost:8080`

## Configuration

The server reads the following environment variable:

- `NASA_API_KEY` (optional)
  - Default: `DEMO_KEY`
  - Recommended: set your own key for higher reliability/rate limits.

Examples:

```bash
export NASA_API_KEY="your_api_key_here"
go run main.go
```

Or one-liner:

```bash
NASA_API_KEY="your_api_key_here" go run main.go
```

## Usage Guide

- **Infinite browse:** Scroll to load more APOD images.
- **Search Date:** Select a single date and click **Search Date**.
- **Search Range:** Select both start and end dates, then click **Search Range**.
- **View details:** Click any card to open its modal and read full description.
- **Save favorite:** In modal, click **Save Favorite**.
- **Show favorites:** Click **⭐ Favorites** in the header.
- **Download image:** Use the modal **Download Image** link.
- **Switch theme:** Click **🌙 Theme**.

## Caching Behavior

- Cache key: complete upstream request URL.
- Cache store: in-memory map in the Go process.
- TTL: 10 minutes.
- Scope: per running server instance (not persisted across restarts).

This helps reduce external API calls and improves response speed for repeated queries.

## Error Handling

- Backend returns:
  - `405 Method Not Allowed` for unsupported HTTP methods.
  - `502 Bad Gateway` when upstream fetch/parse fails.
- Frontend fallback:
  - Space facts section shows `Could not load facts.` on fetch errors.

## Known Limitations

- Favorites can contain duplicates (no deduplication yet).
- Favorites are local to one browser/device (`localStorage`).
- The APOD API may return videos; gallery currently renders images only.
- Infinite scroll uses random APOD batches (`count`) and may include repeats.
- In-memory cache is not shared across multiple instances.

## Future Improvements

- Add favorites deduplication and delete actions.
- Add pagination controls and better loading states.
- Add request validation for date/range parameters.
- Add tests for handlers and utility functions.
- Add Docker support and deployment guide.
- Add robust logging/metrics and configurable cache TTL.

## Contributing

1. Fork the repo.
2. Create a feature branch.
3. Make your changes.
4. Run formatting/tests.
5. Open a pull request.

## License

No license file is currently included. Add a license (for example MIT) if you plan to distribute this project publicly.
