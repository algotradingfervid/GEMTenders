# GEM Tenders Scraper Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Go CLI tool that scrapes all ~41K ongoing tender records from bidplus.gem.gov.in, stores metadata in SQLite, and downloads bid PDFs locally.

**Architecture:** chromedp bootstraps a headless Chrome session to extract CSRF token + cookies from the main page. Then standard `net/http` calls paginate through `POST /all-bids-data` (10 records/page, ~4116 pages) to collect bid metadata into SQLite. Finally, a concurrent downloader fetches PDFs from `/showbidDocument/{b_id_parent}` with rate limiting and resume support.

**Tech Stack:** Go, chromedp, mattn/go-sqlite3, standard library (net/http, encoding/json, database/sql)

---

## API Reference (discovered via Playwright inspection)

### Session Bootstrap
- Load `GET https://bidplus.gem.gov.in/all-bids` in browser
- Extract CSRF token from HTML: hidden value matching `csrf_bd_gem_nk`
- Extract all cookies from browser context

### Listing API
- `POST https://bidplus.gem.gov.in/all-bids-data`
- Content-Type: `application/x-www-form-urlencoded`
- Body: `payload={URL-encoded JSON}&csrf_bd_gem_nk={token}`
- Payload JSON:
```json
{
  "page": 1,
  "param": {"searchBid": "", "searchType": "fullText"},
  "filter": {
    "bidStatusType": "ongoing_bids",
    "byType": "all",
    "highBidValue": "",
    "byEndDate": {"from": "", "to": ""},
    "sort": "Bid-End-Date-Oldest"
  }
}
```
- Response: JSON with `response.response.numFound` (total), `response.response.start` (offset), `response.response.docs[]` (array of bid records)
- 10 records per page

### Document Download
- `GET https://bidplus.gem.gov.in/showbidDocument/{b_id_parent}`
- Returns PDF with header `Content-Disposition: attachment; filename="GeM-Bidding-{id}.pdf"`
- Requires same session cookies

### Key Record Fields from API
| Field | Type | Description |
|-------|------|-------------|
| `id` | string | Solr document ID |
| `b_id` | []int | Bid ID |
| `b_bid_number` | []string | RA number (e.g., GEM/2026/R/643199) |
| `b_bid_number_parent` | []string | Parent bid number (e.g., GEM/2025/B/7001990) |
| `b_id_parent` | []int | Parent bid ID — used for document download URL |
| `b_category_name` | []string | Item/category description |
| `b_total_quantity` | []int | Total quantity |
| `b_status` | []int | Bid status |
| `b_bid_type` | []int | Bid type |
| `b_type` | []int | Type |
| `final_start_date_sort` | []string | Start date (ISO 8601) |
| `final_end_date_sort` | []string | End date (ISO 8601) |
| `is_high_value` | []bool | High value flag |
| `ba_official_details_minName` | []string | Ministry name |
| `ba_official_details_deptName` | []string | Department name |
| `ba_is_global_tendering` | []int | Global tendering flag |
| `is_rc_bid` | []int | Rate contract bid flag |

---

## Project Structure

```
GEMTenders/
├── main.go              # Entry point, CLI flags, orchestration
├── session.go           # chromedp-based CSRF + cookie extraction
├── scraper.go           # Paginated API calls to collect bid listings
├── downloader.go        # Concurrent PDF downloader with resume
├── db.go                # SQLite schema, insert, query operations
├── models.go            # Structs for API response and bid records
├── db_test.go           # Tests for DB operations
├── models_test.go       # Tests for JSON parsing
├── go.mod
├── go.sum
├── gems.db              # SQLite database (created at runtime)
└── downloads/           # PDF documents (created at runtime)
```

---

## Task 1: Project Setup and Data Models

**Files:**
- Create: `go.mod`
- Create: `models.go`
- Create: `models_test.go`

**Step 1: Initialize Go module and install dependencies**

```bash
cd /Users/narendhupati/Documents/GEMTenders
go mod init gemtenders
go get github.com/chromedp/chromedp
go get github.com/mattn/go-sqlite3
```

