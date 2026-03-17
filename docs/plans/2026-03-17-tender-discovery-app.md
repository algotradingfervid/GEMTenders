# Tender Discovery App Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a web app for searching and viewing GEM tender records with embedded PDF viewer.

**Architecture:** Go + Gin serves HTMX-powered `.tmpl` templates. SQLite FTS5 provides full-text search across ~41K bids. PDFs served directly from the `downloads/` folder.

**Tech Stack:** Go, Gin, html/template (.tmpl), HTMX (CDN), SQLite FTS5, minimal CSS

---

## Existing Context

- **Database:** `gems.db` with `bids` table (41K records), columns: id, bid_id, bid_number, bid_number_parent, bid_id_parent, category_name, total_quantity, status, bid_type, start_date, end_date, is_high_value, ministry_name, department_name, pdf_downloaded
- **PDFs:** `downloads/GeM-Bidding-{id}.pdf` where id is `bid_id_parent` if >0 else `bid_id`
- **Package:** `package main` in project root, module name `gemtenders`
- **Existing files:** main.go (CLI subcommands), db.go, models.go, scraper.go, downloader.go, session.go

---

## Task 1: Install Gin and Create Web Server Skeleton

**Files:**
- Modify: `go.mod` (add gin dependency)
- Create: `web/server.go`
- Modify: `main.go` (add `serve` subcommand)

**Step 1: Install Gin**

```bash
go get github.com/gin-gonic/gin
```

**Step 2: Create web/server.go**

```go
package main

import (
	"database/sql"
	"log"

	"github.com/gin-gonic/gin"
)

func StartServer(db *sql.DB, downloadDir string, addr string) {
	r := gin.Default()

	// Load templates
	r.LoadHTMLGlob("web/templates/*")

	// Static files
	r.Static("/static", "web/static")

	// Routes
	r.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.tmpl", nil)
	})

	log.Printf("Starting server on %s", addr)
	r.Run(addr)
}
```

**Step 3: Add `serve` subcommand to main.go**

Add this case to the switch in `main()`:
```go
case "serve":
    runServeCmd(os.Args[2:])
```

Add the function:
```go
func runServeCmd(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	downloadDir := fs.String("downloads", "downloads", "PDF download directory")
	addr := fs.String("addr", ":8080", "Server listen address")
	fs.Parse(args)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	StartServer(db, *downloadDir, *addr)
}
```

Update `printUsage()` to include:
```
  serve      Start the web server for tender discovery
```

**Step 4: Create minimal index template**

Create `web/templates/index.tmpl`:
```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>GEM Tender Discovery</title>
    <script src="https://unpkg.com/htmx.org@2.0.4"></script>
    <link rel="stylesheet" href="/static/style.css">
</head>
<body>
    <header>
        <h1>GEM Tender Discovery</h1>
    </header>
    <main>
        <div class="search-container">
            <input type="search"
                   name="q"
                   placeholder="Search tenders..."
                   hx-get="/search"
                   hx-trigger="input changed delay:300ms, search"
                   hx-target="#results"
                   hx-indicator="#spinner"
                   autocomplete="off">
            <span id="spinner" class="htmx-indicator">Searching...</span>
        </div>
        <div id="results"></div>
    </main>
</body>
</html>
```

**Step 5: Create minimal CSS**

Create `web/static/style.css`:
```css
* { box-sizing: border-box; margin: 0; padding: 0; }
body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f5f5f5; color: #333; }
header { background: #1a365d; color: white; padding: 1rem 2rem; }
header h1 { font-size: 1.5rem; font-weight: 600; }
main { max-width: 1200px; margin: 2rem auto; padding: 0 1rem; }
.search-container { margin-bottom: 2rem; }
.search-container input[type="search"] { width: 100%; padding: 0.75rem 1rem; font-size: 1rem; border: 2px solid #ddd; border-radius: 8px; outline: none; }
.search-container input[type="search"]:focus { border-color: #1a365d; }
.htmx-indicator { display: none; margin-left: 0.5rem; color: #666; }
.htmx-request .htmx-indicator { display: inline; }
```

