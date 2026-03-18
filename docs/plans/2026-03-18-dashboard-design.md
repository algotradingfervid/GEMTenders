# GEMTenders Dashboard & Advanced Search — Design Document

**Date:** 2026-03-18
**Status:** Approved

## Overview

Add a dashboard page (`/dashboard`) showing scrape health, tender statistics, and breakdowns with live scrape controls. Enhance the search page (`/`) with an advanced search panel featuring typeahead multiselect chip filters for departments/categories and date range filtering.

## Architecture: HTMX + SSE + Slim JS

- **HTMX** for page interactions (consistent with existing codebase)
- **Server-Sent Events (SSE)** for real-time scrape progress via Gin's `c.Stream()`
- **Chart.js** (CDN) for bar charts, line charts, doughnut charts
- **Vanilla JS** (~100 lines) for the typeahead multiselect chip component
- No new frameworks — minimal departure from current patterns

## Dashboard Page (`/dashboard`)

### Scrape Controls Panel
- Buttons: "Scrape Bids", "Download PDFs", "Check Corrigendums", "Run All"
- Clicking opens a confirmation modal showing what will run and estimated duration
- On confirm, starts background goroutine + opens SSE stream (`/api/scrape/progress`)
- Live progress panel: progress bar, pages scraped, bids inserted, errors, ETA
- Only one scrape job at a time (buttons disabled while running)
- State persists across page refreshes (in-memory goroutine state)

### Scrape Health Cards (row of stat cards)
- Total bids in DB
- Last scrape timestamp
- Bids added in last 24h
- Error count (last scrape)
- PDFs downloaded vs pending
- Corrigendum documents tracked

### Tender Pipeline
- Closing soon table: tenders closing in 24h / 48h / 7 days (counts + expandable list)
- Active vs Expired doughnut chart (Chart.js)

### Breakdowns & Trends
- Top 10 Departments — horizontal bar chart
- Top 10 Categories — horizontal bar chart
- Bids added per day — line chart (last 30 days)
- All charts via Chart.js, data from JSON API endpoints

### Dashboard Load Strategy
- Page shell loads instantly (server-rendered template)
- Each stats section loads asynchronously via HTMX (`hx-get` with `hx-trigger="load"`)
- Charts populate after JSON endpoints return
- Fast initial load — no blocking on heavy queries

## Advanced Search (on `/`)

### Filter Panel
- "Advanced Search" toggle button next to search bar reveals collapsible filter panel
- **Departments** — typeahead multiselect with chip/tag UI (`/api/departments?q=...`)
- **Categories** — same chip component (`/api/categories?q=...`)
- **Date Range** — two native HTML date pickers (start/end) filtering on bid closing date
- **Apply Filters** button — submits all filters + query via HTMX
- **Clear Filters** link — resets everything
- Filter state preserved in URL params for bookmarking/sharing

### Chip Multiselect Component
- Vanilla JS (~100 lines)
- Keyboard navigation, chip rendering, autocomplete dropdown
- Debounced 200ms typeahead requests

### Search Handler Changes
- Parse optional filter query params: `departments`, `categories`, `start_date`, `end_date`
- Build dynamic WHERE clause: `department_name IN (...)`, `category_name IN (...)`, `end_date BETWEEN ... AND ...`
- FTS query + filters combined when both present; direct query when filters only

## Backend Architecture

### New Go Files
- `dashboard.go` — Dashboard handler, stats queries, scrape control endpoints
- `scrape_manager.go` — Background scrape goroutine management, state tracking, SSE events
- `stats.go` — SQL queries for all dashboard metrics

### New API Endpoints

| Method | Route | Purpose |
|--------|-------|---------|
| GET | `/dashboard` | Dashboard page |
| GET | `/api/stats/summary` | Health cards data (JSON) |
| GET | `/api/stats/pipeline` | Active/expired/closing-soon (JSON) |
| GET | `/api/stats/departments` | Top departments breakdown (JSON) |
| GET | `/api/stats/categories` | Top categories breakdown (JSON) |
| GET | `/api/stats/timeline` | Bids per day, last 30 days (JSON) |
| POST | `/api/scrape/start` | Start scrape job |
| GET | `/api/scrape/status` | Current scrape status (JSON) |
| GET | `/api/scrape/progress` | SSE stream for live progress |
| GET | `/api/departments` | Typeahead search for departments |
| GET | `/api/categories` | Typeahead search for categories |

### Scrape Manager
- Singleton `ScrapeManager` struct with mutex-protected state
- Tracks: running tasks, progress counters, error count, start time
- Existing scraper/downloader functions get callback hooks to report progress
- SSE stream reads from a channel that the scrape goroutine writes to
- Only one job at a time; in-memory state only (resets on server restart)

### New Templates
- `web/templates/dashboard.tmpl` — Dashboard page
- `web/templates/filters.tmpl` — Advanced search filter partial

### New JS Assets
- `web/static/chip-select.js` — Typeahead multiselect chip component
- Chart.js via CDN

## Data & Performance

- All stats queries are simple aggregations on the `bids` table (~42K rows) — fast
- Department/category breakdowns: `GROUP BY ... ORDER BY COUNT(*) DESC LIMIT 10`
- Typeahead: `SELECT DISTINCT col FROM bids WHERE col LIKE ?||'%' LIMIT 10` with 200ms debounce
- No new database tables needed
- Add indexes on `department_name` and `category_name` if performance requires it
- SSE events: `progress`, `error`, `complete` — native EventSource auto-reconnect on client
