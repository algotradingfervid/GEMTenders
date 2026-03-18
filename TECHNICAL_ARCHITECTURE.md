# GEMTenders - Technical Architecture

## System Overview

GEMTenders is a Go monolith that scrapes, stores, indexes, and serves government tender data from India's GeM portal. It ships as a single binary with an embedded web server, SQLite database, and headless browser integration.

```
┌─────────────────────────────────────────────────────────────────┐
│                        CLI Entry Point                          │
│              cmd/gemscraper/main.go (flag-based)                │
├──────────┬──────────┬──────────┬──────────┬─────────────────────┤
│  scrape  │ download │  serve   │ reindex  │      status         │
└────┬─────┴────┬─────┴────┬─────┴────┬─────┴─────────────────────┘
     │          │          │          │
     ▼          ▼          ▼          ▼
 ┌────────┐ ┌────────┐ ┌────────┐ ┌────────┐
 │Scraper │ │Download│ │ Server │ │ Store  │
 │Package │ │Package │ │Package │ │Package │
 └───┬────┘ └───┬────┘ └───┬────┘ └────────┘
     │          │          │
     ▼          ▼          ▼
 ┌────────────────────────────────────────┐
 │         Session Pool (Playwright)      │
 │    WAF bypass, CSRF, cookie jar        │
 └────────────────────────────────────────┘
     │          │          │
     ▼          ▼          ▼
 ┌────────────────────────────────────────┐
 │     SQLite (WAL mode, FTS5 search)     │
 └────────────────────────────────────────┘
```

---

## Project Structure

```
cmd/gemscraper/main.go            CLI entry point, command router
internal/
  models/models.go                Types, configs, API schemas, helpers
  session/session.go              Playwright/HTTP session management
  store/
    store.go                      SQLite schema, migrations, CRUD
    stats.go                      Dashboard statistics queries
  scraper/
    scraper.go                    Parallel bid scraping engine
    corrigendum.go                Amendment detection & parsing
  downloader/downloader.go        PDF download pipeline
  worker/worker.go                Generic rate-limited worker pool
  manager/manager.go              Background orchestration + SSE broadcast
  server/
    server.go                     Gin routing, template setup
    handlers.go                   HTTP handlers
  errlog/errlog.go                Timestamped error logging
web/
  templates/                      Go HTML templates (index, results, tender, dashboard)
  static/                         Tailwind CSS, HTMX, Chart.js, chip-select.js
data/                             SQLite database
downloads/                        Bid and corrigendum PDFs
logs/                             Error logs
```

---

## CLI Commands & Configuration

```bash
gemscraper scrape     # Fetch all active bids from GeM
  -db        data/gems.db
  -sessions  3           # Playwright browser sessions
  -scrapers  5           # Parallel scraper instances
  -stagger   30          # Seconds between scraper launches
  -workers   100         # Workers per scraper
  -rps       50          # Requests/second per scraper

gemscraper download   # Download bid and corrigendum PDFs
  -db        data/gems.db
  -dir       downloads
  -workers   100
  -rps       50
  -retries   5

gemscraper serve      # Start web server
  -db        data/gems.db
  -downloads downloads
  -addr      :28080
  -sessions  3           # For web-triggered scrapes

gemscraper reindex    # Rebuild full-text search index
gemscraper status     # Show scraping/download progress
```

---

## Session Management & WAF Bypass

The GeM portal uses a Web Application Firewall. The system bypasses it with a two-tier approach:

```
┌──────────────────────────────────────────────────┐
│              Session Bootstrap                    │
│                                                  │
│  1. Launch headless Chromium via Playwright       │
│  2. Navigate to GeM portal, wait for networkidle │
│  3. Extract CSRF token from page                 │
│  4. Extract WAF cookies from browser context     │
│  5. Transfer cookies to Go net/http client       │
│  6. Force HTTP/1.1 (disable h2 ALPN negotiation) │
│                                                  │
│  Fallback: Raw HTTP with browser-like headers    │
└──────────────────────────────────────────────────┘
```

**Session Pool** - Thread-safe round-robin using atomic counter:

```go
type SessionPool struct {
    pairs []*SessionPair   // Each: CSRF token + HTTP client
    idx   uint64           // Atomic counter for round-robin
}
```

---

## Scraping Architecture

### Parallel Scraper Design