**Step 6: Build and test**

```bash
mkdir -p web/templates web/static
go build -o gemscraper .
./gemscraper serve
# Visit http://localhost:8080 — should show search box
```

**Step 7: Commit**

```bash
git add web/ main.go go.mod go.sum
git commit -m "feat: add web server skeleton with search UI"
```

---

## Task 2: FTS5 Full-Text Search

**Files:**
- Modify: `db.go` (add FTS5 table creation and search query)
- Create: `web/search.go` (search handler)
- Create: `web/templates/results.tmpl` (search result cards)

**Step 1: Add FTS5 setup and search to db.go**

Add these functions to `db.go`:

```go
func InitFTS(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS bids_fts USING fts5(
			bid_number,
			bid_number_parent,
			category_name,
			ministry_name,
			department_name,
			content='bids',
			content_rowid='rowid'
		)
	`)
	if err != nil {
		return fmt.Errorf("create FTS table: %w", err)
	}
	return nil
}

func RebuildFTS(db *sql.DB) error {
	log.Println("Rebuilding FTS index...")
	_, err := db.Exec(`INSERT INTO bids_fts(bids_fts) VALUES('rebuild')`)
	if err != nil {
		return fmt.Errorf("rebuild FTS: %w", err)
	}
	log.Println("FTS index rebuilt")
	return nil
}

type BidResult struct {
	ID             string
	BidID          int
	BidNumber      string
	BidNumberParent string
	BidIDParent    int
	CategoryName   string
	TotalQuantity  int
	StartDate      string
	EndDate        string
	IsHighValue    int
	MinistryName   string
	DepartmentName string
}

