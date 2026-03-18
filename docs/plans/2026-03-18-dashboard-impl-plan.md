# Dashboard & Advanced Search Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a statistics dashboard at `/dashboard` with live scrape controls (SSE), and add advanced search filters (typeahead chip multiselect for departments/categories, date range) to the existing search page.

**Architecture:** Go/Gin backend serving HTMX-powered templates. Dashboard stats load asynchronously via HTMX `hx-trigger="load"`. Scrape progress streams via SSE (`c.Stream()`). Chart.js (CDN) renders bar/line/doughnut charts. A vanilla JS chip-select component handles typeahead multiselect. No new frameworks.

**Tech Stack:** Go 1.25, Gin, SQLite/FTS5, HTMX, Chart.js (CDN), SSE, vanilla JS

---

## Task 1: Stats Queries (`stats.go`)

**Files:**
- Create: `stats.go`

**Step 1: Create `stats.go` with all dashboard query functions**

```go
package main

import (
	"database/sql"
	"time"
)

type SummaryStats struct {
	TotalBids          int    `json:"total_bids"`
	PDFsDownloaded     int    `json:"pdfs_downloaded"`
	PDFsPending        int    `json:"pdfs_pending"`
	CorrDocsTracked    int    `json:"corr_docs_tracked"`
	CorrDocsDownloaded int    `json:"corr_docs_downloaded"`
	BidsLast24h        int    `json:"bids_last_24h"`
	LastScrapeTime     string `json:"last_scrape_time"`
}

type PipelineStats struct {
	Active      int `json:"active"`
	Expired     int `json:"expired"`
	Closing24h  int `json:"closing_24h"`
	Closing48h  int `json:"closing_48h"`
	Closing7d   int `json:"closing_7d"`
}

type BreakdownItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type TimelinePoint struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

func GetSummaryStats(db *sql.DB) (SummaryStats, error) {
	var s SummaryStats

	db.QueryRow("SELECT COUNT(*) FROM bids").Scan(&s.TotalBids)
	db.QueryRow("SELECT COUNT(*) FROM bids WHERE pdf_downloaded = 1").Scan(&s.PDFsDownloaded)
	db.QueryRow("SELECT COUNT(*) FROM bids WHERE pdf_downloaded = 0").Scan(&s.PDFsPending)
	db.QueryRow("SELECT COUNT(*) FROM corrigendum_documents").Scan(&s.CorrDocsTracked)
	db.QueryRow("SELECT COUNT(*) FROM corrigendum_documents WHERE downloaded = 1").Scan(&s.CorrDocsDownloaded)
	db.QueryRow("SELECT COUNT(*) FROM bids WHERE created_at >= datetime('now', '-1 day')").Scan(&s.BidsLast24h)
	db.QueryRow("SELECT COALESCE(MAX(created_at), '') FROM bids").Scan(&s.LastScrapeTime)

	return s, nil
}

func GetPipelineStats(db *sql.DB) (PipelineStats, error) {
	var p PipelineStats
	now := time.Now().Format("2006-01-02T15:04:05")

	db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date > ?", now).Scan(&p.Active)
	db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date <= ?", now).Scan(&p.Expired)
	db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date > ? AND end_date <= datetime(?, '+1 day')", now, now).Scan(&p.Closing24h)
	db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date > ? AND end_date <= datetime(?, '+2 days')", now, now).Scan(&p.Closing48h)
	db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date > ? AND end_date <= datetime(?, '+7 days')", now, now).Scan(&p.Closing7d)

	return p, nil
}

func GetTopDepartments(db *sql.DB, limit int) ([]BreakdownItem, error) {
	rows, err := db.Query(
		"SELECT department_name, COUNT(*) as cnt FROM bids WHERE department_name != '' GROUP BY department_name ORDER BY cnt DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []BreakdownItem
	for rows.Next() {
		var item BreakdownItem
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func GetTopCategories(db *sql.DB, limit int) ([]BreakdownItem, error) {
	rows, err := db.Query(
		"SELECT category_name, COUNT(*) as cnt FROM bids WHERE category_name != '' GROUP BY category_name ORDER BY cnt DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []BreakdownItem
	for rows.Next() {
		var item BreakdownItem
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func GetBidsTimeline(db *sql.DB, days int) ([]TimelinePoint, error) {
	rows, err := db.Query(
		"SELECT date(created_at) as d, COUNT(*) as cnt FROM bids WHERE created_at >= datetime('now', ? || ' days') GROUP BY d ORDER BY d",
		-days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var points []TimelinePoint
	for rows.Next() {
		var p TimelinePoint
		if err := rows.Scan(&p.Date, &p.Count); err != nil {
			return nil, err
		}
		points = append(points, p)
	}
	return points, nil
}

func SearchDistinctValues(db *sql.DB, column string, query string, limit int) ([]string, error) {
	// Allowlist columns to prevent SQL injection
	allowed := map[string]bool{"department_name": true, "category_name": true}
	if !allowed[column] {
		return nil, nil
	}

	rows, err := db.Query(
		"SELECT DISTINCT "+column+" FROM bids WHERE "+column+" LIKE ? ORDER BY "+column+" LIMIT ?",
		query+"%", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var values []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		values = append(values, v)
	}
	return values, nil
}
```

**Step 2: Commit**

```bash
git add stats.go
git commit -m "feat: add dashboard stats query functions"
```

---

## Task 2: Scrape Manager (`scrape_manager.go`)

**Files:**
- Create: `scrape_manager.go`
- Modify: `scraper.go` — add callback hooks to `ScrapeBids()` progress reporting
- Modify: `downloader.go` — add callback hooks to `DownloadPDFs()` progress reporting
- Modify: `corrigendum.go` — add callback hooks to `ScrapeCorrigendums()` progress reporting

**Step 1: Create `scrape_manager.go`**

This file manages background scrape goroutines, tracks their state, and emits SSE events.

