# Corrigendum & Representation Tracking — Design

**Date:** 2026-03-17
**Status:** Approved

## Problem

The scraper inserts new bids via `INSERT OR IGNORE`, discarding existing records. There is no tracking of corrigendums (bid extensions, date changes, PDF amendments) or representations (buyer query/reply). These can arrive at any time while a bid is active and change critical details like end_date.

## Approach: Hybrid (raw HTML + extracted metadata)

Store raw HTML responses for UI display, while also extracting key queryable fields: latest end_date, document URLs, corrigendum count.

## Database Schema

### New table: `bid_other_details`

| Column | Type | Description |
|--------|------|-------------|
| bid_id | INTEGER PK | FK to bids.bid_id |
| has_corrigendum | INTEGER | 0/1 from API |
| has_representation | INTEGER | 0/1 from API |
| corrigendum_html | TEXT | Raw HTML from `/viewCorrigendum/{bid_id}` |
| representation_html | TEXT | Raw HTML from representation endpoint |
| corrigendum_count | INTEGER | Number of corrigendum entries parsed |
| latest_end_date | TEXT | Latest extended end_date from corrigendum |
| last_checked | TEXT | Timestamp of last check |

### New table: `corrigendum_documents`

| Column | Type | Description |
|--------|------|-------------|
| id | INTEGER PK AUTOINCREMENT | |
| bid_id | INTEGER | FK to bids.bid_id |
| corrigendum_id | INTEGER | Extracted from URL path |
| download_url | TEXT | e.g. `/bidding/bid/showcorrigendumpdf/4098546/8960898` |
| modified_on | TEXT | Parsed from HTML |
| downloaded | INTEGER DEFAULT 0 | Download status |
| UNIQUE(bid_id, download_url) | | Prevents duplicates on re-parse |

### Changes to `bids` table

- Add column: `end_date_original TEXT` — preserves original end_date before corrigendum extensions
- Existing `end_date` gets updated to latest corrigendum end_date when found

## API Flow

### Endpoints used

1. **Check flags:** `POST /public-bid-other-details/{bid_id}` → `{corrigendum: bool, representation: bool}`
2. **Fetch corrigendum:** `POST /bidding/bid/viewCorrigendum/{bid_id}` → HTML
3. **Fetch representation:** `POST /bidding/bid/viewRepresentation/{bid_id}` → HTML (endpoint TBC)
4. **Download corrigendum PDF:** `GET /bidding/bid/showcorrigendumpdf/{corrigendum_id}/{bid_id}`

All POST endpoints require `csrf_bd_gem_nk={csrf}` in the body. Reuses existing `SessionPool`.

### Scrape cycle (every 6hrs)

```
1. Bootstrap sessions (existing)
2. Scrape bid listings (existing)
3. Corrigendum/Representation pass:
   ├─ Get all active bids: SELECT bid_id FROM bids WHERE end_date > NOW()
   ├─ For each bid_id (parallel workers, rate-limited):
   │   ├─ POST /public-bid-other-details/{bid_id}
   │   ├─ Load existing row from bid_other_details
   │   ├─ If corrigendum == true:
   │   │   POST /viewCorrigendum/{bid_id}
   │   │   Compare new HTML vs stored corrigendum_html
   │   │   If different (or no existing row):
   │   │     → Update corrigendum_html
   │   │     → Parse: extract download links → INSERT OR IGNORE corrigendum_documents
   │   │     → Extract latest "Bid extended to" date → UPDATE bids.end_date
   │   │     → Update corrigendum_count
   │   │   If same: skip, just update last_checked
   │   ├─ If representation == true:
   │   │   POST /viewRepresentation/{bid_id}
   │   │   Compare new HTML vs stored representation_html
   │   │   If different: update. If same: skip.
   │   ├─ Update has_corrigendum/has_representation flags if changed
   │   └─ UPDATE last_checked = NOW()
4. Download corrigendum PDFs
   ├─ SELECT from corrigendum_documents WHERE downloaded = 0
   ├─ GET /showcorrigendumpdf/{corrigendum_id}/{bid_id}
   └─ Save to downloads/corrigendums/Corrigendum-{corrigendum_id}-{bid_id}.pdf
```

### Backfill

On first run, `bid_other_details` is empty, so all bids with `end_date > NOW()` get checked. No special code path needed.

### End date management

- On first insert of a bid, `end_date_original` is set to the same value as `end_date`
- When corrigendum has "Bid extended to" dates, take the latest one and UPDATE `bids.end_date`
- A bid stops being monitored when `end_date` (possibly extended) passes

## HTML Parsing Targets

From corrigendum HTML, extract:
- `<a href="/bidding/bid/showcorrigendumpdf/{cid}/{bid_id}">` → download URLs
- `Bid extended to <strong>{date}</strong>` → end_date updates
- `Bid Opening Date: <strong>{date}</strong>` → informational
- `<span id=span_{cid}>` → corrigendum_id
- `Modified On: {date}` → modified_on for corrigendum_documents
- Count of `<div class="well">` → corrigendum_count

## Code Structure

### New files

| File | Purpose |
|------|---------|
| `corrigendum.go` | Core logic: check, fetch, parse, delta detect |
| `corrigendum_test.go` | Tests for HTML parsing |

### Modified files

| File | Changes |
|------|---------|
| `db.go` | New tables, `UpsertBidOtherDetails()`, `InsertCorrigendumDoc()`, `GetPendingCorrigendumDownloads()`, `UpdateBidEndDate()`, `GetActiveBidIDs()`, `end_date_original` column |
| `models.go` | New structs: `OtherDetailsResponse`, `BidOtherDetails`, `CorrigendumDocument` |
| `downloader.go` | Add `DownloadCorrigendumPDFs()` |
| `scraper.go` | Call `ScrapeCorrigendums()` after `ScrapeBids()` |
| `main.go` | Wire into scrape/download/status commands |
| `server.go` | Corrigendum/representation tabs on bid detail page |

## Web UI

### Bid detail page
- Tabbed section: **Corrigendum** and **Representation** tabs
- Corrigendum tab: renders raw HTML, shows download links (local file if downloaded, "Pending" otherwise), badge with count
- Representation tab: renders raw HTML table (section/query/reply), shown only if `has_representation == true`

### Bid listing page
- Badge indicators: "C" for corrigendum, "R" for representation on bid cards

### Status command
```
Corrigendums checked:    41097
Bids with corrigendums:  5230
Corrigendum PDFs:        1200 downloaded / 1350 total
```

## Downloads

- Corrigendum PDFs saved to `downloads/corrigendums/Corrigendum-{corrigendum_id}-{bid_id}.pdf`
- Same worker pool + rate limiter pattern as existing `DownloadPDFs()`
- Links stored in `corrigendum_documents` table for reference from UI