```
5 staggered scrapers (launched 30s apart)
   │
   ├── Scraper 1: 100 workers ──┐
   ├── Scraper 2: 100 workers ──┤
   ├── Scraper 3: 100 workers ──┼── Rate limiter (50 req/s each)
   ├── Scraper 4: 100 workers ──┤        │
   └── Scraper 5: 100 workers ──┘        ▼
                                   GeM API endpoint
                                   POST /all-bids-data
                                         │
                                         ▼
                                   InsertBidsBatch
                                   (INSERT OR IGNORE)
```

**Why 5 staggered scrapers:** Records shift between pages during scraping. Staggering catches records that would otherwise be missed.

**Rate limiting:** `golang.org/x/time/rate.Limiter` with burst = 2x RPS.

**Retry:** 2 attempts per page with 3-second backoff and session rotation on failure.

### API Interaction

- **Endpoint:** `POST https://bidplus.gem.gov.in/all-bids-data`
- **Page size:** 10 records per page
- **Response format:** Solr-style JSON with array fields (e.g., `b_id: [123]`)
- **Flattened** during insert using `FirstStr()`, `FirstInt()` helpers

### Progress Reporting

A background goroutine reports every 3 seconds using atomic counters (no mutex contention). Reports pages done, bids inserted, errors, and calculated ETA.

---

## Corrigendum Detection & Processing

```
For each active bid (end_date > now):
  │
  ├── POST /public-bid-other-details/{bid_id}
  │   └── Returns: { corrigendum: bool, representation: bool }
  │
  ├── If corrigendum exists:
  │   ├── POST /bidding/bid/viewCorrigendum/{bid_id}
  │   │   └── Returns: HTML with amendment details
  │   ├── Parse with regex:
  │   │   ├── PDF download links (corrigendum docs)
  │   │   ├── Modified dates per corrigendum
  │   │   └── "Bid extended to" dates (latest = new end_date)
  │   ├── Delta detection: skip if HTML unchanged since last check
  │   └── Insert corrigendum documents, update bid end_date
  │
  └── If representation exists:
      └── GET /publish-representations/{bid_id}
          └── Store HTML for display
```

**Key regex patterns:**
- `href="(/bidding/bid/showcorrigendumpdf/(\d+)/(\d+))"` — PDF links
- `Bid extended to\s*<strong>([\d\- :]+)</strong>` — Extended deadlines
- `<div class="well">` — Corrigendum count

---

## PDF Download Pipeline

```
GetPendingDownloads (pdf_downloaded = 0)
   │
   ├── Filter: skip if file already on disk
   │
   ├── 100 workers, 50 req/s rate limit
   │   └── downloadFile(url, destPath, maxRetries=5)
   │       └── Linear backoff: 2s, 4s, 6s, 8s, 10s
   │
   ├── Bid PDFs:       downloads/GeM-Bidding-{id}.pdf
   └── Corrigendum PDFs: downloads/corrigendums/Corrigendum-{corrId}-{bidId}.pdf
```

On success, marks `pdf_downloaded = 1` in the database.

---

## Database Layer

### SQLite Configuration

- **WAL mode** (Write-Ahead Logging) for concurrent reads during writes
- **FTS5** virtual table for full-text search with BM25 relevance ranking
- **CGO required** — uses `github.com/mattn/go-sqlite3`

### Schema

**`bids`** — Core bid records (20+ columns)

| Column | Type | Notes |
|--------|------|-------|
| id | TEXT PK | Unique bid identifier |
| bid_id, bid_number | INT, TEXT | GeM identifiers |
| bid_number_parent, bid_id_parent | TEXT, INT | Parent bid reference |
| category_name | TEXT | Procurement category |
| ministry_name, department_name | TEXT | Government org |
| start_date, end_date | TEXT | Bid window (ISO format) |
| end_date_original | TEXT | Pre-amendment end date |
| total_quantity | INT | Quantity requested |
| is_high_value | INT | High-value flag |
| pdf_downloaded | INT | 0/1 download status |
| created_at | DATETIME | Insertion timestamp |

**Indexes:** `bid_id_parent`, `pdf_downloaded`, `bid_number`, `department_name`, `category_name`, `end_date`, `created_at`

**`bid_other_details`** — Amendment metadata

| Column | Type | Notes |
|--------|------|-------|
| bid_id | INT PK | FK to bids |
| has_corrigendum, has_representation | INT | Boolean flags |
| corrigendum_html, representation_html | TEXT | Raw HTML from GeM |
| corrigendum_count | INT | Number of amendments |
| latest_end_date | TEXT | Most recent extended date |
| last_checked | TEXT | Timestamp of last check |