```go
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type ScrapeTask string

const (
	TaskScrape      ScrapeTask = "scrape"
	TaskDownload    ScrapeTask = "download"
	TaskCorrigendum ScrapeTask = "corrigendum"
)

type ScrapeProgress struct {
	Task       ScrapeTask `json:"task"`
	Status     string     `json:"status"` // "running", "completed", "error"
	Message    string     `json:"message"`
	Current    int64      `json:"current"`
	Total      int64      `json:"total"`
	Errors     int64      `json:"errors"`
	StartedAt  time.Time  `json:"started_at"`
	ElapsedSec float64    `json:"elapsed_sec"`
}

type ScrapeManager struct {
	mu        sync.RWMutex
	running   bool
	tasks     []ScrapeTask
	progress  ScrapeProgress
	listeners map[chan ScrapeProgress]struct{}
	cancel    context.CancelFunc
	lastRun   *ScrapeProgress
}

func NewScrapeManager() *ScrapeManager {
	return &ScrapeManager{
		listeners: make(map[chan ScrapeProgress]struct{}),
	}
}

func (sm *ScrapeManager) IsRunning() bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.running
}

func (sm *ScrapeManager) GetProgress() ScrapeProgress {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.progress
}

func (sm *ScrapeManager) GetLastRun() *ScrapeProgress {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.lastRun
}

// Subscribe returns a channel that receives progress updates.
// Caller must call Unsubscribe when done.
func (sm *ScrapeManager) Subscribe() chan ScrapeProgress {
	ch := make(chan ScrapeProgress, 64)
	sm.mu.Lock()
	sm.listeners[ch] = struct{}{}
	sm.mu.Unlock()
	return ch
}

func (sm *ScrapeManager) Unsubscribe(ch chan ScrapeProgress) {
	sm.mu.Lock()
	delete(sm.listeners, ch)
	close(ch)
	sm.mu.Unlock()
}

func (sm *ScrapeManager) broadcast(p ScrapeProgress) {
	sm.mu.Lock()
	sm.progress = p
	sm.progress.ElapsedSec = time.Since(p.StartedAt).Seconds()
	for ch := range sm.listeners {
		select {
		case ch <- sm.progress:
		default:
			// Drop if listener is slow
		}
	}
	sm.mu.Unlock()
}

// Start launches the selected tasks in sequence as a background goroutine.
// Returns an error if already running.
func (sm *ScrapeManager) Start(db *sql.DB, tasks []ScrapeTask, pool *SessionPool) error {
	sm.mu.Lock()
	if sm.running {
		sm.mu.Unlock()
		return fmt.Errorf("scrape already running")
	}
	sm.running = true
	sm.tasks = tasks
	ctx, cancel := context.WithCancel(context.Background())
	sm.cancel = cancel
	sm.mu.Unlock()

	go sm.run(ctx, db, tasks, pool)
	return nil
}

func (sm *ScrapeManager) Stop() {
	sm.mu.RLock()
	cancel := sm.cancel
	sm.mu.RUnlock()
	if cancel != nil {
		cancel()
	}
}

func (sm *ScrapeManager) run(ctx context.Context, db *sql.DB, tasks []ScrapeTask, pool *SessionPool) {
	startTime := time.Now()
	var finalProgress ScrapeProgress

	for _, task := range tasks {
		if ctx.Err() != nil {
			break
		}
		p := ScrapeProgress{
			Task:      task,
			Status:    "running",
			Message:   fmt.Sprintf("Starting %s...", task),
			StartedAt: startTime,
		}
		sm.broadcast(p)

		switch task {
		case TaskScrape:
			sm.runScrapeTask(ctx, db, pool, startTime)
		case TaskDownload:
			sm.runDownloadTask(ctx, db, pool, startTime)
		case TaskCorrigendum:
			sm.runCorrigendumTask(ctx, db, pool, startTime)
		}
	}

	finalProgress = ScrapeProgress{
		Task:      tasks[len(tasks)-1],
		Status:    "completed",
		Message:   "All tasks completed",
		StartedAt: startTime,
	}
	sm.broadcast(finalProgress)

	sm.mu.Lock()
	sm.running = false
	sm.lastRun = &finalProgress
	sm.mu.Unlock()
}

// runScrapeTask wraps ScrapeBids with progress callbacks.
// The actual integration with ScrapeBids requires adding a ProgressFunc
// callback parameter to ScrapeBids. This is done in a later step.
func (sm *ScrapeManager) runScrapeTask(ctx context.Context, db *sql.DB, pool *SessionPool, startTime time.Time) {
	onProgress := func(current, total, errors int64, msg string) {
		sm.broadcast(ScrapeProgress{
			Task:      TaskScrape,
			Status:    "running",
			Message:   msg,
			Current:   current,
			Total:     total,
			Errors:    errors,
			StartedAt: startTime,
		})
	}
	// ScrapeBidsWithProgress will be the modified version of ScrapeBids
	// that accepts a progress callback. See Task 2 Step 2.
	ScrapeBidsWithProgress(db, pool, onProgress)
}

func (sm *ScrapeManager) runDownloadTask(ctx context.Context, db *sql.DB, pool *SessionPool, startTime time.Time) {
	onProgress := func(current, total, errors int64, msg string) {
		sm.broadcast(ScrapeProgress{
			Task:      TaskDownload,
			Status:    "running",
			Message:   msg,
			Current:   current,
			Total:     total,
			Errors:    errors,
			StartedAt: startTime,
		})
	}
	DownloadPDFsWithProgress(db, pool, "downloads", onProgress)
}

func (sm *ScrapeManager) runCorrigendumTask(ctx context.Context, db *sql.DB, pool *SessionPool, startTime time.Time) {
	onProgress := func(current, total, errors int64, msg string) {
		sm.broadcast(ScrapeProgress{
			Task:      TaskCorrigendum,
			Status:    "running",
			Message:   msg,
			Current:   current,
			Total:     total,
			Errors:    errors,
			StartedAt: startTime,
		})
	}
	ScrapeCorrigenumsWithProgress(db, pool, onProgress)
}

func (p ScrapeProgress) JSON() string {
	b, _ := json.Marshal(p)
	return string(b)
}
```

**Step 2: Add `ProgressFunc` type and `*WithProgress` wrapper functions to scraper.go, downloader.go, corrigendum.go**

Add this type near the top of `scraper.go`:

```go
type ProgressFunc func(current, total, errors int64, msg string)
```

Then create thin wrapper functions `ScrapeBidsWithProgress`, `DownloadPDFsWithProgress`, and `ScrapeCorrigenumsWithProgress` in each respective file. These wrappers call the existing functions with default CLI flags but also periodically invoke the callback. The simplest approach: add the `ProgressFunc` as an optional parameter to the existing internal progress-reporter goroutines.

For `scraper.go`, add after the existing `ScrapeBids` function:

```go
func ScrapeBidsWithProgress(db *sql.DB, pool *SessionPool, onProgress ProgressFunc) {
	// Uses same defaults as CLI: 5 scrapers, 30s stagger, 100 workers, 50 rps
	// The existing ScrapeBids function's progress reporter (lines 113-137 of runScraper)
	// already tracks pages via atomic counters. We hook into the same counters.
	ScrapeBidsOpts(db, pool, 5, 30, 100, 50, onProgress)
}
```

Refactor `ScrapeBids` → `ScrapeBidsOpts` that accepts all config + optional `ProgressFunc`. The existing `ScrapeBids` calls `ScrapeBidsOpts` with `nil` callback. Inside the progress reporter goroutine (line 113-137), when `onProgress != nil`, call it alongside the existing `log.Printf`.

Apply the same pattern to `DownloadPDFs` and `ScrapeCorrigendums`.

**Step 3: Commit**

```bash
git add scrape_manager.go scraper.go downloader.go corrigendum.go
git commit -m "feat: add scrape manager with background goroutine control and SSE progress"
```

---

## Task 3: Dashboard Handlers (`dashboard.go`)

**Files:**
- Create: `dashboard.go`

**Step 1: Create `dashboard.go` with all dashboard API endpoints**

```go
package main

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

func DashboardPage(c *gin.Context) {
	c.HTML(http.StatusOK, "dashboard.tmpl", nil)
}

func StatsHandlers(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		// This is a route-group pattern — individual handlers below
	}
}

func SummaryHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats, err := GetSummaryStats(db)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, stats)
	}
}

func PipelineHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats, err := GetPipelineStats(db)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, stats)
	}
}

func DepartmentsBreakdownHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := GetTopDepartments(db, 10)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, items)
	}
}

func CategoriesBreakdownHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := GetTopCategories(db, 10)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, items)
	}
}

func TimelineHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		points, err := GetBidsTimeline(db, 30)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, points)
	}
}

func DepartmentTypeaheadHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		values, err := SearchDistinctValues(db, "department_name", q, 10)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, values)
	}
}

func CategoryTypeaheadHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		values, err := SearchDistinctValues(db, "category_name", q, 10)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, values)
	}
}

func ScrapeStartHandler(db *sql.DB, sm *ScrapeManager, pool *SessionPool) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Tasks []string `json:"tasks"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}

		var tasks []ScrapeTask
		for _, t := range req.Tasks {
			switch ScrapeTask(t) {
			case TaskScrape, TaskDownload, TaskCorrigendum:
				tasks = append(tasks, ScrapeTask(t))
			default:
				c.JSON(400, gin.H{"error": fmt.Sprintf("unknown task: %s", t)})
				return
			}
		}

		if len(tasks) == 0 {
			c.JSON(400, gin.H{"error": "no tasks specified"})
			return
		}

		if err := sm.Start(db, tasks, pool); err != nil {
			c.JSON(409, gin.H{"error": err.Error()})
			return
		}

		c.JSON(200, gin.H{"status": "started", "tasks": req.Tasks})
	}
}

func ScrapeStatusHandler(sm *ScrapeManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(200, gin.H{
			"running":  sm.IsRunning(),
			"progress": sm.GetProgress(),
			"last_run": sm.GetLastRun(),
		})
	}
}

func ScrapeProgressSSEHandler(sm *ScrapeManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		ch := sm.Subscribe()
		defer sm.Unsubscribe(ch)

		// Send current status immediately
		p := sm.GetProgress()
		c.SSEvent("progress", p.JSON())
		c.Writer.Flush()

		c.Stream(func(w io.Writer) bool {
			select {
			case progress, ok := <-ch:
				if !ok {
					return false
				}
				c.SSEvent("progress", progress.JSON())
				return progress.Status != "completed"
			case <-c.Request.Context().Done():
				return false
			}
		})
	}
}
```

**Step 2: Commit**

```bash
git add dashboard.go
git commit -m "feat: add dashboard HTTP handlers and SSE endpoint"
```

---

## Task 4: Register Routes (`server.go`, `main.go`)

**Files:**
- Modify: `server.go:13-75` — add dashboard routes and inject ScrapeManager
- Modify: `main.go:179-193` — create ScrapeManager and SessionPool for serve command

**Step 1: Modify `server.go`**

Update `StartServer` to accept `*ScrapeManager` and `*SessionPool` parameters. Add all new routes after the existing ones:

```go
// Change function signature:
func StartServer(addr string, dbPath string, downloadDir string, sm *ScrapeManager, pool *SessionPool) {

// After existing routes (after line 71), add:

	// Dashboard
	r.GET("/dashboard", DashboardPage)

	// Stats API
	r.GET("/api/stats/summary", SummaryHandler(db))
	r.GET("/api/stats/pipeline", PipelineHandler(db))
	r.GET("/api/stats/departments", DepartmentsBreakdownHandler(db))
	r.GET("/api/stats/categories", CategoriesBreakdownHandler(db))
	r.GET("/api/stats/timeline", TimelineHandler(db))

	// Scrape control API
	r.POST("/api/scrape/start", ScrapeStartHandler(db, sm, pool))
	r.GET("/api/scrape/status", ScrapeStatusHandler(sm))
	r.GET("/api/scrape/progress", ScrapeProgressSSEHandler(sm))

	// Typeahead API
	r.GET("/api/departments", DepartmentTypeaheadHandler(db))
	r.GET("/api/categories", CategoryTypeaheadHandler(db))
```

**Step 2: Modify `main.go` `runServeCmd`**

In `runServeCmd()` (lines 179-193), create the ScrapeManager and SessionPool before calling StartServer:

```go
func runServeCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "database path")
	downloadDir := fs.String("downloads", "downloads", "PDF directory")
	addr := fs.String("addr", ":28080", "server address")
	sessions := fs.Int("sessions", 3, "number of bootstrap sessions for scraping")
	fs.Parse(args)

	sm := NewScrapeManager()

	// Bootstrap session pool for scrape controls
	// Pool is created lazily on first scrape start, or eagerly if sessions > 0
	var pool *SessionPool
	if *sessions > 0 {
		pool = NewSessionPool(*sessions)
		// Don't bootstrap yet — will bootstrap on first scrape start
	}

	StartServer(*addr, *dbPath, *downloadDir, sm, pool)
}
```

**Step 3: Commit**

```bash
git add server.go main.go
git commit -m "feat: register dashboard routes and wire up scrape manager"
```

---

## Task 5: Enhanced Search Handler (`search.go`, `db.go`)

**Files:**
- Modify: `search.go:11-47` — parse filter params, pass to query
- Modify: `db.go:295-324` — add filtered search query variant

**Step 1: Add `SearchBidsFiltered` to `db.go`**

Add after the existing `SearchBids` function (after line 324):

```go
type SearchFilters struct {
	Query       string
	Departments []string
	Categories  []string
	StartDate   string // YYYY-MM-DD
	EndDate     string // YYYY-MM-DD
}

