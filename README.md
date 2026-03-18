# GEMTenders

A high-performance tender discovery platform for India's **Government e-Marketplace (GeM)**. Scrapes, indexes, and serves 42,000+ active bids with full-text search, PDF management, and a real-time analytics dashboard.

## About

GEMTenders automates the process of discovering and tracking government tenders from [bidplus.gem.gov.in](https://bidplus.gem.gov.in). It runs parallel scrapers to collect bid listings, downloads associated PDF documents, tracks corrigendum amendments, and provides a modern web interface to search and analyze the data.

### Key Features

- **Parallel Scraping** — 5 staggered scrapers with 100 workers each, fetching bids at up to 250 concurrent requests
- **Full-Text Search** — SQLite FTS5-powered search with BM25 relevance ranking across bid numbers, categories, ministries, and departments
- **PDF Management** — Automated download and serving of bid PDFs and corrigendum documents with retry logic
- **Corrigendum Tracking** — Detects amendments, extracts updated end dates, and links to corrigendum PDFs
- **Analytics Dashboard** — Real-time charts for tender pipeline, department/category breakdown, and 30-day timeline
- **Live Progress** — Server-Sent Events stream scrape/download progress to the dashboard in real time
- **WAF Bypass** — Playwright-based session bootstrap with browser-like HTTP transport for reliable access

### Tech Stack

| Component | Technology |
|-----------|-----------|
| Language | Go 1.25 |
| Web Framework | Gin |
| Database | SQLite3 (FTS5, WAL mode) |
| Browser Automation | Playwright (Chromium, headless) |
| Frontend | Tailwind CSS, HTMX 2.0, Chart.js |

## Prerequisites

- **Go 1.25+** with CGO enabled (required for SQLite)
- **GCC** — C compiler for `go-sqlite3`
- **Chromium dependencies** — auto-installed by Playwright on first run (~130 MB)
  - On Linux: `libnss3`, `libatk1.0-0`, `libatk-bridge2.0-0`, `libcups2`, `libdrm2`, `libxkbcommon0`, `libgbm1`

## Build

```bash
CGO_ENABLED=1 go build -tags "fts5" -o gemscraper .
```

This produces a single ~25 MB binary.

## Usage

GEMTenders is operated through CLI subcommands. A typical first run follows this sequence:

### 1. Scrape Bids

Fetches all active bid listings from the GeM API and stores them in SQLite.

```bash
./gemscraper scrape [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-db` | `gems.db` | SQLite database path |
| `-sessions` | `3` | Browser sessions to bootstrap |
| `-scrapers` | `5` | Parallel scraper instances |
| `-stagger` | `30` | Seconds between scraper launches |
| `-workers` | `100` | Workers per scraper |
| `-rps` | `50` | Requests per second per scraper |

### 2. Download PDFs

Downloads bid PDF documents for all scraped bids.

```bash
./gemscraper download [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-db` | `gems.db` | SQLite database path |
| `-dir` | `downloads` | PDF output directory |
| `-workers` | `100` | Download goroutines |
| `-rps` | `50` | Download rate limit |
| `-retries` | `5` | Max retries per file |

### 3. Reindex Search

Rebuilds the full-text search index. Required after the initial scrape.

```bash
./gemscraper reindex -db gems.db
```

### 4. Check Status

Shows a summary of the database: total bids, PDFs downloaded, pending, and corrigendum stats.

```bash
./gemscraper status -db gems.db
```

### 5. Start Web Server

Launches the web UI for search, tender details, and the analytics dashboard.

```bash
./gemscraper serve [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `-db` | `gems.db` | SQLite database path |
| `-downloads` | `downloads` | PDF directory to serve |
| `-addr` | `:28080` | Listen address |
| `-sessions` | `3` | Sessions for background scrapes via UI |

Open `http://localhost:28080` in your browser.

## Deployment

### Quick Start

```bash
# Build
CGO_ENABLED=1 go build -tags "fts5" -o gemscraper .

# Initial data collection
./gemscraper scrape
./gemscraper download
./gemscraper reindex

# Start the web UI
./gemscraper serve
```

### Recurring Scrapes (Cron)

Set up periodic scraping and downloading to keep data fresh:

```cron
# Scrape every 6 hours
0 */6 * * * cd /path/to/GEMTenders && ./gemscraper scrape >> scrape.log 2>&1

# Download new PDFs 15 minutes after each scrape
15 */6 * * * cd /path/to/GEMTenders && ./gemscraper download >> download.log 2>&1
```

### Systemd Service

Create `/etc/systemd/system/gemtenders.service`:

```ini
[Unit]
Description=GEMTenders Web Server
After=network.target

[Service]
Type=simple
User=gemtenders
WorkingDirectory=/opt/GEMTenders
ExecStart=/opt/GEMTenders/gemscraper serve -addr :28080
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl enable --now gemtenders
```

### Reverse Proxy (Nginx)

```nginx
server {
    listen 80;
    server_name tenders.example.com;

    location / {
        proxy_pass http://127.0.0.1:28080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_buffering off;                    # Required for SSE
    }
}
```

### Disk Requirements

| Component | Size |
|-----------|------|
| Binary | ~25 MB |
| SQLite database | ~50 MB |
| Bid PDFs | 15–20 GB |
| Corrigendum PDFs | ~500 MB |
| Chromium cache | ~130 MB |
| **Total** | **~20 GB** |

## Project Structure

```
├── main.go                 # CLI entry point and command router
├── scraper.go              # Parallel bid scraping engine
├── session.go              # HTTP session bootstrap and pool
├── session_playwright.go   # Playwright-based WAF bypass
├── db.go                   # SQLite schema, migrations, queries
├── models.go               # Data structures
├── search.go               # FTS5 search handler
├── dashboard.go            # Dashboard stats and API handlers
├── downloader.go           # PDF download workers
├── corrigendum.go          # Corrigendum parsing and tracking
├── scrape_manager.go       # Background scrape orchestration + SSE
├── server.go               # Gin routes and template setup
├── stats.go                # Statistics queries
├── errlog.go               # Timestamped error logging
├── web/
│   ├── templates/          # Go HTML templates (index, results, tender, dashboard)
│   └── static/             # CSS and JS (Tailwind, chip-select)
└── docs/
    ├── deployment.md       # Detailed deployment guide
    └── scraping-process.md # Scraping architecture deep dive
```

## License

This project is for personal/internal use.
