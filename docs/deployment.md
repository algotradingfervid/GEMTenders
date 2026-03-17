# GEM Tenders Scraper — Deployment Guide

## Overview

A Go CLI tool that scrapes ~42K+ bid listings from India's Government e-Marketplace (GEM) portal, tracks corrigendums/representations, downloads bid PDFs, and serves a search UI.

## Prerequisites

| Requirement | Version | Purpose |
|---|---|---|
| Go | 1.25+ | Build the binary |
| GCC/CGO | Any | Required by `go-sqlite3` (C library) |
| Chromium | Auto-installed | Session bootstrap via Playwright |

### macOS
```bash
# Go (via Homebrew)
brew install go

# CGO is available by default with Xcode command line tools
xcode-select --install
```

### Ubuntu/Debian
```bash
# Go
sudo snap install go --classic

# CGO dependencies
sudo apt-get install build-essential

# Playwright system dependencies (for headless Chromium)
sudo apt-get install libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 \
  libxkbcommon0 libxdamage1 libgbm1 libpango-1.0-0 libcairo2 libasound2
```

### RHEL/CentOS
```bash
sudo yum install golang gcc
sudo yum install nss atk at-spi2-atk cups-libs libxkbcommon \
  libXdamage mesa-libgbm pango cairo alsa-lib
```

## Build

```bash
git clone <repo-url> && cd GEMTenders

# Build the single binary
CGO_ENABLED=1 go build -tags "fts5" -o gemscraper .

# Verify
./gemscraper
```

This produces a single `gemscraper` binary. Chromium for Playwright is auto-installed on first run (~130MB download, stored in `~/.cache/ms-playwright/`).

## Configuration

All configuration is via CLI flags. No config files needed.

### Scrape command
```bash
./gemscraper scrape [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-db` | `gems.db` | SQLite database path |
| `-sessions` | `3` | Number of browser sessions to bootstrap |
| `-scrapers` | `5` | Parallel scraper instances |
| `-stagger` | `30` | Seconds between scraper launches |
| `-workers` | `100` | Workers per scraper instance |
| `-rps` | `50` | Requests per second per scraper |

### Download command
```bash
./gemscraper download [flags]
```

| Flag | Default | Description |
|---|---|---|
| `-db` | `gems.db` | SQLite database path |
| `-dir` | `downloads` | PDF download directory |
| `-workers` | `100` | Parallel download goroutines |
| `-rps` | `50` | Download requests per second |
| `-retries` | `5` | Max retry attempts per download |

### Other commands
```bash
./gemscraper status              # Show scraping/download progress
./gemscraper serve               # Start web UI (default :28080)
./gemscraper reindex             # Rebuild full-text search index
```

## First Run

```bash
# 1. Build
CGO_ENABLED=1 go build -tags "fts5" -o gemscraper .

# 2. Scrape all bid listings + check corrigendums
#    First run: Chromium auto-installs (~30s), then scrapes ~42K bids (~30min)
./gemscraper scrape

# 3. Download all bid PDFs + corrigendum PDFs
./gemscraper download

# 4. Build search index
./gemscraper reindex

# 5. Start web UI
./gemscraper serve
# Open http://localhost:28080
```

## Recurring Scrapes (Every 6 Hours)

### Cron setup
```bash
crontab -e
```

Add:
```cron
# Scrape new bids + check corrigendums every 6 hours
0 */6 * * * cd /path/to/GEMTenders && ./gemscraper scrape >> scrape.log 2>&1

# Download PDFs every 6 hours (15 min after scrape)
15 */6 * * * cd /path/to/GEMTenders && ./gemscraper download >> download.log 2>&1
```

### Systemd timer (alternative)
```ini
# /etc/systemd/system/gemscraper.service
[Unit]
Description=GEM Tender Scraper

[Service]
Type=oneshot
WorkingDirectory=/path/to/GEMTenders
ExecStart=/path/to/GEMTenders/gemscraper scrape
ExecStartPost=/path/to/GEMTenders/gemscraper download
User=gemscraper
StandardOutput=append:/var/log/gemscraper/scrape.log
StandardError=append:/var/log/gemscraper/scrape.log

# /etc/systemd/system/gemscraper.timer
[Unit]
Description=Run GEM scraper every 6 hours

[Timer]
OnCalendar=*-*-* 00/6:00:00
Persistent=true

[Install]
WantedBy=timers.target
```

```bash
sudo systemctl enable --now gemscraper.timer
```

## Directory Structure After Running

```
GEMTenders/
├── gemscraper                    # Binary
├── gems.db                       # SQLite database (~50MB)
├── gems.db-wal                   # WAL journal (auto-managed)
├── gems.db-shm                   # Shared memory (auto-managed)
├── downloads/                    # Bid PDFs
│   ├── GeM-Bidding-8783867.pdf
│   ├── GeM-Bidding-8936822.pdf
│   └── corrigendums/             # Corrigendum PDFs
│       ├── Corrigendum-4098546-8960898.pdf
│       └── ...
├── scrape_errors_*.log           # Error logs from scrape runs
├── download_errors_*.log         # Error logs from download runs
└── web/                          # Templates + static assets for UI
    ├── templates/
    └── static/
```