func SearchBidsFiltered(db *sql.DB, filters SearchFilters, limit, offset int) ([]BidResult, int, error) {
	var args []interface{}
	var conditions []string
	var fromClause, orderClause string

	// FTS match if query provided
	if filters.Query != "" {
		fromClause = `FROM bids b
			INNER JOIN bids_fts f ON b.rowid = f.rowid
			LEFT JOIN bid_other_details o ON b.bid_id = o.bid_id`
		conditions = append(conditions, "bids_fts MATCH ?")
		args = append(args, filters.Query)
		orderClause = "ORDER BY f.rank"
	} else {
		fromClause = `FROM bids b
			LEFT JOIN bid_other_details o ON b.bid_id = o.bid_id`
		orderClause = "ORDER BY b.end_date DESC"
	}

	// Department filter
	if len(filters.Departments) > 0 {
		placeholders := ""
		for i, d := range filters.Departments {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, d)
		}
		conditions = append(conditions, "b.department_name IN ("+placeholders+")")
	}

	// Category filter
	if len(filters.Categories) > 0 {
		placeholders := ""
		for i, cat := range filters.Categories {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			args = append(args, cat)
		}
		conditions = append(conditions, "b.category_name IN ("+placeholders+")")
	}

	// Date range filter
	if filters.StartDate != "" {
		conditions = append(conditions, "b.end_date >= ?")
		args = append(args, filters.StartDate)
	}
	if filters.EndDate != "" {
		conditions = append(conditions, "b.end_date <= ?")
		args = append(args, filters.EndDate+"T23:59:59")
	}

	whereClause := ""
	if len(conditions) > 0 {
		whereClause = "WHERE "
		for i, c := range conditions {
			if i > 0 {
				whereClause += " AND "
			}
			whereClause += c
		}
	}

	// Count query
	countSQL := "SELECT COUNT(*) " + fromClause + " " + whereClause
	var total int
	if err := db.QueryRow(countSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Results query
	selectCols := `b.id, b.bid_id, b.bid_number, b.bid_number_parent, b.bid_id_parent,
		b.category_name, b.total_quantity, b.start_date, b.end_date, b.is_high_value,
		b.ministry_name, b.department_name,
		COALESCE(o.has_corrigendum, 0), COALESCE(o.has_representation, 0)`

	dataArgs := make([]interface{}, len(args))
	copy(dataArgs, args)
	dataArgs = append(dataArgs, limit, offset)

	dataSQL := "SELECT " + selectCols + " " + fromClause + " " + whereClause + " " + orderClause + " LIMIT ? OFFSET ?"

	rows, err := db.Query(dataSQL, dataArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	results, err := scanBidResults(rows)
	return results, total, err
}
```

**Step 2: Update `SearchHandler` in `search.go`**

Modify `SearchHandler` to parse filter params and call `SearchBidsFiltered`:

```go
func SearchHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Query("q")
		pageStr := c.DefaultQuery("page", "1")
		page, _ := strconv.Atoi(pageStr)
		if page < 1 {
			page = 1
		}
		limit := 20
		offset := (page - 1) * limit

		filters := SearchFilters{
			Query:     query,
			StartDate: c.Query("start_date"),
			EndDate:   c.Query("end_date"),
		}

		if depts := c.Query("departments"); depts != "" {
			filters.Departments = strings.Split(depts, ",")
		}
		if cats := c.Query("categories"); cats != "" {
			filters.Categories = strings.Split(cats, ",")
		}

		hasFilters := len(filters.Departments) > 0 || len(filters.Categories) > 0 ||
			filters.StartDate != "" || filters.EndDate != ""

		var results []BidResult
		var total int
		var err error

		if hasFilters {
			results, total, err = SearchBidsFiltered(db, filters, limit, offset)
		} else {
			results, total, err = SearchBids(db, query, limit, offset)
		}

		if err != nil {
			c.HTML(200, "results.tmpl", gin.H{"Error": err.Error()})
			return
		}

		totalPages := (total + limit - 1) / limit
		// ... rest of pagination logic unchanged ...
	}
}
```

Add `"strings"` to the import block in `search.go`.

**Step 3: Commit**

```bash
git add search.go db.go
git commit -m "feat: add filtered search with department, category, and date range support"
```

---

## Task 6: Dashboard Template (`web/templates/dashboard.tmpl`)

**Files:**
- Create: `web/templates/dashboard.tmpl`

**Step 1: Create the dashboard template**

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>GEM Tenders — Dashboard</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <script src="https://cdn.tailwindcss.com"></script>
    <script src="https://cdn.jsdelivr.net/npm/chart.js@4"></script>
    <link rel="stylesheet" href="/static/style.css">
</head>
<body class="bg-slate-950 text-white min-h-screen">

<!-- Nav -->
<nav class="fixed top-0 left-0 right-0 z-50 bg-slate-900 border-b border-slate-800 px-6 py-3 flex items-center justify-between">
    <a href="/" class="text-lg font-semibold text-white hover:text-blue-400">GEM Tenders</a>
    <div class="flex gap-4">
        <a href="/" class="text-slate-400 hover:text-white text-sm">Search</a>
        <a href="/dashboard" class="text-white text-sm font-medium border-b-2 border-blue-500 pb-0.5">Dashboard</a>
    </div>
</nav>

<main class="max-w-7xl mx-auto px-4 pt-20 pb-12">

    <!-- Scrape Controls -->
    <section class="mb-8">
        <h2 class="text-lg font-semibold mb-4">Scrape Controls</h2>
        <div class="bg-slate-900 rounded-lg border border-slate-800 p-6">
            <div class="flex flex-wrap gap-3 mb-4">
                <button onclick="showScrapeModal(['scrape'])" class="px-4 py-2 bg-blue-600 hover:bg-blue-700 rounded-lg text-sm font-medium transition-colors">
                    Scrape Bids
                </button>
                <button onclick="showScrapeModal(['download'])" class="px-4 py-2 bg-emerald-600 hover:bg-emerald-700 rounded-lg text-sm font-medium transition-colors">
                    Download PDFs
                </button>
                <button onclick="showScrapeModal(['corrigendum'])" class="px-4 py-2 bg-orange-600 hover:bg-orange-700 rounded-lg text-sm font-medium transition-colors">
                    Check Corrigendums
                </button>
                <button onclick="showScrapeModal(['scrape','download','corrigendum'])" class="px-4 py-2 bg-purple-600 hover:bg-purple-700 rounded-lg text-sm font-medium transition-colors">
                    Run All
                </button>
            </div>
            <!-- Progress panel (hidden by default) -->
            <div id="scrape-progress" class="hidden">
                <div class="bg-slate-800 rounded-lg p-4">
                    <div class="flex items-center justify-between mb-2">
                        <span id="progress-task" class="text-sm font-medium text-slate-300"></span>
                        <span id="progress-pct" class="text-sm text-slate-400"></span>
                    </div>
                    <div class="w-full bg-slate-700 rounded-full h-2 mb-3">
                        <div id="progress-bar" class="bg-blue-500 h-2 rounded-full transition-all duration-300" style="width: 0%"></div>
                    </div>
                    <p id="progress-message" class="text-xs text-slate-400"></p>
                    <div class="flex gap-4 mt-2 text-xs text-slate-500">
                        <span>Processed: <span id="progress-current" class="text-slate-300">0</span></span>
                        <span>Total: <span id="progress-total" class="text-slate-300">0</span></span>
                        <span>Errors: <span id="progress-errors" class="text-red-400">0</span></span>
                        <span>Elapsed: <span id="progress-elapsed" class="text-slate-300">0s</span></span>
                    </div>
                </div>
            </div>
        </div>
    </section>

    <!-- Confirmation Modal -->
    <div id="scrape-modal" class="hidden fixed inset-0 z-50 flex items-center justify-center bg-black/60">
        <div class="bg-slate-900 border border-slate-700 rounded-xl p-6 max-w-md w-full mx-4">
            <h3 class="text-lg font-semibold mb-2">Confirm Scrape</h3>
            <p class="text-sm text-slate-400 mb-1">The following tasks will run:</p>
            <ul id="modal-tasks" class="list-disc list-inside text-sm text-slate-300 mb-4"></ul>
            <p class="text-xs text-slate-500 mb-4">This may take several minutes depending on the number of records.</p>
            <div class="flex justify-end gap-3">
                <button onclick="closeScrapeModal()" class="px-4 py-2 text-sm text-slate-400 hover:text-white transition-colors">Cancel</button>
                <button onclick="confirmScrape()" class="px-4 py-2 bg-blue-600 hover:bg-blue-700 rounded-lg text-sm font-medium transition-colors">Start</button>
            </div>
        </div>
    </div>

    <!-- Summary Stats -->
    <section class="mb-8">
        <h2 class="text-lg font-semibold mb-4">Overview</h2>
        <div id="summary-stats" class="grid grid-cols-2 md:grid-cols-4 gap-4"
             hx-get="/api/stats/summary" hx-trigger="load" hx-swap="innerHTML">
            <div class="text-center text-slate-500 text-sm col-span-full">Loading...</div>
        </div>
    </section>

    <!-- Pipeline -->
    <section class="mb-8">
        <h2 class="text-lg font-semibold mb-4">Tender Pipeline</h2>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div id="pipeline-stats" class="bg-slate-900 rounded-lg border border-slate-800 p-6"
                 hx-get="/api/stats/pipeline" hx-trigger="load" hx-swap="innerHTML">
                <div class="text-center text-slate-500 text-sm">Loading...</div>
            </div>
            <div class="bg-slate-900 rounded-lg border border-slate-800 p-6">
                <canvas id="pipeline-chart" height="200"></canvas>
            </div>
        </div>
    </section>

    <!-- Breakdowns -->
    <section class="mb-8">
        <h2 class="text-lg font-semibold mb-4">Breakdowns</h2>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-6">
            <div class="bg-slate-900 rounded-lg border border-slate-800 p-6">
                <h3 class="text-sm font-medium text-slate-400 mb-3">Top 10 Departments</h3>
                <canvas id="departments-chart" height="300"></canvas>
            </div>
            <div class="bg-slate-900 rounded-lg border border-slate-800 p-6">
                <h3 class="text-sm font-medium text-slate-400 mb-3">Top 10 Categories</h3>
                <canvas id="categories-chart" height="300"></canvas>
            </div>
        </div>
    </section>

    <!-- Timeline -->
    <section class="mb-8">
        <h2 class="text-lg font-semibold mb-4">Bids Added (Last 30 Days)</h2>
        <div class="bg-slate-900 rounded-lg border border-slate-800 p-6">
            <canvas id="timeline-chart" height="150"></canvas>
        </div>
    </section>

</main>

<script>
// ---- Scrape Controls ----
let pendingTasks = [];

function showScrapeModal(tasks) {
    pendingTasks = tasks;
    const list = document.getElementById('modal-tasks');
    list.innerHTML = tasks.map(t => `<li>${t}</li>`).join('');
    document.getElementById('scrape-modal').classList.remove('hidden');
}

function closeScrapeModal() {
    document.getElementById('scrape-modal').classList.add('hidden');
    pendingTasks = [];
}

function confirmScrape() {
    closeScrapeModal();
    fetch('/api/scrape/start', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({tasks: pendingTasks})
    }).then(r => r.json()).then(data => {
        if (data.status === 'started') {
            connectSSE();
        }
    });
}

function connectSSE() {
    const panel = document.getElementById('scrape-progress');
    panel.classList.remove('hidden');

    const source = new EventSource('/api/scrape/progress');
    source.addEventListener('progress', function(e) {
        const p = JSON.parse(e.data);
        document.getElementById('progress-task').textContent = p.task;
        document.getElementById('progress-message').textContent = p.message;
        document.getElementById('progress-current').textContent = p.current;
        document.getElementById('progress-total').textContent = p.total;
        document.getElementById('progress-errors').textContent = p.errors;
        document.getElementById('progress-elapsed').textContent = Math.round(p.elapsed_sec) + 's';

        const pct = p.total > 0 ? Math.round((p.current / p.total) * 100) : 0;
        document.getElementById('progress-pct').textContent = pct + '%';
        document.getElementById('progress-bar').style.width = pct + '%';

        if (p.status === 'completed') {
            source.close();
            document.getElementById('progress-message').textContent = 'All tasks completed!';
            document.getElementById('progress-bar').classList.replace('bg-blue-500', 'bg-emerald-500');
            // Refresh stats
            htmx.trigger('#summary-stats', 'load');
        }
    });
}

// Check if scrape is already running on page load
fetch('/api/scrape/status').then(r => r.json()).then(data => {
    if (data.running) connectSSE();
});

// ---- Charts ----
const chartDefaults = {
    color: '#94a3b8',
    borderColor: '#334155',
    font: { family: 'system-ui' }
};
Chart.defaults.color = chartDefaults.color;

// Summary stats — rendered via HTMX response (server returns HTML fragment)
// We override: the /api/stats/summary endpoint returns JSON, so we handle it with afterSwap
document.body.addEventListener('htmx:beforeSwap', function(evt) {
    if (evt.detail.pathInfo && evt.detail.pathInfo.requestPath === '/api/stats/summary') {
        evt.detail.shouldSwap = false;
        const data = JSON.parse(evt.detail.xhr.responseText);
        evt.detail.target.innerHTML = renderSummaryCards(data);
    }
    if (evt.detail.pathInfo && evt.detail.pathInfo.requestPath === '/api/stats/pipeline') {
        evt.detail.shouldSwap = false;
        const data = JSON.parse(evt.detail.xhr.responseText);
        evt.detail.target.innerHTML = renderPipelineStats(data);
        renderPipelineChart(data);
    }
});

function renderSummaryCards(s) {
    return `
        <div class="bg-slate-900 rounded-lg border border-slate-800 p-4">
            <div class="text-2xl font-bold text-white">${s.total_bids.toLocaleString()}</div>
            <div class="text-xs text-slate-400 mt-1">Total Bids</div>
        </div>
        <div class="bg-slate-900 rounded-lg border border-slate-800 p-4">
            <div class="text-2xl font-bold text-emerald-400">${s.pdfs_downloaded.toLocaleString()}</div>
            <div class="text-xs text-slate-400 mt-1">PDFs Downloaded</div>
            <div class="text-xs text-slate-500">${s.pdfs_pending.toLocaleString()} pending</div>
        </div>
        <div class="bg-slate-900 rounded-lg border border-slate-800 p-4">
            <div class="text-2xl font-bold text-orange-400">${s.corr_docs_tracked.toLocaleString()}</div>
            <div class="text-xs text-slate-400 mt-1">Corrigendum Docs</div>
            <div class="text-xs text-slate-500">${s.corr_docs_downloaded.toLocaleString()} downloaded</div>
        </div>
        <div class="bg-slate-900 rounded-lg border border-slate-800 p-4">
            <div class="text-2xl font-bold text-blue-400">${s.bids_last_24h.toLocaleString()}</div>
            <div class="text-xs text-slate-400 mt-1">Bids (Last 24h)</div>
            <div class="text-xs text-slate-500">Last: ${s.last_scrape_time || 'Never'}</div>
        </div>`;
}

function renderPipelineStats(p) {
    return `
        <div class="space-y-3">
            <div class="flex justify-between items-center">
                <span class="text-sm text-slate-400">Active Tenders</span>
                <span class="text-lg font-semibold text-emerald-400">${p.active.toLocaleString()}</span>
            </div>
            <div class="flex justify-between items-center">
                <span class="text-sm text-slate-400">Expired Tenders</span>
                <span class="text-lg font-semibold text-slate-500">${p.expired.toLocaleString()}</span>
            </div>
            <hr class="border-slate-700">
            <div class="flex justify-between items-center">
                <span class="text-sm text-red-400">Closing in 24h</span>
                <span class="text-lg font-bold text-red-400">${p.closing_24h}</span>
            </div>
            <div class="flex justify-between items-center">
                <span class="text-sm text-orange-400">Closing in 48h</span>
                <span class="text-lg font-semibold text-orange-400">${p.closing_48h}</span>
            </div>
            <div class="flex justify-between items-center">
                <span class="text-sm text-yellow-400">Closing in 7 days</span>
                <span class="text-lg font-semibold text-yellow-400">${p.closing_7d}</span>
            </div>
        </div>`;
}

function renderPipelineChart(p) {
    new Chart(document.getElementById('pipeline-chart'), {
        type: 'doughnut',
        data: {
            labels: ['Active', 'Expired'],
            datasets: [{
                data: [p.active, p.expired],
                backgroundColor: ['#10b981', '#475569'],
                borderWidth: 0
            }]
        },
        options: {
            responsive: true,
            plugins: { legend: { position: 'bottom' } }
        }
    });
}

// Breakdown charts — loaded via fetch
fetch('/api/stats/departments').then(r => r.json()).then(data => {
    new Chart(document.getElementById('departments-chart'), {
        type: 'bar',
        data: {
            labels: data.map(d => d.name.length > 30 ? d.name.slice(0, 30) + '...' : d.name),
            datasets: [{
                label: 'Tenders',
                data: data.map(d => d.count),
                backgroundColor: '#3b82f6',
                borderRadius: 4
            }]
        },
        options: {
            indexAxis: 'y',
            responsive: true,
            plugins: { legend: { display: false } },
            scales: { x: { grid: { color: '#1e293b' } }, y: { grid: { display: false } } }
        }
    });
});

fetch('/api/stats/categories').then(r => r.json()).then(data => {
    new Chart(document.getElementById('categories-chart'), {
        type: 'bar',
        data: {
            labels: data.map(d => d.name.length > 30 ? d.name.slice(0, 30) + '...' : d.name),
            datasets: [{
                label: 'Tenders',
                data: data.map(d => d.count),
                backgroundColor: '#f59e0b',
                borderRadius: 4
            }]
        },
        options: {
            indexAxis: 'y',
            responsive: true,
            plugins: { legend: { display: false } },
            scales: { x: { grid: { color: '#1e293b' } }, y: { grid: { display: false } } }
        }
    });
});

fetch('/api/stats/timeline').then(r => r.json()).then(data => {
    new Chart(document.getElementById('timeline-chart'), {
        type: 'line',
        data: {
            labels: data.map(d => d.date),
            datasets: [{
                label: 'Bids Added',
                data: data.map(d => d.count),
                borderColor: '#3b82f6',
                backgroundColor: 'rgba(59, 130, 246, 0.1)',
                fill: true,
                tension: 0.3,
                pointRadius: 3
            }]
        },
        options: {
            responsive: true,
            plugins: { legend: { display: false } },
            scales: {
                x: { grid: { color: '#1e293b' } },
                y: { grid: { color: '#1e293b' }, beginAtZero: true }
            }
        }
    });
});
</script>

</body>
</html>
```