**`corrigendum_documents`** — Individual amendment PDFs

| Column | Type | Notes |
|--------|------|-------|
| id | INT PK AUTO | Sequential ID |
| bid_id | INT | FK to bids |
| corrigendum_id | INT | GeM corrigendum ID |
| download_url | TEXT | PDF path on GeM |
| modified_on | TEXT | Amendment date |
| downloaded | INT | 0/1 download status |

**Unique constraint:** `(bid_id, download_url)`

**`bids_fts`** — Full-text search virtual table

```sql
CREATE VIRTUAL TABLE bids_fts USING fts5(
    bid_number, bid_number_parent, category_name,
    ministry_name, department_name,
    content='bids', content_rowid='rowid'
)
```

### Migrations

Applied automatically on `InitDB()`:
1. Add `end_date_original` column (idempotent — logs error if exists)
2. Backfill: `SET end_date_original = end_date WHERE end_date_original = ''`

### Key Query Patterns

- **Batch insert:** Transaction with `INSERT OR IGNORE` (skip duplicates)
- **FTS search:** `SELECT ... FROM bids_fts WHERE bids_fts MATCH ? ORDER BY rank`
- **Filtered search:** Dynamic WHERE clause with parameterized placeholders (SQL injection safe)
- **Corrigendum upsert:** `INSERT ... ON CONFLICT(bid_id) DO UPDATE`
- **Stats:** Single consolidated queries with CASE expressions for pipeline breakdowns

---

## HTTP Server

### Framework & Routes

Built on **Gin** (`github.com/gin-gonic/gin`).

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/` | Search page |
| GET | `/search` | Full-text search (HTMX fragment) |
| GET | `/tender/:id` | Bid detail page |
| GET | `/pdf/:id` | Serve bid PDF |
| GET | `/corrigendum-pdf/:corrId/:bidId` | Serve corrigendum PDF |
| GET | `/dashboard` | Analytics dashboard |
| GET | `/api/stats/summary` | JSON: total bids, PDFs, timestamps |
| GET | `/api/stats/pipeline` | JSON: active/expiring/expired counts |
| GET | `/api/stats/departments` | JSON: top 10 departments by bid count |
| GET | `/api/stats/categories` | JSON: top 10 categories by bid count |
| GET | `/api/stats/timeline` | JSON: 30-day daily bid counts |
| POST | `/api/scrape/start` | Trigger background scrape |
| GET | `/api/scrape/status` | Current scrape status |
| GET | `/api/scrape/progress` | SSE progress stream |
| GET | `/api/departments` | Typeahead autocomplete |
| GET | `/api/categories` | Typeahead autocomplete |

### Template Functions

```go
"safeHTML"    // Render raw HTML (for corrigendum/representation content)
"formatDate"  // Parse multiple formats, append " IST" timezone label
```

---

## Background Task Manager

```
ScrapeManager
  │
  ├── Start(tasks) — launches background goroutine
  │   ├── Bootstrap sessions
  │   ├── For each task: scrape → download → corrigendum
  │   └── Broadcast progress via pub-sub channels
  │
  ├── Subscribe() — returns channel for SSE handler
  │   └── Buffered channel, drops messages for slow consumers
  │
  └── broadcast(progress)
      └── RWMutex guards listener map
          └── Non-blocking send to each subscriber
```

**SSE endpoint** (`/api/scrape/progress`):
- Subscribes to manager broadcast channel
- Streams `ScrapeProgress` JSON events to connected clients
- Sends current status immediately on connect
- Closes on task completion

---

## Frontend Stack

### Technologies

| Technology | Version | Purpose |
|-----------|---------|---------|
| **Tailwind CSS** | CDN | Utility-first styling |
| **HTMX** | 2.0 | Dynamic content without page refreshes |
| **Chart.js** | CDN | Analytics visualizations |
| **ChipSelect** | Custom | Multi-select autocomplete component |

### HTMX Integration

```html
<!-- Real-time search with 300ms debounce -->
<input hx-get="/search"
       hx-trigger="input changed delay:300ms"
       hx-target="#results"
       hx-indicator="#spinner">