## Session Bootstrap

The scraper uses a two-tier session strategy:

1. **Primary: Playwright** — launches headless Chromium, loads the GEM portal, extracts CSRF token + WAF cookies from the real browser, transfers them to a Go `net/http` client
2. **Fallback: Raw HTTP** — if Playwright fails (no Chromium, headless not supported), uses direct HTTP/1.1 requests with manual TLS configuration

After bootstrap, all scraping uses fast `net/http` — the browser is closed immediately.

Multiple sessions are created (default 3) and rotated across workers to distribute load and avoid rate limiting.

## What Gets Scraped

### Every run:
1. **Bid listings** — all ~42K ongoing bids from `/all-bids-data` (paginated, 10 per page)
2. **Corrigendum/representation flags** — `POST /public-bid-other-details/{bid_id}` for each active bid
3. **Corrigendum HTML** — `POST /bidding/bid/viewCorrigendum/{bid_id}` if flagged (with delta detection — only updates if content changed)
4. **Representation HTML** — `GET /publish-representations/{bid_id}` if flagged (with delta detection)
5. **End date updates** — extracted from corrigendum "Bid extended to" entries, updates `bids.end_date`
6. **Download links** — extracted from corrigendum HTML, stored for PDF download

### Download run:
1. **Bid PDFs** — `GET /showbidDocument/{bid_id}` for each bid
2. **Corrigendum PDFs** — `GET /bidding/bid/showcorrigendumpdf/{corrigendum_id}/{bid_id}`

## Monitoring

### Check progress
```bash
./gemscraper status
```

Output:
```
Total bids:             42708
PDFs downloaded:        42703
PDFs pending:           5
Corrigendums checked:   42707
Bids with corrigendums: 7905
Corrigendum PDFs:       2441 downloaded / 2441 total
```

### Error logs

Each run creates a timestamped error log:
- `scrape_errors_2026-03-18_00-15-13.log`
- `download_errors_2026-03-18_00-15-13.log`

See `docs/error-log-guide.md` for how to read and classify errors.

Quick check:
```bash
# Count errors by category
grep -oP '\[\K[^\]]+' scrape_errors_*.log | sort | uniq -c | sort -rn

# Find non-network errors (need investigation)
grep -vE 'connection reset|timeout|status 503|status 429|EOF' scrape_errors_*.log
```

### Database inspection
```bash
# Total bids
sqlite3 gems.db "SELECT COUNT(*) FROM bids"

# Bids with corrigendums
sqlite3 gems.db "SELECT COUNT(*) FROM bid_other_details WHERE has_corrigendum = 1"

# Pending corrigendum PDF downloads
sqlite3 gems.db "SELECT COUNT(*) FROM corrigendum_documents WHERE downloaded = 0"

# Bids expiring today
sqlite3 gems.db "SELECT bid_number, end_date FROM bids WHERE end_date LIKE '$(date +%Y-%m-%d)%'"
```

## Web UI

```bash
./gemscraper serve -addr :28080
```

Features:
- Full-text search across bid numbers, categories, ministries
- Bid detail pages with embedded PDF viewer
- Corrigendum/representation tabs on bid detail pages
- C/R badges on search results indicating corrigendum/representation presence
- Pagination

### Running behind nginx
```nginx
server {
    listen 80;
    server_name tenders.example.com;

    location / {
        proxy_pass http://127.0.0.1:28080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
    }
}
```

## Troubleshooting

### "could not extract CSRF token from page"
The GEM portal may be down or returning an error page. Wait and retry.

### "database is locked"
Too many concurrent writers. Reduce `-workers` (try 20) and `-rps` (try 10).

### Playwright fails to install Chromium
Install system dependencies (see Prerequisites). On servers without a display:
```bash
# Ensure headless dependencies are installed
npx playwright install-deps chromium
```

### Connection reset errors during download
GEM rate-limits downloads. Reduce `-rps` (try 20) and `-workers` (try 50). Failed downloads retry on next run.

### "no sessions could be created"
All session bootstrap attempts failed. Check internet connectivity and whether `https://bidplus.gem.gov.in` is reachable.

## Disk Space

| Component | Approximate Size |
|---|---|
| Binary (`gemscraper`) | ~25MB |
| SQLite database | ~50MB |
| Bid PDFs (~42K files) | ~15-20GB |
| Corrigendum PDFs | ~500MB |
| Chromium (auto-installed) | ~130MB in `~/.cache/ms-playwright/` |
| Error logs | <1MB per run |

**Total: ~20GB** mostly from bid PDFs.
