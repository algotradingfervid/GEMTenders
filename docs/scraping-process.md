# GEM Tenders — Scraping Process Documentation

## Overview

GEMTenders scrapes bid/tender data from the **Government e-Marketplace (GEM)** portal (`bidplus.gem.gov.in`). The system uses a high-throughput Go pipeline that bootstraps browser-like sessions, fetches paginated bid listings via the GEM API, stores them in SQLite, and downloads associated PDF documents.

The pipeline has four stages:

```
Session Bootstrap → Parallel Listing Scrape → DB Persistence → PDF Download
```

---

## 1. Data Source

### Target Portal

- **Base URL**: `https://bidplus.gem.gov.in`
- **Dataset Size**: 4.19M+ bids (and growing)
- **Update Frequency**: Real-time — new bids appear continuously

### Core API: Bid Listings

**Endpoint**: `POST https://bidplus.gem.gov.in/all-bids-data`

**Content-Type**: `application/x-www-form-urlencoded; charset=UTF-8`

**Request Body**:
```
payload={json_payload}&csrf_bd_gem_nk={csrf_token}
```

**Payload Structure**:
```json
{
  "param": {
    "searchBid": "",
    "searchType": "fullText"
  },
  "filter": {
    "bidStatusType": "ongoing_bids",
    "byType": "all",
    "highBidValue": "",
    "byEndDate": { "from": "", "to": "" },
    "sort": "Bid-End-Date-Oldest"
  }
}
```

**Pagination**: Offset-based using the `start` field. Each page returns 10 results.

**Response Format** (Solr-style):
```json
{
  "status": 1,
  "response": {
    "response": {
      "numFound": 4195680,
      "docs": [
        {
          "id": "abc123",
          "b_id": [9067932],
          "b_bid_number": ["GEM/2026/B/7310854"],
          "b_category_name": ["LED Fixture"],
          "b_total_quantity": [500],
          "b_status": [1],
          "final_start_date_sort": ["2026-03-02T15:37:09Z"],
          "final_end_date_sort": ["2026-03-17T21:00:00Z"],
          "b_ministry_name": ["Ministry Of Railways"],
          "b_department_name": ["Northern Railway"],
          "b_is_high_value": [false],
          "b_bid_type": [0]
        }
      ]
    }
  }
}
```

> **Note**: All Solr document fields are arrays. The scraper flattens them using `firstStr()`, `firstInt()`, and `firstBool()` helper functions.

### PDF Documents

**Endpoint**: `GET https://bidplus.gem.gov.in/showbidDocument/{bid_id_parent}`

Returns the bid document as a PDF file.

---

## 2. WAF & Authentication Bypass

The GEM portal is protected by an **F5 BIG-IP WAF** that performs TLS fingerprinting and requires valid CSRF tokens and session cookies.

### Challenges