**Step 2: Create models.go with all data structures**

```go
package main

import "net/http"

// Session holds CSRF token and cookies from browser bootstrap
type Session struct {
	CSRFToken string
	Cookies   []*http.Cookie
}

// APIPayload is the POST body for /all-bids-data
type APIPayload struct {
	Page   int       `json:"page"`
	Param  APIParam  `json:"param"`
	Filter APIFilter `json:"filter"`
}

type APIParam struct {
	SearchBid  string `json:"searchBid"`
	SearchType string `json:"searchType"`
}

type APIFilter struct {
	BidStatusType string       `json:"bidStatusType"`
	ByType        string       `json:"byType"`
	HighBidValue  string       `json:"highBidValue"`
	ByEndDate     APIDateRange `json:"byEndDate"`
	Sort          string       `json:"sort"`
}

type APIDateRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// APIResponse is the top-level JSON response
type APIResponse struct {
	Status   int    `json:"status"`
	Code     int    `json:"code"`
	Message  string `json:"message"`
	Response struct {
		Response struct {
			NumFound      int       `json:"numFound"`
			Start         int       `json:"start"`
			NumFoundExact bool      `json:"numFoundExact"`
			Docs          []BidDoc  `json:"docs"`
		} `json:"response"`
	} `json:"response"`
}

// BidDoc is a single bid record from the API
// All fields come as arrays from Solr — we flatten them during DB insert
type BidDoc struct {
	ID               string   `json:"id"`
	BidID            []int    `json:"b_id"`
	BidNumber        []string `json:"b_bid_number"`
	BidNumberParent  []string `json:"b_bid_number_parent"`
	BidIDParent      []int    `json:"b_id_parent"`
	CategoryName     []string `json:"b_category_name"`
	TotalQuantity    []int    `json:"b_total_quantity"`
	Status           []int    `json:"b_status"`
	BidType          []int    `json:"b_bid_type"`
	Type             []int    `json:"b_type"`
	IsBunch          []int    `json:"b_is_bunch"`
	BidToRA          []int    `json:"b_bid_to_ra"`
	StartDate        []string `json:"final_start_date_sort"`
	EndDate          []string `json:"final_end_date_sort"`
	IsHighValue      []bool   `json:"is_high_value"`
	MinistryName     []string `json:"ba_official_details_minName"`
	DepartmentName   []string `json:"ba_official_details_deptName"`
	IsGlobalTender   []int    `json:"ba_is_global_tendering"`
	IsRCBid          []int    `json:"is_rc_bid"`
	IsCustomItem     []int    `json:"b_is_custom_item"`
}

// Helper to safely get first element from Solr arrays
func firstStr(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func firstInt(s []int) int {
	if len(s) > 0 {
		return s[0]
	}
	return 0
}

func firstBool(s []bool) bool {
	if len(s) > 0 {
		return s[0]
	}
	return false
}
```

**Step 3: Write test for JSON parsing**