**Step 2: Commit**

```bash
git add web/templates/dashboard.tmpl
git commit -m "feat: add dashboard template with charts, scrape controls, and SSE progress"
```

---

## Task 7: Advanced Search UI (`web/templates/index.tmpl`, `web/templates/results.tmpl`)

**Files:**
- Modify: `web/templates/index.tmpl` — add nav link, advanced search toggle, filter panel
- Modify: `web/templates/results.tmpl` — pass filter params through pagination links
- Create: `web/static/chip-select.js` — typeahead multiselect chip component

**Step 1: Create `chip-select.js`**

```javascript
class ChipSelect {
    constructor(container, endpoint, name) {
        this.container = container;
        this.endpoint = endpoint;
        this.name = name;
        this.selected = [];
        this.debounceTimer = null;

        this.chips = container.querySelector('.chips');
        this.input = container.querySelector('input[type="text"]');
        this.dropdown = container.querySelector('.dropdown');
        this.hiddenInput = container.querySelector('input[type="hidden"]');

        this.input.addEventListener('input', () => this.onInput());
        this.input.addEventListener('keydown', (e) => this.onKeydown(e));
        this.container.addEventListener('click', () => this.input.focus());
        document.addEventListener('click', (e) => {
            if (!this.container.contains(e.target)) this.dropdown.classList.add('hidden');
        });
    }

    onInput() {
        clearTimeout(this.debounceTimer);
        this.debounceTimer = setTimeout(() => this.fetchSuggestions(), 200);
    }

    onKeydown(e) {
        if (e.key === 'Backspace' && this.input.value === '' && this.selected.length > 0) {
            this.removeChip(this.selected.length - 1);
        }
    }

    async fetchSuggestions() {
        const q = this.input.value.trim();
        if (q.length < 1) {
            this.dropdown.classList.add('hidden');
            return;
        }

        const res = await fetch(`${this.endpoint}?q=${encodeURIComponent(q)}`);
        const items = await res.json();
        const filtered = items.filter(i => !this.selected.includes(i));

        if (filtered.length === 0) {
            this.dropdown.classList.add('hidden');
            return;
        }

        this.dropdown.innerHTML = filtered.map(item =>
            `<div class="px-3 py-2 text-sm text-slate-300 hover:bg-slate-700 cursor-pointer" data-value="${item}">${item}</div>`
        ).join('');
        this.dropdown.classList.remove('hidden');

        this.dropdown.querySelectorAll('[data-value]').forEach(el => {
            el.addEventListener('click', () => this.addChip(el.dataset.value));
        });
    }

    addChip(value) {
        if (this.selected.includes(value)) return;
        this.selected.push(value);
        this.render();
        this.input.value = '';
        this.dropdown.classList.add('hidden');
        this.input.focus();
    }

    removeChip(index) {
        this.selected.splice(index, 1);
        this.render();
    }

    render() {
        // Render chips
        const chipHTML = this.selected.map((val, i) =>
            `<span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-md bg-blue-600/30 text-blue-300 text-xs">
                ${val}
                <button type="button" class="hover:text-white" data-remove="${i}">&times;</button>
            </span>`
        ).join('');
        this.chips.innerHTML = chipHTML;
        this.hiddenInput.value = this.selected.join(',');

        // Bind remove buttons
        this.chips.querySelectorAll('[data-remove]').forEach(btn => {
            btn.addEventListener('click', (e) => {
                e.stopPropagation();
                this.removeChip(parseInt(btn.dataset.remove));
            });
        });
    }

    getValues() {
        return this.selected;
    }
}
```

