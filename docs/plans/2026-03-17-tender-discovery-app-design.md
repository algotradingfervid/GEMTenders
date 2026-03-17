# GEM Tender Discovery App — Design

## Goal
A web application for discovering and viewing GEM tender records. Search tenders via full-text search, view details and embedded PDF documents.

## Architecture
Go + Gin server rendering `.tmpl` templates, HTMX for dynamic search. SQLite with FTS5 for full-text search. Serves the same `gems.db` populated by the scraper. PDFs served from the `downloads/` folder.

## Pages
- `/` — Search page with a search box, results load below via HTMX
- `/tender/{id}` — Detail page: metadata table at top, embedded PDF viewer below

## Search Flow
1. User types in search box → HTMX sends `GET /search?q=laptop` after debounce
2. Server queries FTS5 table, returns HTML fragment of result cards
3. Results show: bid number, category, ministry/department, start/end dates, quantity
4. Clicking a result navigates to `/tender/{id}`

## Detail Page
- **Top section:** Bid metadata in a clean card layout (bid number, category, ministry, department, dates, quantity, status, high value badge)
- **Bottom section:** PDF embedded via `<iframe>` or `<embed>` pointing to `/pdf/{download_id}`
- Back button to return to search

## FTS5
```sql
CREATE VIRTUAL TABLE bids_fts USING fts5(
    bid_number, bid_number_parent, category_name,
    ministry_name, department_name,
    content='bids', content_rowid='rowid'
);
```
Populated via triggers or a one-time rebuild command.

## Project Structure
```
web/
  server.go        — Gin routes + handlers
  search.go        — search/FTS logic
  templates/
    layout.tmpl    — base layout (head, nav, scripts)
    index.tmpl     — search page
    results.tmpl   — HTMX partial: search result cards
    tender.tmpl    — detail page with PDF embed
  static/
    style.css      — minimal styling
```

## Endpoints

| Method | Path | Response | Description |
|--------|------|----------|-------------|
| GET | `/` | Full page | Search page |
| GET | `/search?q=...` | HTML fragment | HTMX search results |
| GET | `/tender/:id` | Full page | Tender detail + PDF |
| GET | `/pdf/:id` | PDF file | Serves PDF from downloads/ |

## Tech Stack
- Go + Gin
- html/template (`.tmpl` files)
- HTMX (CDN)
- SQLite + FTS5
- Minimal CSS (no framework)
