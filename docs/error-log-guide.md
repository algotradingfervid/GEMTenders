# Error Log Guide

## Log Files

Each command creates a timestamped error log file in the working directory:

```
./gemscraper scrape    → scrape_errors_2026-03-18_00-15-13.log
./gemscraper download  → download_errors_2026-03-18_00-15-13.log
```

Errors also print to stderr in real-time. The log file is a persistent record for post-run analysis.

If there are zero errors, the log file is created but stays empty.

## Log Format

Every error entry follows this format:

```
TIMESTAMP [CATEGORY] id=IDENTIFIER error=DESCRIPTION
```

Example:
```
2026/03/18 00:16:14 [corrigendum-check] id=8971532 error=bid 8971532 other-details: request: connection reset by peer
```

- **TIMESTAMP** — when the error occurred
- **CATEGORY** — which pipeline stage failed (see table below)
- **IDENTIFIER** — the specific record that failed (page number, bid ID, corrigendum ID)
- **DESCRIPTION** — the full error chain, wrapped from innermost cause outward

## Error Categories

### Scrape Command (`./gemscraper scrape`)

| Category | Identifier | What Failed | Common Causes |
|---|---|---|---|
| `scrape` | `page=N` | Fetching a page of bid listings after retry | Network timeout, WAF block, 503 |
| `scrape-insert` | `page=N` | Inserting scraped bids into SQLite | DB locked, disk full |
| `corrigendum-check` | `bid_id` | Checking/fetching corrigendum for a bid | See sub-steps below |
| `[corrigendum] insert doc error` | `bid=X corr=Y` | Saving a corrigendum document link to DB | DB locked, constraint violation |
| `[corrigendum] update end_date error` | `bid=X date=Y` | Updating bid end_date from corrigendum | DB locked |

#### Corrigendum Check Sub-Steps

The `corrigendum-check` error description tells you which step failed:

```
bid 8971532 other-details: request: connection reset     ← Step 1: checking flags
bid 8971532 corrigendum: status 429                      ← Step 2: fetching corrigendum HTML
bid 8971532 representation: request: timeout             ← Step 3: fetching representation HTML
bid 8971532 upsert: database is locked                   ← Step 4: saving to DB
```

### Download Command (`./gemscraper download`)

| Category | Identifier | What Failed | Common Causes |
|---|---|---|---|
| `pdf-download` | `bid_id` | Downloading a bid PDF after all retries | Connection reset, 503, 404 |
| `pdf-mark-downloaded` | `bid_id` | Marking bid as downloaded in DB after successful download | DB locked |
| `[pdf-download] mark-downloaded error` | `bid=X` | Marking already-on-disk file as downloaded in DB | DB locked |
| `corrigendum-pdf-download` | `corr=X bid=Y` | Downloading a corrigendum PDF after all retries | Connection reset, 404 |
| `corrigendum-pdf-mark-downloaded` | `corr=X bid=Y` | Marking corrigendum PDF as downloaded in DB | DB locked |
| `[corrigendum-pdf] mark-downloaded error` | `corr=X bid=Y` | Marking already-on-disk corrigendum file in DB | DB locked |

## Classifying Errors

### Network/Server Errors (Transient — Ignore)

These resolve on the next run. The failed records are automatically retried.

```
connection reset by peer          ← Server dropped connection (rate limiting)
read: connection reset            ← Same, during response read
context deadline exceeded         ← Request timed out
status 503                        ← Server overloaded
status 429                        ← Rate limited
EOF                               ← Connection closed unexpectedly
```

### Data/Application Errors (Investigate)

These indicate bugs or unexpected data. Check the specific bid on the GEM portal.

```
unmarshal: invalid character       ← API returned non-JSON (HTML error page?)
status 404                         ← Bid/document no longer exists on GEM
status 403                         ← Access denied, session may have expired
gzip reader: unexpected EOF        ← Corrupted response
```

### Database Errors (Fix Immediately)

These indicate local system problems.

```
database is locked                 ← Too many concurrent writers, reduce workers
disk full                          ← Free disk space
no such table                      ← DB corrupted, re-init
constraint violation               ← Duplicate data, usually harmless (INSERT OR IGNORE handles this)
```

## Common Analysis Commands

### Count errors by category
```bash
grep -oP '\[\K[^\]]+' scrape_errors_*.log | sort | uniq -c | sort -rn
```

### List all failed bid IDs for corrigendum checks
```bash
grep 'corrigendum-check' scrape_errors_*.log | grep -oP 'id=\K\d+'
```

### Find all network errors (safe to ignore)
```bash
grep -E 'connection reset|timeout|status 503|status 429|EOF' scrape_errors_*.log
```

### Find all non-network errors (need investigation)
```bash
grep -vE 'connection reset|timeout|status 503|status 429|EOF' scrape_errors_*.log
```

### Find failed PDF downloads
```bash
grep 'pdf-download' download_errors_*.log | grep -oP 'id=\K\d+'
```

### Find DB write failures
```bash
grep -E 'database is locked|disk full|no such table' *_errors_*.log
```

## Error Recovery

| Error Type | Recovery |
|---|---|
| Network errors during scrape | Automatic — next `scrape` run retries active bids without `last_checked` |
| Network errors during download | Automatic — next `download` run retries bids with `pdf_downloaded = 0` |
| DB locked errors | Reduce `--workers` and `--rps` flags, or wait for other processes to finish |
| Status 404 on PDF download | Bid may have been withdrawn. Check on GEM portal manually |
| Unmarshal errors | API response format may have changed. Check raw response and update parser |

## Pipeline Flow and Where Errors Occur

```
scrape command
│
├─ 1. Bootstrap sessions
│     └─ Fatal on failure (retries 5x with backoff internally)
│
├─ 2. Scrape bid listings (parallel)
│     ├─ fetchPage() fails      → [scrape] page=N
│     └─ InsertBidsBatch() fails → [scrape-insert] page=N
│
├─ 3. Check corrigendums (parallel)
│     ├─ FetchOtherDetails() fails       → [corrigendum-check] "other-details: ..."
│     ├─ FetchCorrigendumHTML() fails     → [corrigendum-check] "corrigendum: ..."
│     ├─ FetchRepresentationHTML() fails  → [corrigendum-check] "representation: ..."
│     ├─ InsertCorrigendumDoc() fails     → [corrigendum] insert doc error
│     ├─ UpdateBidEndDate() fails         → [corrigendum] update end_date error
│     └─ UpsertBidOtherDetails() fails    → [corrigendum-check] "upsert: ..."
│
└─ Done. Summary printed.

download command
│
├─ 1. Download bid PDFs (parallel, 5 retries each)
│     ├─ downloadWithRetry() fails         → [pdf-download] bid_id
│     ├─ MarkPDFDownloaded() fails         → [pdf-mark-downloaded] bid_id
│     └─ MarkPDFDownloaded() fails (disk)  → [pdf-download] mark-downloaded error
│
├─ 2. Download corrigendum PDFs (parallel, 5 retries each)
│     ├─ downloadCorrigendumWithRetry() fails   → [corrigendum-pdf-download] corr=X bid=Y
│     ├─ MarkCorrigendumDownloaded() fails      → [corrigendum-pdf-mark-downloaded] corr=X bid=Y
│     └─ MarkCorrigendumDownloaded() fails (disk) → [corrigendum-pdf] mark-downloaded error
│
└─ Done. Summary printed.
```