**Step 2: Update `index.tmpl`**

Add the Dashboard nav link, Advanced Search toggle, and filter panel. Key changes:

- Add "Dashboard" link in the nav bar (right side)
- Add "Advanced Search" button below the search input
- Add collapsible filter panel with two chip-select containers and two date inputs
- Include `chip-select.js` script
- Wire up the Apply Filters button to trigger HTMX search with all params

The filter panel HTML includes:
```html
<!-- Advanced Search Toggle -->
<button id="adv-toggle" onclick="toggleFilters()" class="text-xs text-blue-400 hover:text-blue-300 mt-2">
    Advanced Search ▼
</button>

<!-- Filter Panel (hidden by default) -->
<div id="filter-panel" class="hidden mt-4 bg-slate-800/50 rounded-xl p-4 border border-slate-700 max-w-3xl mx-auto">
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mb-4">
        <!-- Departments chip-select -->
        <div>
            <label class="block text-xs text-slate-400 mb-1">Departments</label>
            <div class="chip-select-container bg-slate-800 rounded-lg border border-slate-600 p-2 min-h-[42px] relative">
                <div class="chips flex flex-wrap gap-1 mb-1"></div>
                <input type="text" placeholder="Type to search..." class="bg-transparent border-none outline-none text-sm text-white w-full">
                <input type="hidden" name="departments">
                <div class="dropdown hidden absolute left-0 right-0 top-full mt-1 bg-slate-800 border border-slate-600 rounded-lg max-h-48 overflow-y-auto z-10"></div>
            </div>
        </div>
        <!-- Categories chip-select -->
        <div>
            <label class="block text-xs text-slate-400 mb-1">Categories</label>
            <div class="chip-select-container bg-slate-800 rounded-lg border border-slate-600 p-2 min-h-[42px] relative">
                <div class="chips flex flex-wrap gap-1 mb-1"></div>
                <input type="text" placeholder="Type to search..." class="bg-transparent border-none outline-none text-sm text-white w-full">
                <input type="hidden" name="categories">
                <div class="dropdown hidden absolute left-0 right-0 top-full mt-1 bg-slate-800 border border-slate-600 rounded-lg max-h-48 overflow-y-auto z-10"></div>
            </div>
        </div>
    </div>
    <div class="grid grid-cols-1 md:grid-cols-2 gap-4 mb-4">
        <div>
            <label class="block text-xs text-slate-400 mb-1">Closing After</label>
            <input type="date" id="filter-start-date" class="w-full bg-slate-800 border border-slate-600 rounded-lg px-3 py-2 text-sm text-white">
        </div>
        <div>
            <label class="block text-xs text-slate-400 mb-1">Closing Before</label>
            <input type="date" id="filter-end-date" class="w-full bg-slate-800 border border-slate-600 rounded-lg px-3 py-2 text-sm text-white">
        </div>
    </div>
    <div class="flex gap-3">
        <button onclick="applyFilters()" class="px-4 py-2 bg-blue-600 hover:bg-blue-700 rounded-lg text-sm font-medium transition-colors">
            Apply Filters
        </button>
        <button onclick="clearFilters()" class="px-4 py-2 text-sm text-slate-400 hover:text-white transition-colors">
            Clear Filters
        </button>
    </div>
</div>
```