<!-- Dashboard stats loaded on page load -->
<div hx-get="/api/stats/summary" hx-trigger="load"></div>
```

### Chart.js Visualizations

- **Pipeline doughnut:** Active vs. expired tenders
- **Departments bar chart:** Top 10 by bid count (horizontal)
- **Categories bar chart:** Top 10 by bid count (horizontal)
- **Timeline line chart:** 30-day daily bid arrival trend

### ChipSelect Component

Custom autocomplete multi-select for department/category filtering:
- Fetches suggestions from `/api/departments` or `/api/categories`
- 200ms debounce on input
- Renders selected values as removable chips
- Populates hidden form field for HTMX submission

---

## Concurrency Patterns

| Pattern | Where Used | Implementation |
|---------|-----------|----------------|
| **Atomic round-robin** | Session pool | `atomic.AddUint64` for lock-free rotation |
| **Buffered channel + WaitGroup** | Scraper, downloader | Job queue → N workers → WaitGroup.Wait() |
| **Token bucket rate limiter** | All HTTP requests | `rate.NewLimiter(rps, rps*2)` |
| **Atomic counters** | Progress reporting | `atomic.AddInt64` / `LoadInt64` for contention-free tracking |
| **Ticker goroutine** | Progress reporting | Reports every 3s, exits on `close(done)` |
| **Context cancellation** | Manager tasks | `context.WithCancel` for stopping running scrapes |
| **Pub-sub channels** | SSE broadcast | `RWMutex` + map of subscriber channels |

---

## Error Handling

### Error Log

```go
type ErrorLog struct {
    file   *os.File
    logger *log.Logger
    mu     sync.Mutex
    count  int
}
```

- Creates timestamped log files: `logs/scrape_errors_TIMESTAMP.log`
- Dual output: stderr (console) + log file
- Reports total error count on `Close()`

### Error Strategy

| Scope | Behavior |
|-------|----------|
| **CLI command fails** | `log.Fatal` → exit 1 |
| **Individual page/download fails** | Log error, increment counter, continue |
| **Corrigendum check fails** | Log error, don't fail the overall scrape |
| **Session creation fails** | Retry with fallback (Playwright → raw HTTP) |

Errors are wrapped with `fmt.Errorf("context: %w", err)` for chain inspection.

---

## Build & Deployment

### Build

```bash
CGO_ENABLED=1 go build -tags "fts5" -o gemscraper ./cmd/gemscraper
```

- `CGO_ENABLED=1` — Required for SQLite C bindings
- `-tags "fts5"` — Enables SQLite full-text search

### Dependencies

```
github.com/mattn/go-sqlite3                     SQLite driver (CGO)
golang.org/x/time                               Rate limiting
github.com/gin-gonic/gin                        Web framework
github.com/playwright-community/playwright-go   Browser automation
```

### Disk Footprint

| Component | Size |
|-----------|------|
| Binary | ~25 MB |
| SQLite database | ~50 MB |
| Bid PDFs | 15-20 GB |
| Corrigendum PDFs | ~500 MB |
| Chromium (auto-installed) | ~130 MB |
| **Total** | **~20 GB** |

### Cron Automation

```cron
0 */6 * * *   cd /opt/GEMTenders && ./gemscraper scrape >> scrape.log 2>&1
15 */6 * * *  cd /opt/GEMTenders && ./gemscraper download >> download.log 2>&1
```

### Systemd Service

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

### Nginx Reverse Proxy

```nginx
server {
    listen 80;
    server_name tenders.example.com;

    location / {
        proxy_pass http://127.0.0.1:28080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_buffering off;  # Required for SSE streaming
    }
}
```

---

## Key Data Flows

### Scrape Flow

```
CLI: gemscraper scrape
  → BootstrapSessions(3) [Playwright + cookie extraction]
  → ScrapeBids [5 scrapers × 100 workers = 500 concurrent]
     → POST /all-bids-data (paginated, 10/page)
     → InsertBidsBatch (INSERT OR IGNORE)
  → ScrapeCorrigendums [check all active bids for amendments]
     → Upsert bid_other_details + corrigendum_documents
     → Update bid end_date if extended
```

### Search Flow

```
User types query
  → HTMX: GET /search?q=... (300ms debounce)
  → SearchBidsFiltered (FTS5 MATCH + filters)
  → Render results.tmpl fragment
  → HTMX injects into #results div
```

### Dashboard SSE Flow

```
User clicks "Run All"
  → POST /api/scrape/start {tasks: [scrape, download, corrigendum]}
  → ScrapeManager.Start() → background goroutine
  → EventSource('/api/scrape/progress')
  → Manager broadcasts progress → SSE pushes to browser
  → Dashboard updates progress bars, stats, charts in real-time
```