```go
package main

import (
	"encoding/json"
	"testing"
)

func TestAPIResponseParsing(t *testing.T) {
	raw := `{"status":1,"code":200,"message":"Bid result","response":{"response":{"numFound":41158,"start":0,"numFoundExact":true,"docs":[{"id":"9119628","b_id":[9119628],"b_bid_number":["GEM/2026/R/643199"],"b_category_name":["Tea CTC 500 Gms Pack"],"b_total_quantity":[2600],"b_status":[1],"b_bid_type":[2],"b_is_bunch":[6],"b_type":[0],"b_bid_to_ra":[1],"final_start_date_sort":["2026-03-14T18:00:00Z"],"final_end_date_sort":["2026-03-17T14:11:14Z"],"b_is_custom_item":[0],"b_bid_number_parent":["GEM/2025/B/7001990"],"b_id_parent":[8714749],"is_high_value":[true],"b_ra_to_bid":[1],"ra_b_status":[1],"ba_official_details_minName":["Ministry of Defence"],"ba_official_details_deptName":["Department of Military Affairs"],"ba_is_global_tendering":[0],"is_rc_bid":[0]}]}}}`

	var resp APIResponse
	err := json.Unmarshal([]byte(raw), &resp)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if resp.Response.Response.NumFound != 41158 {
		t.Errorf("expected numFound=41158, got %d", resp.Response.Response.NumFound)
	}
	if len(resp.Response.Response.Docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(resp.Response.Response.Docs))
	}

	doc := resp.Response.Response.Docs[0]
	if doc.ID != "9119628" {
		t.Errorf("expected id=9119628, got %s", doc.ID)
	}
	if firstInt(doc.BidIDParent) != 8714749 {
		t.Errorf("expected b_id_parent=8714749, got %d", firstInt(doc.BidIDParent))
	}
	if firstStr(doc.BidNumberParent) != "GEM/2025/B/7001990" {
		t.Errorf("expected parent bid number GEM/2025/B/7001990, got %s", firstStr(doc.BidNumberParent))
	}
	if firstStr(doc.MinistryName) != "Ministry of Defence" {
		t.Errorf("expected Ministry of Defence, got %s", firstStr(doc.MinistryName))
	}
}

func TestFirstHelpers(t *testing.T) {
	if firstStr(nil) != "" {
		t.Error("firstStr(nil) should be empty")
	}
	if firstStr([]string{"a", "b"}) != "a" {
		t.Error("firstStr should return first element")
	}
	if firstInt(nil) != 0 {
		t.Error("firstInt(nil) should be 0")
	}
	if firstBool(nil) != false {
		t.Error("firstBool(nil) should be false")
	}
}
```

**Step 4: Run tests**

```bash
go test -v -run TestAPI
go test -v -run TestFirst
```
Expected: PASS

**Step 5: Commit**

```bash
git init
git add go.mod go.sum models.go models_test.go
git commit -m "feat: initialize project with data models and JSON parsing tests"
```

---

## Task 2: SQLite Database Layer

**Files:**
- Create: `db.go`
- Create: `db_test.go`

**Step 1: Create db.go with schema and operations**