| Challenge | Description |
|-----------|-------------|
| TLS Fingerprinting | F5 WAF rejects connections with non-browser TLS fingerprints |
| HTTP/2 Rejection | WAF blocks HTTP/2 ALPN negotiation (Go's default since 1.25) |
| CSRF Protection | Every POST requires a valid `csrf_bd_gem_nk` token |
| Session Cookies | WAF sets `TS01*` tracking cookies and JS challenge cookies |

### Go Solution

The Go scraper bypasses these protections with a custom HTTP transport:

**Force HTTP/1.1** — Disable HTTP/2 ALPN:
```go
transport := &http.Transport{
    TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
    TLSNextProto:    make(map[string]func(string, *tls.Conn) http.RoundTripper),
}
```

**Browser-Like Headers**:
```
Accept: text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,...
Accept-Encoding: gzip, deflate, br
Accept-Language: en-US,en;q=0.9
User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 ...
```

**Cookie Jar**: Persistent `net/http/cookiejar` maintains session cookies across requests.

**CSRF Extraction**: After bootstrapping, the scraper reads the `csrf_gem_cookie` from the cookie jar to use in API requests.

---

## 3. Session Management (`session.go`)

### SessionPool

The scraper maintains a pool of authenticated sessions that are rotated in round-robin fashion.

```
SessionPool
├── sessions []Session     // Array of authenticated sessions
├── mu       sync.Mutex    // Thread-safe rotation
└── idx      int           // Current rotation index

Session
├── client *http.Client    // HTTP client with cookie jar
└── csrf   string          // CSRF token for this session
```

### Bootstrap Process

```
For each of N sessions:
  1. Create HTTP client with custom TLS transport
  2. GET /all-bids → establishes WAF cookies (TS01*, JSESSIONID)
  3. Extract csrf_gem_cookie from cookie jar
  4. Store Session{client, csrf}
  5. Wait 5 seconds before bootstrapping next session (avoid rate limit)
```

### Retry Strategy with Exponential Backoff

If session bootstrap fails, it retries with increasing delays:

| Attempt | Backoff Delay |
|---------|--------------|
| 1       | 5 seconds    |
| 2       | 20 seconds   |
| 3       | 45 seconds   |
| 4       | 80 seconds   |
| 5       | (give up)    |

**Formula**: `(attempt^2) * 5 seconds`

**Partial Failure Handling**: Scraping proceeds if at least 1 session bootstraps successfully. Only a total failure (0 sessions) aborts the operation.

### Session Rotation

```go
func (p *SessionPool) Next() Session {
    p.mu.Lock()
    defer p.mu.Unlock()
    s := p.sessions[p.idx % len(p.sessions)]
    p.idx++
    return s
}
```

Workers call `pool.Next()` to get the next session, distributing load across all authenticated sessions.

---

## 4. Parallel Scraping Architecture (`scraper.go`)

### High-Level Flow

```
ScrapeBids(db, config)
│
├─ 1. Bootstrap N sessions (default: 3)
│
├─ 2. Fetch page 1 to discover total bid count
│     └─ numFound → totalPages = (numFound + 9) / 10
│
├─ 3. Launch M scrapers (default: 5), staggered by S seconds (default: 30)
│     │
│     ├─ Scraper 0: starts immediately
│     ├─ Scraper 1: starts at T+30s
│     ├─ Scraper 2: starts at T+60s
│     └─ ...
│
└─ Each Scraper:
      ├─ W worker goroutines (default: 20)
      ├─ Rate limiter (default: 20 req/s)
      └─ Page channel for work distribution
         │
         └─ Each Worker:
              ├─ Pull page number from channel
              ├─ Wait for rate limit token
              ├─ Call fetchPage(session, page)
              ├─ Retry once on failure (3s delay)
              ├─ Batch insert results into SQLite
              └─ Log progress every 1,000 new bids
```

### Why Staggered Scrapers?

The GEM API is a live system — new bids are constantly being added and the ordering shifts. A single pass through all pages may miss bids that move between pages during the scrape. Running multiple scrapers with staggered start times creates overlapping snapshots that catch these "shifting" bids.

### Page Fetching

Each page fetch:

1. Acquires a rate limit token (blocks until available)
2. Gets the next session from the pool (round-robin)
3. Sends POST to `/all-bids-data` with the page offset (`start = page * 10`)
4. Parses the JSON response
5. Extracts the `docs` array and flattens Solr array fields
6. Returns a slice of `BidDoc` structs

### Concurrency Parameters

| Parameter | Default | Flag | Description |
|-----------|---------|------|-------------|
| Sessions | 3 | `-sessions` | Number of authenticated sessions |
| Scrapers | 5 | `-scrapers` | Number of parallel scraper instances |
| Workers | 20 | `-workers` | Worker goroutines per scraper |
| RPS | 20 | `-rps` | Max requests per second per scraper |
| Stagger | 30s | `-stagger` | Delay between scraper launches |

### Performance

With default settings (5 scrapers x 20 workers x 20 req/s):
- **Throughput**: ~100-200 pages/minute
- **Full scrape**: ~4.19M bids in 1-2 hours
- **Per-page latency**: 200-500ms

---

## 5. Data Model (`db.go`)

### BidDoc Struct (API Response)

```go
type BidDoc struct {
    ID              string   // Solr document ID (unique)
    BidID           []int    // b_id
    BidNumber       []string // b_bid_number (e.g., "GEM/2026/B/7310854")
    BidNumberParent []string // b_bid_number_parent (parent bid for RAs)
    BidIDParent     []int    // b_id_parent
    CategoryName    []string // b_category_name
    TotalQuantity   []int    // b_total_quantity
    Status          []int    // b_status (0=closed, 1=active)
    BidType         []int    // b_bid_type (0=product, 1=service)
    Type            []int    // b_type
    IsBunch         []int    // b_is_bunch
    BidToRA         []int    // b_bid_to_ra (bid-to-reverse-auction flag)
    StartDate       []string // final_start_date_sort (ISO datetime)
    EndDate         []string // final_end_date_sort (ISO datetime)
    IsHighValue     []bool   // b_is_high_value
    MinistryName    []string // b_ministry_name
    DepartmentName  []string // b_department_name
    IsGlobalTender  []int    // b_is_global_tender
    IsRCBid         []int    // b_is_rc_bid
    IsCustomItem    []int    // b_is_custom_item
}
```

### SQLite Schema

**Table: `bids`**

| Column | Type | Description |
|--------|------|-------------|
| `id` | TEXT PRIMARY KEY | Solr document ID |
| `bid_id` | INTEGER | GEM bid ID |
| `bid_number` | TEXT | Format: `GEM/YYYY/B/XXXXXXX` |
| `bid_number_parent` | TEXT | Parent bid number (for Reverse Auctions) |
| `bid_id_parent` | INTEGER | Parent bid ID |
| `category_name` | TEXT | Procurement category |
| `total_quantity` | INTEGER | Quantity requested |
| `status` | INTEGER | 0=closed, 1=active/ongoing |
| `bid_type` | INTEGER | 0=product, 1=service |
| `type` | INTEGER | Metadata type |
| `is_bunch` | INTEGER | Bunching flag |
| `bid_to_ra` | INTEGER | Bid-to-Reverse-Auction flag |
| `start_date` | TEXT | ISO datetime string |
| `end_date` | TEXT | ISO datetime string |
| `is_high_value` | INTEGER | High-value tender flag (>5L INR) |
| `ministry_name` | TEXT | Procuring ministry |
| `department_name` | TEXT | Procuring department |
| `is_global_tender` | INTEGER | Global tendering flag |
| `is_rc_bid` | INTEGER | Rate contract flag |
| `is_custom_item` | INTEGER | Custom item flag |
| `pdf_downloaded` | INTEGER DEFAULT 0 | PDF download tracking |
| `created_at` | DATETIME | Row insertion timestamp |

**Indexes**:

| Index | Column(s) | Purpose |
|-------|-----------|---------|
| `idx_bid_id_parent` | `bid_id_parent` | Parent-child bid lookups |
| `idx_pdf_downloaded` | `pdf_downloaded` | Query pending PDF downloads |
| `idx_bid_number` | `bid_number` | Direct bid number lookups |

**Full-Text Search Table: `bids_fts`**

```sql
CREATE VIRTUAL TABLE bids_fts USING fts5(
    bid_number,
    bid_number_parent,
    category_name,
    ministry_name,
    department_name,
    content='bids',
    content_rowid='rowid'
);
```

Triggers on `bids` automatically keep the FTS index in sync on INSERT, UPDATE, and DELETE.

### Database Configuration

- **Mode**: WAL (Write-Ahead Logging) — allows concurrent reads while scraper writes
- **Deduplication**: `INSERT OR IGNORE` on the unique `id` column prevents duplicates
- **Batch Inserts**: Records are inserted in transactions of 10 (one API page per batch)

---

## 6. PDF Download Process (`downloader.go`)

### Flow

```
DownloadPDFs(db, config)
│
├─ 1. Query DB: SELECT bid_id_parent WHERE pdf_downloaded = 0
│
├─ 2. Check disk: skip if downloads/GeM-Bidding-{id}.pdf already exists
│     └─ Mark as downloaded in DB if file found
│
├─ 3. Launch N download workers (default: 100)
│     └─ Rate limited at R req/s (default: 50)
│
└─ Each Worker:
      ├─ Pull bid_id_parent from work channel
      ├─ GET /showbidDocument/{bid_id_parent}
      ├─ Save to downloads/GeM-Bidding-{id}.pdf
      ├─ Retry up to M times with exponential backoff
      └─ Mark pdf_downloaded = 1 in DB on success
```

### Retry Strategy

| Attempt | Backoff |
|---------|---------|
| 1       | 2s      |
| 2       | 4s      |
| 3       | 6s      |
| 4       | 8s      |
| 5       | 10s     |

**Formula**: `attempt * 2 seconds`

### Download Parameters

| Parameter | Default | Flag | Description |
|-----------|---------|------|-------------|
| Workers | 100 | `-workers` | Concurrent download goroutines |
| RPS | 50 | `-rps` | Max requests per second |
| Retries | 5 | `-retries` | Max retry attempts per file |
| Directory | `downloads` | `-dir` | Output directory for PDFs |

### File Naming

```
downloads/GeM-Bidding-{bid_id_parent}.pdf
```

### Disk Usage Estimate

- Average PDF size: 200-500 KB
- Full dataset (~4.19M PDFs): ~1-2 TB

---

## 7. CLI Commands

### `scrape` — Fetch Bid Listings

```bash
go run . scrape \
  -db gems.db \
  -sessions 3 \
  -scrapers 5 \
  -stagger 30 \
  -workers 20 \
  -rps 20
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-db` | string | `gems.db` | SQLite database path |
| `-sessions` | int | 3 | Number of authenticated sessions |
| `-scrapers` | int | 5 | Parallel scraper instances |
| `-stagger` | int | 30 | Seconds between scraper launches |
| `-workers` | int | 20 | Workers per scraper |
| `-rps` | int | 20 | Requests per second per scraper |

### `download` — Download PDFs

```bash
go run . download \
  -db gems.db \
  -dir downloads \
  -workers 100 \
  -rps 50 \
  -retries 5
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-db` | string | `gems.db` | SQLite database path |
| `-dir` | string | `downloads` | PDF output directory |
| `-workers` | int | 100 | Concurrent download workers |
| `-rps` | int | 50 | Requests per second |
| `-retries` | int | 5 | Max retries per download |

### `status` — Database Statistics

```bash
go run . status -db gems.db
```

Reports total bids, pending downloads, and other counts.

### `reindex` — Rebuild FTS Index

```bash
go run . reindex -db gems.db
```

Drops and rebuilds the `bids_fts` full-text search index.

### `serve` — Start Web Server

```bash
go run . serve \
  -db gems.db \
  -downloads downloads \
  -addr :28080
```

Starts the web UI for searching and browsing scraped tenders.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `-db` | string | `gems.db` | SQLite database path |
| `-downloads` | string | `downloads` | PDF directory |
| `-addr` | string | `:28080` | Listen address |

---

## 8. Web Server & Search

### Routes

| Method | Path | Description |
|--------|------|-------------|
| GET | `/` | Landing page with bid listing cards |
| GET | `/search?q=...&page=...` | Full-text search results |
| GET | `/tender/:id` | Tender detail page with embedded PDF |
| GET | `/pdf/:id` | PDF file download |

### Full-Text Search

The FTS5 index supports rich query syntax:

| Query | Behavior |
|-------|----------|
| `LED` | Match in any indexed field |
| `GEM/2026*` | Prefix match |
| `railways AND power` | Boolean AND |
| `ministry:Railways` | Field-specific search |
| `"LED tube light"` | Exact phrase match |

Results are ranked using BM25 relevance scoring and paginated at 20 results per page.

---

## 9. Key Files

| File | Description |
|------|-------------|
| `main.go` | CLI entrypoint and command router |
| `scraper.go` | Parallel scraping engine |
| `session.go` | Session bootstrap, pool, and rotation |
| `db.go` | SQLite schema, migrations, queries, and batch inserts |
| `downloader.go` | PDF download workers with retry logic |
| `server.go` | Gin web server routes |
| `search.go` | FTS5 search handler |

---

## 10. Architecture Diagram

```
                    ┌──────────────────────────────────────────────┐
                    │              GEM Portal (bidplus.gem.gov.in) │
                    │  ┌─────────┐  ┌───────────────┐  ┌───────┐ │
                    │  │ F5 WAF  │  │ /all-bids-data│  │ /PDF  │ │
                    │  └────┬────┘  └───────┬───────┘  └───┬───┘ │
                    └───────┼───────────────┼──────────────┼─────┘
                            │               │              │
              ┌─────────────┼───────────────┼──────────────┼──────────┐
              │             ▼               ▼              ▼          │
              │  ┌──────────────────┐                                 │
              │  │  Session Pool    │                                 │
              │  │  (N sessions)    │                                 │
              │  │  - HTTP/1.1      │                                 │
              │  │  - Cookie Jar    │                                 │
              │  │  - CSRF Token    │                                 │
              │  └────────┬─────────┘                                 │
              │           │                                           │
              │  ┌────────▼─────────────────────────────────────┐    │
              │  │           Scraper Engine                      │    │
              │  │                                               │    │
              │  │  Scraper 0 ──┐                                │    │
              │  │  Scraper 1 ──┼── Workers ── Rate Limiter ──►  │    │
              │  │  Scraper 2 ──┤              (20 req/s)        │    │
              │  │  ...         │                                │    │
              │  └──────────────┼────────────────────────────────┘    │
              │                 │                                      │
              │        ┌────────▼─────────┐    ┌──────────────────┐   │
              │        │   SQLite (WAL)   │    │  Download Engine │   │
              │        │                  │───►│  100 workers     │   │
              │        │  bids table      │    │  50 req/s        │   │
              │        │  bids_fts (FTS5) │    └────────┬─────────┘   │
              │        └────────┬─────────┘             │             │
              │                 │                ┌──────▼──────┐      │
              │        ┌────────▼─────────┐     │  downloads/ │      │
              │        │   Web Server     │     │  *.pdf      │      │
              │        │   (Gin :28080)   │     └─────────────┘      │
              │        │                  │                           │
              │        │  / (landing)     │                           │
              │        │  /search (FTS)   │                           │
              │        │  /tender/:id     │                           │
              │        │  /pdf/:id        │                           │
              │        └──────────────────┘                           │
              │                                                       │
              │                    GEMTenders Application              │
              └───────────────────────────────────────────────────────┘
```