JS to wire it up:
```javascript
let deptSelect, catSelect;
document.addEventListener('DOMContentLoaded', () => {
    const containers = document.querySelectorAll('.chip-select-container');
    deptSelect = new ChipSelect(containers[0], '/api/departments', 'departments');
    catSelect = new ChipSelect(containers[1], '/api/categories', 'categories');
});

function toggleFilters() {
    document.getElementById('filter-panel').classList.toggle('hidden');
}

function applyFilters() {
    const q = document.querySelector('input[name="q"]').value;
    const depts = deptSelect.getValues().join(',');
    const cats = catSelect.getValues().join(',');
    const start = document.getElementById('filter-start-date').value;
    const end = document.getElementById('filter-end-date').value;

    let url = `/search?q=${encodeURIComponent(q)}&page=1`;
    if (depts) url += `&departments=${encodeURIComponent(depts)}`;
    if (cats) url += `&categories=${encodeURIComponent(cats)}`;
    if (start) url += `&start_date=${start}`;
    if (end) url += `&end_date=${end}`;

    htmx.ajax('GET', url, {target: '#results', indicator: '#spinner'});
}

function clearFilters() {
    deptSelect.selected = []; deptSelect.render();
    catSelect.selected = []; catSelect.render();
    document.getElementById('filter-start-date').value = '';
    document.getElementById('filter-end-date').value = '';
    applyFilters();
}
```