func SearchBids(db *sql.DB, query string, limit int, offset int) ([]BidResult, int, error) {
	if query == "" {
		return recentBids(db, limit, offset)
	}

	// Count total matches
	var total int
	err := db.QueryRow(`
		SELECT COUNT(*) FROM bids_fts WHERE bids_fts MATCH ?
	`, query).Scan(&total)
	if err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	rows, err := db.Query(`
		SELECT b.id, b.bid_id, b.bid_number, b.bid_number_parent, b.bid_id_parent,
		       b.category_name, b.total_quantity, b.start_date, b.end_date,
		       b.is_high_value, b.ministry_name, b.department_name
		FROM bids_fts f
		JOIN bids b ON f.rowid = b.rowid
		WHERE bids_fts MATCH ?
		ORDER BY rank
		LIMIT ? OFFSET ?
	`, query, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	return scanBidResults(rows, total)
}

func recentBids(db *sql.DB, limit int, offset int) ([]BidResult, int, error) {
	var total int
	db.QueryRow("SELECT COUNT(*) FROM bids").Scan(&total)

	rows, err := db.Query(`
		SELECT id, bid_id, bid_number, bid_number_parent, bid_id_parent,
		       category_name, total_quantity, start_date, end_date,
		       is_high_value, ministry_name, department_name
		FROM bids
		ORDER BY end_date DESC
		LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	return scanBidResults(rows, total)
}

func scanBidResults(rows *sql.Rows, total int) ([]BidResult, int, error) {
	var results []BidResult
	for rows.Next() {
		var r BidResult
		err := rows.Scan(&r.ID, &r.BidID, &r.BidNumber, &r.BidNumberParent,
			&r.BidIDParent, &r.CategoryName, &r.TotalQuantity,
			&r.StartDate, &r.EndDate, &r.IsHighValue,
			&r.MinistryName, &r.DepartmentName)
		if err != nil {
			return nil, 0, err
		}
		results = append(results, r)
	}
	return results, total, rows.Err()
}
```

**Step 2: Create search handler in web/search.go**

```go
package main

import (
	"database/sql"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func SearchHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Query("q")
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		if page < 1 {
			page = 1
		}
		limit := 20
		offset := (page - 1) * limit

		results, total, err := SearchBids(db, query, limit, offset)
		if err != nil {
			c.HTML(http.StatusInternalServerError, "results.tmpl", gin.H{
				"Error": err.Error(),
			})
			return
		}

		totalPages := (total + limit - 1) / limit

		c.HTML(http.StatusOK, "results.tmpl", gin.H{
			"Results":    results,
			"Query":      query,
			"Total":      total,
			"Page":       page,
			"TotalPages": totalPages,
			"HasPrev":    page > 1,
			"HasNext":    page < totalPages,
			"PrevPage":   page - 1,
			"NextPage":   page + 1,
		})
	}
}
```

**Step 3: Create results.tmpl**

Create `web/templates/results.tmpl`:
```html
{{ if .Error }}
<div class="error">Search error: {{ .Error }}</div>
{{ else }}
<div class="results-header">
    <span>{{ .Total }} tenders found</span>
</div>
{{ range .Results }}
<a href="/tender/{{ .ID }}" class="result-card">
    <div class="result-top">
        <span class="bid-number">{{ .BidNumber }}</span>
        {{ if eq .IsHighValue 1 }}<span class="badge high-value">High Value</span>{{ end }}
    </div>
    <div class="result-category">{{ .CategoryName }}</div>
    <div class="result-meta">
        <span>{{ .MinistryName }}</span>
        {{ if .DepartmentName }}<span> · {{ .DepartmentName }}</span>{{ end }}
    </div>
    <div class="result-dates">
        <span>Qty: {{ .TotalQuantity }}</span>
        <span>End: {{ .EndDate }}</span>
    </div>
</a>
{{ else }}
<div class="no-results">No tenders found. Try a different search term.</div>
{{ end }}

{{ if gt .TotalPages 1 }}
<div class="pagination">
    {{ if .HasPrev }}
    <button hx-get="/search?q={{ .Query }}&page={{ .PrevPage }}" hx-target="#results">← Previous</button>
    {{ end }}
    <span>Page {{ .Page }} of {{ .TotalPages }}</span>
    {{ if .HasNext }}
    <button hx-get="/search?q={{ .Query }}&page={{ .NextPage }}" hx-target="#results">Next →</button>
    {{ end }}
</div>
{{ end }}
{{ end }}
```

**Step 4: Wire up search route in web/server.go**

Update `StartServer` to add the search route and init FTS:
```go
func StartServer(db *sql.DB, downloadDir string, addr string) {
	// Init FTS
	if err := InitFTS(db); err != nil {
		log.Fatalf("Failed to init FTS: %v", err)
	}
	if err := RebuildFTS(db); err != nil {
		log.Fatalf("Failed to rebuild FTS: %v", err)
	}

	r := gin.Default()
	r.LoadHTMLGlob("web/templates/*")
	r.Static("/static", "web/static")

	r.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.tmpl", nil)
	})
	r.GET("/search", SearchHandler(db))

	log.Printf("Starting server on %s", addr)
	r.Run(addr)
}
```

**Step 5: Add CSS for result cards**

Append to `web/static/style.css`:
```css
.results-header { margin-bottom: 1rem; color: #666; font-size: 0.9rem; }
.result-card { display: block; background: white; border-radius: 8px; padding: 1rem 1.25rem; margin-bottom: 0.75rem; text-decoration: none; color: inherit; border: 1px solid #e2e8f0; transition: border-color 0.15s; }
.result-card:hover { border-color: #1a365d; }
.result-top { display: flex; align-items: center; gap: 0.5rem; margin-bottom: 0.5rem; }
.bid-number { font-weight: 600; color: #1a365d; font-size: 0.9rem; }
.badge { font-size: 0.7rem; padding: 0.15rem 0.5rem; border-radius: 4px; font-weight: 600; }
.high-value { background: #fef3c7; color: #92400e; }
.result-category { font-size: 0.95rem; margin-bottom: 0.5rem; line-height: 1.4; }
.result-meta { font-size: 0.85rem; color: #666; margin-bottom: 0.25rem; }
.result-dates { font-size: 0.85rem; color: #888; display: flex; gap: 1rem; }
.no-results { text-align: center; padding: 3rem; color: #999; }
.error { background: #fee2e2; color: #991b1b; padding: 1rem; border-radius: 8px; }
.pagination { display: flex; align-items: center; justify-content: center; gap: 1rem; margin-top: 1.5rem; }
.pagination button { padding: 0.5rem 1rem; border: 1px solid #ddd; border-radius: 6px; background: white; cursor: pointer; }
.pagination button:hover { background: #f0f0f0; }
```

**Step 6: Build and test**

```bash
go build -o gemscraper .
./gemscraper serve
# Visit http://localhost:8080
# Type "laptop" in search — should show results
```

**Step 7: Commit**

```bash
git add db.go web/
git commit -m "feat: add FTS5 search with HTMX results"
```

---

## Task 3: Tender Detail Page with PDF Viewer

**Files:**
- Modify: `db.go` (add GetBidByID function)
- Modify: `web/server.go` (add tender and pdf routes)
- Create: `web/templates/tender.tmpl`

**Step 1: Add GetBidByID to db.go**

```go
func GetBidByID(db *sql.DB, id string) (*BidResult, error) {
	var r BidResult
	err := db.QueryRow(`
		SELECT id, bid_id, bid_number, bid_number_parent, bid_id_parent,
		       category_name, total_quantity, start_date, end_date,
		       is_high_value, ministry_name, department_name
		FROM bids WHERE id = ?
	`, id).Scan(&r.ID, &r.BidID, &r.BidNumber, &r.BidNumberParent,
		&r.BidIDParent, &r.CategoryName, &r.TotalQuantity,
		&r.StartDate, &r.EndDate, &r.IsHighValue,
		&r.MinistryName, &r.DepartmentName)
	if err != nil {
		return nil, err
	}
	return &r, nil
}
```

**Step 2: Add tender detail and PDF routes to web/server.go**

Add these routes inside `StartServer`, after the search route:

```go
	r.GET("/tender/:id", func(c *gin.Context) {
		id := c.Param("id")
		bid, err := GetBidByID(db, id)
		if err != nil {
			c.HTML(404, "index.tmpl", nil)
			return
		}

		// Determine PDF download ID
		pdfID := bid.BidIDParent
		if pdfID == 0 {
			pdfID = bid.BidID
		}

		c.HTML(200, "tender.tmpl", gin.H{
			"Bid":   bid,
			"PDFID": pdfID,
		})
	})

	r.GET("/pdf/:id", func(c *gin.Context) {
		id := c.Param("id")
		filePath := downloadDir + "/GeM-Bidding-" + id + ".pdf"
		c.File(filePath)
	})
```

**Step 3: Create tender.tmpl**

Create `web/templates/tender.tmpl`:
```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{ .Bid.BidNumber }} — GEM Tender</title>
    <link rel="stylesheet" href="/static/style.css">
</head>
<body>
    <header>
        <h1><a href="/" style="color:white;text-decoration:none;">GEM Tender Discovery</a></h1>
    </header>
    <main>
        <a href="/" class="back-link">← Back to search</a>

        <div class="tender-detail">
            <div class="tender-header">
                <h2>{{ .Bid.BidNumber }}</h2>
                {{ if eq .Bid.IsHighValue 1 }}<span class="badge high-value">High Value</span>{{ end }}
            </div>

            <div class="tender-meta-grid">
                <div class="meta-item">
                    <span class="meta-label">Category</span>
                    <span class="meta-value">{{ .Bid.CategoryName }}</span>
                </div>
                <div class="meta-item">
                    <span class="meta-label">Ministry</span>
                    <span class="meta-value">{{ .Bid.MinistryName }}</span>
                </div>
                <div class="meta-item">
                    <span class="meta-label">Department</span>
                    <span class="meta-value">{{ .Bid.DepartmentName }}</span>
                </div>
                <div class="meta-item">
                    <span class="meta-label">Quantity</span>
                    <span class="meta-value">{{ .Bid.TotalQuantity }}</span>
                </div>
                {{ if .Bid.BidNumberParent }}
                <div class="meta-item">
                    <span class="meta-label">Parent Bid</span>
                    <span class="meta-value">{{ .Bid.BidNumberParent }}</span>
                </div>
                {{ end }}
                <div class="meta-item">
                    <span class="meta-label">Start Date</span>
                    <span class="meta-value">{{ .Bid.StartDate }}</span>
                </div>
                <div class="meta-item">
                    <span class="meta-label">End Date</span>
                    <span class="meta-value">{{ .Bid.EndDate }}</span>
                </div>
            </div>
        </div>

        <div class="pdf-container">
            <h3>Bid Document</h3>
            <iframe src="/pdf/{{ .PDFID }}" width="100%" height="800px"></iframe>
        </div>
    </main>
</body>
</html>
```

**Step 4: Add detail page CSS**

Append to `web/static/style.css`:
```css
.back-link { display: inline-block; margin-bottom: 1rem; color: #1a365d; text-decoration: none; font-size: 0.9rem; }
.back-link:hover { text-decoration: underline; }
.tender-detail { background: white; border-radius: 8px; padding: 1.5rem; border: 1px solid #e2e8f0; margin-bottom: 1.5rem; }
.tender-header { display: flex; align-items: center; gap: 0.75rem; margin-bottom: 1.25rem; }
.tender-header h2 { font-size: 1.25rem; color: #1a365d; }
.tender-meta-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(250px, 1fr)); gap: 1rem; }
.meta-item { display: flex; flex-direction: column; gap: 0.2rem; }
.meta-label { font-size: 0.75rem; text-transform: uppercase; color: #999; font-weight: 600; letter-spacing: 0.05em; }
.meta-value { font-size: 0.95rem; }
.pdf-container { background: white; border-radius: 8px; padding: 1.5rem; border: 1px solid #e2e8f0; }
.pdf-container h3 { margin-bottom: 1rem; font-size: 1.1rem; color: #333; }
.pdf-container iframe { border: 1px solid #e2e8f0; border-radius: 4px; }
```

**Step 5: Build and test**

```bash
go build -o gemscraper .
./gemscraper serve
# Visit http://localhost:8080
# Search for something, click a result
# Should see detail page with metadata + embedded PDF
```

**Step 6: Commit**

```bash
git add db.go web/
git commit -m "feat: add tender detail page with embedded PDF viewer"
```

---

## Task 4: Add `reindex` Subcommand

**Files:**
- Modify: `main.go` (add reindex subcommand)

**Step 1: Add reindex case to main.go switch**

```go
case "reindex":
    runReindexCmd(os.Args[2:])
```

Add function:
```go
func runReindexCmd(args []string) {
	fs := flag.NewFlagSet("reindex", flag.ExitOnError)
	dbPath := fs.String("db", "gems.db", "SQLite database path")
	fs.Parse(args)

	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	if err := InitFTS(db); err != nil {
		log.Fatalf("Failed to init FTS: %v", err)
	}
	if err := RebuildFTS(db); err != nil {
		log.Fatalf("Failed to rebuild FTS: %v", err)
	}

	total, downloaded, _ := GetBidCount(db)
	log.Printf("Reindex complete. Total bids: %d, PDFs: %d", total, downloaded)
}
```

Update `printUsage()`:
```
  reindex    Rebuild the full-text search index
```

**Step 2: Build and test**

```bash
go build -o gemscraper .
./gemscraper reindex
# Should output: Rebuilding FTS index... Reindex complete.
```

**Step 3: Commit**

```bash
git add main.go
git commit -m "feat: add reindex subcommand for FTS rebuild"
```