```go
package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "github.com/mattn/go-sqlite3"
)

const createTableSQL = `
CREATE TABLE IF NOT EXISTS bids (
	id TEXT PRIMARY KEY,
	bid_id INTEGER,
	bid_number TEXT,
	bid_number_parent TEXT,
	bid_id_parent INTEGER,
	category_name TEXT,
	total_quantity INTEGER,
	status INTEGER,
	bid_type INTEGER,
	type INTEGER,
	is_bunch INTEGER,
	bid_to_ra INTEGER,
	start_date TEXT,
	end_date TEXT,
	is_high_value INTEGER,
	ministry_name TEXT,
	department_name TEXT,
	is_global_tender INTEGER,
	is_rc_bid INTEGER,
	is_custom_item INTEGER,
	pdf_downloaded INTEGER DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_bid_id_parent ON bids(bid_id_parent);
CREATE INDEX IF NOT EXISTS idx_pdf_downloaded ON bids(pdf_downloaded);
CREATE INDEX IF NOT EXISTS idx_bid_number ON bids(bid_number);
`

func InitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec(createTableSQL); err != nil {
		return nil, fmt.Errorf("create table: %w", err)
	}
	return db, nil
}

func InsertBid(db *sql.DB, doc BidDoc) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO bids (
			id, bid_id, bid_number, bid_number_parent, bid_id_parent,
			category_name, total_quantity, status, bid_type, type,
			is_bunch, bid_to_ra, start_date, end_date, is_high_value,
			ministry_name, department_name, is_global_tender, is_rc_bid, is_custom_item
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		doc.ID,
		firstInt(doc.BidID),
		firstStr(doc.BidNumber),
		firstStr(doc.BidNumberParent),
		firstInt(doc.BidIDParent),
		firstStr(doc.CategoryName),
		firstInt(doc.TotalQuantity),
		firstInt(doc.Status),
		firstInt(doc.BidType),
		firstInt(doc.Type),
		firstInt(doc.IsBunch),
		firstInt(doc.BidToRA),
		firstStr(doc.StartDate),
		firstStr(doc.EndDate),
		boolToInt(firstBool(doc.IsHighValue)),
		firstStr(doc.MinistryName),
		firstStr(doc.DepartmentName),
		firstInt(doc.IsGlobalTender),
		firstInt(doc.IsRCBid),
		firstInt(doc.IsCustomItem),
	)
	return err
}

func InsertBidsBatch(db *sql.DB, docs []BidDoc) (int, error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT OR IGNORE INTO bids (
			id, bid_id, bid_number, bid_number_parent, bid_id_parent,
			category_name, total_quantity, status, bid_type, type,
			is_bunch, bid_to_ra, start_date, end_date, is_high_value,
			ministry_name, department_name, is_global_tender, is_rc_bid, is_custom_item
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	inserted := 0
	for _, doc := range docs {
		res, err := stmt.Exec(
			doc.ID,
			firstInt(doc.BidID),
			firstStr(doc.BidNumber),
			firstStr(doc.BidNumberParent),
			firstInt(doc.BidIDParent),
			firstStr(doc.CategoryName),
			firstInt(doc.TotalQuantity),
			firstInt(doc.Status),
			firstInt(doc.BidType),
			firstInt(doc.Type),
			firstInt(doc.IsBunch),
			firstInt(doc.BidToRA),
			firstStr(doc.StartDate),
			firstStr(doc.EndDate),
			boolToInt(firstBool(doc.IsHighValue)),
			firstStr(doc.MinistryName),
			firstStr(doc.DepartmentName),
			firstInt(doc.IsGlobalTender),
			firstInt(doc.IsRCBid),
			firstInt(doc.IsCustomItem),
		)
		if err != nil {
			log.Printf("insert error for id=%s: %v", doc.ID, err)
			continue
		}
		n, _ := res.RowsAffected()
		inserted += int(n)
	}

	return inserted, tx.Commit()
}

func MarkPDFDownloaded(db *sql.DB, bidIDParent int) error {
	_, err := db.Exec("UPDATE bids SET pdf_downloaded = 1 WHERE bid_id_parent = ?", bidIDParent)
	return err
}

func GetPendingDownloads(db *sql.DB) ([]int, error) {
	rows, err := db.Query("SELECT DISTINCT bid_id_parent FROM bids WHERE pdf_downloaded = 0 AND bid_id_parent > 0")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func GetBidCount(db *sql.DB) (total int, downloaded int, err error) {
	err = db.QueryRow("SELECT COUNT(*) FROM bids").Scan(&total)
	if err != nil {
		return
	}
	err = db.QueryRow("SELECT COUNT(*) FROM bids WHERE pdf_downloaded = 1").Scan(&downloaded)
	return
}

func GetLastScrapedPage(db *sql.DB) int {
	var count int
	db.QueryRow("SELECT COUNT(*) FROM bids").Scan(&count)
	if count == 0 {
		return 0
	}
	return count / 10 // 10 records per page
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
```

**Step 2: Write db_test.go**

```go
package main

import (
	"os"
	"testing"
)

func TestInitDB(t *testing.T) {
	dbPath := "/tmp/test_gem.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	// Verify table exists
	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='bids'").Scan(&name)
	if err != nil {
		t.Fatalf("table not created: %v", err)
	}
	if name != "bids" {
		t.Errorf("expected table 'bids', got '%s'", name)
	}
}

func TestInsertAndQuery(t *testing.T) {
	dbPath := "/tmp/test_gem2.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	doc := BidDoc{
		ID:              "123",
		BidID:           []int{123},
		BidNumber:       []string{"GEM/2026/R/100"},
		BidNumberParent: []string{"GEM/2025/B/200"},
		BidIDParent:     []int{456},
		CategoryName:    []string{"Test Item"},
		TotalQuantity:   []int{100},
		Status:          []int{1},
		MinistryName:    []string{"Ministry of Test"},
		DepartmentName:  []string{"Dept of Test"},
	}

	err = InsertBid(db, doc)
	if err != nil {
		t.Fatalf("InsertBid failed: %v", err)
	}

	total, downloaded, err := GetBidCount(db)
	if err != nil {
		t.Fatalf("GetBidCount failed: %v", err)
	}
	if total != 1 {
		t.Errorf("expected total=1, got %d", total)
	}
	if downloaded != 0 {
		t.Errorf("expected downloaded=0, got %d", downloaded)
	}

	// Test pending downloads
	ids, err := GetPendingDownloads(db)
	if err != nil {
		t.Fatalf("GetPendingDownloads failed: %v", err)
	}
	if len(ids) != 1 || ids[0] != 456 {
		t.Errorf("expected [456], got %v", ids)
	}

	// Mark as downloaded
	err = MarkPDFDownloaded(db, 456)
	if err != nil {
		t.Fatalf("MarkPDFDownloaded failed: %v", err)
	}

	_, downloaded, _ = GetBidCount(db)
	if downloaded != 1 {
		t.Errorf("expected downloaded=1, got %d", downloaded)
	}
}

func TestInsertBatchAndDuplicates(t *testing.T) {
	dbPath := "/tmp/test_gem3.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	docs := []BidDoc{
		{ID: "1", BidID: []int{1}, BidIDParent: []int{10}},
		{ID: "2", BidID: []int{2}, BidIDParent: []int{20}},
		{ID: "1", BidID: []int{1}, BidIDParent: []int{10}}, // duplicate
	}

	inserted, err := InsertBidsBatch(db, docs)
	if err != nil {
		t.Fatalf("InsertBidsBatch failed: %v", err)
	}
	if inserted != 2 {
		t.Errorf("expected 2 inserted (1 duplicate ignored), got %d", inserted)
	}
}
```

**Step 3: Run tests**

```bash
CGO_ENABLED=1 go test -v -run TestInitDB
CGO_ENABLED=1 go test -v -run TestInsert
```
Expected: PASS

**Step 4: Commit**

```bash
git add db.go db_test.go
git commit -m "feat: add SQLite database layer with batch insert and resume support"
```

---

## Task 3: Session Bootstrap with chromedp

**Files:**
- Create: `session.go`

**Step 1: Create session.go**

```go
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

const baseURL = "https://bidplus.gem.gov.in"

func NewSession() (*Session, error) {
	log.Println("Bootstrapping session via headless Chrome...")

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.UserAgent("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	// Set timeout for the whole operation
	ctx, cancel = context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var csrfToken string
	var pageHTML string

	// Navigate and extract CSRF token
	err := chromedp.Run(ctx,
		chromedp.Navigate(baseURL+"/all-bids"),
		chromedp.WaitReady("body"),
		chromedp.Sleep(3*time.Second),
		chromedp.OuterHTML("html", &pageHTML),
	)
	if err != nil {
		return nil, fmt.Errorf("navigate: %w", err)
	}

	// Extract CSRF token from HTML
	// Pattern: csrf_bd_gem_nk appears as a hidden value in the page JS
	csrfToken = extractCSRF(pageHTML)
	if csrfToken == "" {
		return nil, fmt.Errorf("could not extract CSRF token from page")
	}
	log.Printf("CSRF token: %s", csrfToken)

	// Extract cookies
	var chromeCookies []*network.Cookie
	err = chromedp.Run(ctx,
		chromedp.ActionFunc(func(ctx context.Context) error {
			cookies, err := network.GetCookies().Do(ctx)
			if err != nil {
				return err
			}
			chromeCookies = cookies
			return nil
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("get cookies: %w", err)
	}

	// Convert chrome cookies to http.Cookie
	httpCookies := make([]*http.Cookie, len(chromeCookies))
	for i, c := range chromeCookies {
		httpCookies[i] = &http.Cookie{
			Name:   c.Name,
			Value:  c.Value,
			Domain: c.Domain,
			Path:   c.Path,
		}
	}
	log.Printf("Extracted %d cookies", len(httpCookies))

	return &Session{
		CSRFToken: csrfToken,
		Cookies:   httpCookies,
	}, nil
}

func extractCSRF(html string) string {
	// The CSRF token appears in the JS as part of the AJAX call:
	// csrf_bd_gem_nk=<token>
	// It's also embedded in the page HTML
	markers := []string{"csrf_bd_gem_nk"}
	for _, marker := range markers {
		idx := strings.Index(html, marker)
		if idx == -1 {
			continue
		}
		// Look for the value after the marker
		rest := html[idx+len(marker):]
		// Skip delimiter characters (=, ', ", :, space)
		start := -1
		for i, c := range rest {
			if c != '=' && c != '\'' && c != '"' && c != ':' && c != ' ' {
				start = i
				break
			}
		}
		if start == -1 {
			continue
		}
		// Read until delimiter
		end := start
		for end < len(rest) {
			c := rest[end]
			if c == '"' || c == '\'' || c == '&' || c == '<' || c == ' ' || c == '}' || c == ',' {
				break
			}
			end++
		}
		token := rest[start:end]
		if len(token) > 10 {
			return token
		}
	}
	return ""
}
```

**Step 2: Commit**

```bash
git add session.go
git commit -m "feat: add chromedp session bootstrap for CSRF and cookie extraction"
```

---

## Task 4: Paginated Bid Listing Scraper

**Files:**
- Create: `scraper.go`

**Step 1: Create scraper.go**

```go
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"time"
)

func DefaultPayload(page int) APIPayload {
	return APIPayload{
		Page: page,
		Param: APIParam{
			SearchBid:  "",
			SearchType: "fullText",
		},
		Filter: APIFilter{
			BidStatusType: "ongoing_bids",
			ByType:        "all",
			HighBidValue:  "",
			ByEndDate:     APIDateRange{From: "", To: ""},
			Sort:          "Bid-End-Date-Oldest",
		},
	}
}

func ScrapeBids(session *Session, db *sql.DB, delayMs int) error {
	startPage := GetLastScrapedPage(db) + 1
	log.Printf("Resuming from page %d", startPage)

	// First request to get total count
	totalFound, err := scrapePage(session, db, startPage)
	if err != nil {
		return fmt.Errorf("page %d: %w", startPage, err)
	}

	totalPages := (totalFound + 9) / 10 // ceil division, 10 per page
	log.Printf("Total records: %d, Total pages: %d", totalFound, totalPages)

	for page := startPage + 1; page <= totalPages; page++ {
		if delayMs > 0 {
			time.Sleep(time.Duration(delayMs) * time.Millisecond)
		}

		_, err := scrapePage(session, db, page)
		if err != nil {
			log.Printf("Error on page %d: %v (retrying in 5s)", page, err)
			time.Sleep(5 * time.Second)
			_, err = scrapePage(session, db, page)
			if err != nil {
				log.Printf("Retry failed on page %d: %v (skipping)", page, err)
				continue
			}
		}

		if page%100 == 0 {
			total, downloaded, _ := GetBidCount(db)
			log.Printf("Progress: page %d/%d, %d bids scraped, %d PDFs downloaded", page, totalPages, total, downloaded)
		}
	}

	total, _, _ := GetBidCount(db)
	log.Printf("Scraping complete. Total bids in DB: %d", total)
	return nil
}

func scrapePage(session *Session, db *sql.DB, page int) (int, error) {
	payload := DefaultPayload(page)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal payload: %w", err)
	}

	// Build form data
	formData := url.Values{}
	formData.Set("payload", string(payloadJSON))
	formData.Set("csrf_bd_gem_nk", session.CSRFToken)

	req, err := http.NewRequest("POST", baseURL+"/all-bids-data",
		strings.NewReader(formData.Encode()))
	if err != nil {
		return 0, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Referer", baseURL+"/all-bids")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	for _, cookie := range session.Cookies {
		req.AddCookie(cookie)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("read body: %w", err)
	}

	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return 0, fmt.Errorf("unmarshal: %w", err)
	}

	if apiResp.Code != 200 {
		return 0, fmt.Errorf("api error: %s", apiResp.Message)
	}

	docs := apiResp.Response.Response.Docs
	if len(docs) > 0 {
		inserted, err := InsertBidsBatch(db, docs)
		if err != nil {
			return 0, fmt.Errorf("insert: %w", err)
		}
		if inserted > 0 {
			log.Printf("Page %d: inserted %d new bids", page, inserted)
		}
	}

	return apiResp.Response.Response.NumFound, nil
}
```

Note: Add `"strings"` to the import list (it's used by `strings.NewReader`).

**Step 2: Commit**

```bash
git add scraper.go
git commit -m "feat: add paginated bid listing scraper with retry and resume"
```

---

## Task 5: PDF Document Downloader

**Files:**
- Create: `downloader.go`

**Step 1: Create downloader.go**

```go
package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

func DownloadPDFs(session *Session, db *sql.DB, downloadDir string, concurrency int, delayMs int) error {
	if err := os.MkdirAll(downloadDir, 0755); err != nil {
		return fmt.Errorf("create download dir: %w", err)
	}

	ids, err := GetPendingDownloads(db)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}

	if len(ids) == 0 {
		log.Println("No pending PDF downloads")
		return nil
	}

	log.Printf("Downloading %d PDFs with concurrency=%d", len(ids), concurrency)

	var (
		wg        sync.WaitGroup
		sem       = make(chan struct{}, concurrency)
		completed int64
		failed    int64
		total     = len(ids)
	)

	for _, bidIDParent := range ids {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore

		go func(id int) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore

			if delayMs > 0 {
				time.Sleep(time.Duration(delayMs) * time.Millisecond)
			}

			err := downloadPDF(session, id, downloadDir)
			if err != nil {
				log.Printf("Download failed for %d: %v", id, err)
				atomic.AddInt64(&failed, 1)
				return
			}

			if err := MarkPDFDownloaded(db, id); err != nil {
				log.Printf("Failed to mark %d as downloaded: %v", id, err)
			}

			done := atomic.AddInt64(&completed, 1)
			if done%100 == 0 {
				log.Printf("PDF progress: %d/%d downloaded, %d failed", done, total, atomic.LoadInt64(&failed))
			}
		}(bidIDParent)
	}

	wg.Wait()
	log.Printf("PDF download complete: %d/%d succeeded, %d failed", completed, total, failed)
	return nil
}

func downloadPDF(session *Session, bidIDParent int, downloadDir string) error {
	url := fmt.Sprintf("%s/showbidDocument/%d", baseURL, bidIDParent)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Referer", baseURL+"/all-bids")

	for _, cookie := range session.Cookies {
		req.AddCookie(cookie)
	}

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	// Use suggested filename from Content-Disposition, fallback to bid ID
	filename := fmt.Sprintf("GeM-Bidding-%d.pdf", bidIDParent)

	destPath := filepath.Join(downloadDir, filename)

	// Skip if already exists on disk
	if _, err := os.Stat(destPath); err == nil {
		return nil
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(destPath) // clean up partial file
		return fmt.Errorf("write file: %w", err)
	}

	return nil
}
```

**Step 2: Commit**

```bash
git add downloader.go
git commit -m "feat: add concurrent PDF downloader with resume and rate limiting"
```

---

## Task 6: Main Entry Point and CLI

**Files:**
- Create: `main.go`

**Step 1: Create main.go**

```go
package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	dbPath := flag.String("db", "gems.db", "SQLite database path")
	downloadDir := flag.String("downloads", "downloads", "PDF download directory")
	concurrency := flag.Int("concurrency", 3, "Number of concurrent PDF downloads")
	delayMs := flag.Int("delay", 500, "Delay between API requests in milliseconds")
	skipScrape := flag.Bool("skip-scrape", false, "Skip scraping, only download PDFs")
	skipDownload := flag.Bool("skip-download", false, "Skip PDF downloads, only scrape listings")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lshortfile)

	// Initialize database
	db, err := InitDB(*dbPath)
	if err != nil {
		log.Fatalf("Failed to init DB: %v", err)
	}
	defer db.Close()

	// Bootstrap browser session
	session, err := NewSession()
	if err != nil {
		log.Fatalf("Failed to create session: %v", err)
	}

	// Phase 1: Scrape bid listings
	if !*skipScrape {
		log.Println("=== Phase 1: Scraping bid listings ===")
		if err := ScrapeBids(session, db, *delayMs); err != nil {
			log.Printf("Scraping error: %v", err)
			os.Exit(1)
		}
	}

	// Phase 2: Download PDFs
	if !*skipDownload {
		log.Println("=== Phase 2: Downloading bid PDFs ===")
		if err := DownloadPDFs(session, db, *downloadDir, *concurrency, *delayMs); err != nil {
			log.Printf("Download error: %v", err)
			os.Exit(1)
		}
	}

	// Print summary
	total, downloaded, _ := GetBidCount(db)
	log.Printf("=== Done === Total bids: %d, PDFs downloaded: %d", total, downloaded)
}
```

**Step 2: Build and verify**

```bash
CGO_ENABLED=1 go build -o gemscraper .
./gemscraper --help
```

Expected output:
```
Usage of ./gemscraper:
  -concurrency int    Number of concurrent PDF downloads (default 3)
  -db string          SQLite database path (default "gems.db")
  -delay int          Delay between API requests in milliseconds (default 500)
  -downloads string   PDF download directory (default "downloads")
  -skip-download      Skip PDF downloads, only scrape listings
  -skip-scrape        Skip scraping, only download PDFs
```

**Step 3: Commit**

```bash
git add main.go
git commit -m "feat: add main entry point with CLI flags for scraper orchestration"
```

---

## Task 7: Integration Test — Scrape First 3 Pages

**Step 1: Run a limited smoke test**

```bash
# Quick test: scrape first few pages only (we'll ctrl+C after a few seconds)
./gemscraper -db /tmp/test_run.db -downloads /tmp/test_downloads -delay 1000 -skip-download
```

Watch for:
- Session bootstrap succeeds (CSRF token + cookies extracted)
- Pages are being scraped and inserted
- No parsing errors

**Step 2: Verify data in SQLite**

```bash
sqlite3 /tmp/test_run.db "SELECT COUNT(*) FROM bids;"
sqlite3 /tmp/test_run.db "SELECT bid_number_parent, category_name, ministry_name FROM bids LIMIT 5;"
```

**Step 3: Test PDF download for a few records**

```bash
./gemscraper -db /tmp/test_run.db -downloads /tmp/test_downloads -delay 1000 -skip-scrape -concurrency 2
ls -la /tmp/test_downloads/
```

**Step 4: Commit any fixes from smoke testing**

```bash
git add -A
git commit -m "fix: adjustments from integration smoke test"
```

---

## Task 8: Clean Up Inspection Scripts

**Step 1: Remove the Node.js inspection scripts**

```bash
rm -f inspect_site.js inspect_api.js inspect_bid_detail.js inspect_download.js
rm -rf node_modules package.json package-lock.json
```

**Step 2: Add .gitignore**

Create `.gitignore`:
```
gems.db
downloads/
node_modules/
*.pdf
```

**Step 3: Commit**

```bash
git add .gitignore
git commit -m "chore: clean up inspection scripts and add .gitignore"
```

---

## CLI Usage Reference

```bash
# Full run: scrape all bids + download all PDFs
./gemscraper

# Scrape listings only (no PDF downloads)
./gemscraper -skip-download

# Download PDFs only (resume after previous scrape)
./gemscraper -skip-scrape

# Custom settings
./gemscraper -concurrency 5 -delay 300 -db my_bids.db -downloads ./pdfs
```