**Step 3: Update `results.tmpl` pagination links**

Update the Previous/Next pagination buttons to pass through filter query params so filtered results paginate correctly. The `hx-get` URLs should include all current filter values. Pass them as template variables from the search handler.

**Step 4: Commit**

```bash
git add web/static/chip-select.js web/templates/index.tmpl web/templates/results.tmpl
git commit -m "feat: add advanced search with typeahead chip multiselect filters"
```

---

## Task 8: Add Navigation Links and Polish

**Files:**
- Modify: `web/templates/index.tmpl` — add Dashboard link in nav
- Modify: `web/templates/dashboard.tmpl` — verify nav links
- Modify: `web/templates/tender.tmpl` — add Dashboard link in nav
- Modify: `web/static/style.css` — add chip-select and date input styles if needed

**Step 1: Add Dashboard link to all page navbars**

In `index.tmpl` nav bar, add:
```html
<a href="/dashboard" class="text-slate-400 hover:text-white text-sm">Dashboard</a>
```

In `tender.tmpl` nav bar, add the same link.

**Step 2: Add any needed CSS for date inputs and chip-select**

Date inputs on dark backgrounds may need custom styling:
```css
input[type="date"]::-webkit-calendar-picker-indicator {
    filter: invert(1);
}
```

**Step 3: Commit**

```bash
git add web/templates/index.tmpl web/templates/tender.tmpl web/static/style.css
git commit -m "feat: add navigation links and polish styles"
```

---

## Task 9: Database Indexes (Performance)

**Files:**
- Modify: `db.go` — add indexes in `InitDB`

**Step 1: Add indexes for dashboard queries**

In the `InitDB` function, after existing index creation, add:

```go
db.Exec("CREATE INDEX IF NOT EXISTS idx_department_name ON bids(department_name)")
db.Exec("CREATE INDEX IF NOT EXISTS idx_category_name ON bids(category_name)")
db.Exec("CREATE INDEX IF NOT EXISTS idx_end_date ON bids(end_date)")
db.Exec("CREATE INDEX IF NOT EXISTS idx_created_at ON bids(created_at)")
```

**Step 2: Commit**

```bash
git add db.go
git commit -m "feat: add database indexes for dashboard and filter queries"
```

---

## Task 10: Integration Testing

**Step 1: Build and run the server**

```bash
go build -tags fts5 -o gemtenders . && ./gemtenders serve -db gems.db
```

**Step 2: Verify all endpoints manually**

- `GET /` — search page loads, Advanced Search toggle works
- `GET /dashboard` — dashboard loads with stats and charts
- `GET /api/stats/summary` — returns JSON
- `GET /api/stats/pipeline` — returns JSON
- `GET /api/stats/departments` — returns JSON
- `GET /api/stats/categories` — returns JSON
- `GET /api/stats/timeline` — returns JSON
- `GET /api/departments?q=min` — returns typeahead results
- `GET /api/categories?q=off` — returns typeahead results
- `GET /search?q=test&departments=X&start_date=2026-01-01` — filtered results
- `POST /api/scrape/start` — starts scrape, SSE progress works
- `GET /api/scrape/status` — returns running state

**Step 3: Fix any issues found**

**Step 4: Final commit**

```bash
git add -A
git commit -m "fix: integration fixes for dashboard and advanced search"
```

---

## Summary

| Task | What | New/Modified Files |
|------|------|--------------------|
| 1 | Stats queries | `stats.go` (new) |
| 2 | Scrape manager | `scrape_manager.go` (new), `scraper.go`, `downloader.go`, `corrigendum.go` |
| 3 | Dashboard handlers | `dashboard.go` (new) |
| 4 | Route registration | `server.go`, `main.go` |
| 5 | Filtered search | `search.go`, `db.go` |
| 6 | Dashboard template | `dashboard.tmpl` (new) |
| 7 | Advanced search UI | `index.tmpl`, `results.tmpl`, `chip-select.js` (new) |
| 8 | Navigation + polish | `index.tmpl`, `tender.tmpl`, `style.css` |
| 9 | DB indexes | `db.go` |
| 10 | Integration testing | All files |
