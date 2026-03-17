# Corrigendum & Representation Tracking — Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Track corrigendum/representation updates for active GEM bids, store the data with delta detection, download corrigendum PDFs, and display them in the web UI.

**Architecture:** After the existing bid-listing scrape pass, a second pass checks all active bids (end_date > now) via GEM's other-details API. If corrigendum/representation exists and differs from what's stored, the new HTML is saved, download links are extracted, and the bid's end_date is updated. A separate download step fetches corrigendum PDFs. The web UI renders raw HTML in tabs on the tender detail page.

**Tech Stack:** Go, SQLite, net/http (existing session pool), regexp/strings for HTML parsing, gin templates for UI.

---

### Task 1: Database schema — new tables and migration

**Files:**
- Modify: `db.go:11-40` (add new CREATE TABLE statements to `createTableSQL`)
- Modify: `db.go:42-51` (add migration for `end_date_original` column in `InitDB`)

**Step 1: Write the failing test**

Add to `db_test.go`:

```go
func TestCorrigendumTablesExist(t *testing.T) {
	dbPath := "/tmp/test_gem_corr.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	// bid_other_details table
	var name string
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='bid_other_details'").Scan(&name)
	if err != nil {
		t.Fatalf("bid_other_details table not created: %v", err)
	}

	// corrigendum_documents table
	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='corrigendum_documents'").Scan(&name)
	if err != nil {
		t.Fatalf("corrigendum_documents table not created: %v", err)
	}

	// end_date_original column exists on bids
	_, err = db.Exec("SELECT end_date_original FROM bids LIMIT 1")
	if err != nil {
		t.Fatalf("end_date_original column not found: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test -run TestCorrigendumTablesExist -v`
Expected: FAIL — tables don't exist yet

**Step 3: Add schema to `db.go`**

Add to the `createTableSQL` const (after the existing bids indexes):

```sql
CREATE TABLE IF NOT EXISTS bid_other_details (
	bid_id INTEGER PRIMARY KEY,
	has_corrigendum INTEGER DEFAULT 0,
	has_representation INTEGER DEFAULT 0,
	corrigendum_html TEXT DEFAULT '',
	representation_html TEXT DEFAULT '',
	corrigendum_count INTEGER DEFAULT 0,
	latest_end_date TEXT DEFAULT '',
	last_checked TEXT DEFAULT ''
);

CREATE TABLE IF NOT EXISTS corrigendum_documents (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	bid_id INTEGER NOT NULL,
	corrigendum_id INTEGER NOT NULL,
	download_url TEXT NOT NULL,
	modified_on TEXT DEFAULT '',
	downloaded INTEGER DEFAULT 0,
	UNIQUE(bid_id, download_url)
);

CREATE INDEX IF NOT EXISTS idx_corr_doc_bid ON corrigendum_documents(bid_id);
CREATE INDEX IF NOT EXISTS idx_corr_doc_pending ON corrigendum_documents(downloaded);
```

Add the `end_date_original` migration inside `InitDB` after the `Exec(createTableSQL)` call:

```go
// Migration: add end_date_original column if missing
db.Exec("ALTER TABLE bids ADD COLUMN end_date_original TEXT DEFAULT ''")
// Backfill: set end_date_original = end_date where not yet set
db.Exec("UPDATE bids SET end_date_original = end_date WHERE end_date_original = '' AND end_date != ''")
```

**Step 4: Run test to verify it passes**

Run: `go test -run TestCorrigendumTablesExist -v`
Expected: PASS

**Step 5: Run all existing tests to verify no regressions**

Run: `go test -v`
Expected: All PASS

**Step 6: Commit**

```bash
git add db.go db_test.go
git commit -m "feat: add corrigendum/representation schema and end_date_original migration"
```

---

### Task 2: Models — new structs and DB helpers

**Files:**
- Modify: `models.go` (add new structs at the end)
- Modify: `db.go` (add new query functions)
- Modify: `db_test.go` (add tests)

**Step 1: Write the failing test**

Add to `db_test.go`:

```go
func TestUpsertBidOtherDetails(t *testing.T) {
	dbPath := "/tmp/test_gem_upsert.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	// Insert
	details := BidOtherDetails{
		BidID:              123,
		HasCorrigendum:     1,
		HasRepresentation:  0,
		CorrigendumHTML:    "<div>test</div>",
		RepresentationHTML: "",
		CorrigendumCount:   1,
		LatestEndDate:      "2026-04-01T09:00:00",
		LastChecked:        "2026-03-17T12:00:00",
	}
	err = UpsertBidOtherDetails(db, details)
	if err != nil {
		t.Fatalf("UpsertBidOtherDetails (insert) failed: %v", err)
	}

	// Read back
	got, err := GetBidOtherDetails(db, 123)
	if err != nil {
		t.Fatalf("GetBidOtherDetails failed: %v", err)
	}
	if got.CorrigendumHTML != "<div>test</div>" {
		t.Errorf("expected HTML '<div>test</div>', got '%s'", got.CorrigendumHTML)
	}
	if got.HasCorrigendum != 1 {
		t.Errorf("expected has_corrigendum=1, got %d", got.HasCorrigendum)
	}

	// Update (upsert)
	details.CorrigendumHTML = "<div>updated</div>"
	details.CorrigendumCount = 2
	err = UpsertBidOtherDetails(db, details)
	if err != nil {
		t.Fatalf("UpsertBidOtherDetails (update) failed: %v", err)
	}

	got, err = GetBidOtherDetails(db, 123)
	if err != nil {
		t.Fatalf("GetBidOtherDetails after update failed: %v", err)
	}
	if got.CorrigendumHTML != "<div>updated</div>" {
		t.Errorf("expected updated HTML, got '%s'", got.CorrigendumHTML)
	}
	if got.CorrigendumCount != 2 {
		t.Errorf("expected count=2, got %d", got.CorrigendumCount)
	}
}

func TestInsertCorrigendumDoc(t *testing.T) {
	dbPath := "/tmp/test_gem_corrdoc.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	doc := CorrigendumDoc{
		BidID:         123,
		CorrigendumID: 456,
		DownloadURL:   "/bidding/bid/showcorrigendumpdf/456/123",
		ModifiedOn:    "2026-03-11 12:47:58",
	}
	err = InsertCorrigendumDoc(db, doc)
	if err != nil {
		t.Fatalf("InsertCorrigendumDoc failed: %v", err)
	}

	// Duplicate should not error (INSERT OR IGNORE)
	err = InsertCorrigendumDoc(db, doc)
	if err != nil {
		t.Fatalf("InsertCorrigendumDoc duplicate failed: %v", err)
	}

	// Check pending
	pending, err := GetPendingCorrigendumDownloads(db)
	if err != nil {
		t.Fatalf("GetPendingCorrigendumDownloads failed: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].CorrigendumID != 456 {
		t.Errorf("expected corrigendum_id=456, got %d", pending[0].CorrigendumID)
	}
}

func TestGetActiveBidIDs(t *testing.T) {
	dbPath := "/tmp/test_gem_active.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	// Insert one future bid, one past bid
	InsertBid(db, BidDoc{
		ID:    "1",
		BidID: []int{1},
		EndDate: []string{"2099-12-31T00:00:00Z"},
	})
	InsertBid(db, BidDoc{
		ID:    "2",
		BidID: []int{2},
		EndDate: []string{"2020-01-01T00:00:00Z"},
	})

	ids, err := GetActiveBidIDs(db)
	if err != nil {
		t.Fatalf("GetActiveBidIDs failed: %v", err)
	}
	if len(ids) != 1 || ids[0] != 1 {
		t.Errorf("expected [1], got %v", ids)
	}
}

func TestUpdateBidEndDate(t *testing.T) {
	dbPath := "/tmp/test_gem_enddate.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	InsertBid(db, BidDoc{
		ID:      "1",
		BidID:   []int{1},
		EndDate: []string{"2026-03-18T09:00:00Z"},
	})

	err = UpdateBidEndDate(db, 1, "2026-04-01T09:00:00Z")
	if err != nil {
		t.Fatalf("UpdateBidEndDate failed: %v", err)
	}

	var endDate string
	db.QueryRow("SELECT end_date FROM bids WHERE bid_id = 1").Scan(&endDate)
	if endDate != "2026-04-01T09:00:00Z" {
		t.Errorf("expected updated end_date, got '%s'", endDate)
	}

	// Original should be preserved
	var orig string
	db.QueryRow("SELECT end_date_original FROM bids WHERE bid_id = 1").Scan(&orig)
	if orig != "2026-03-18T09:00:00Z" {
		t.Errorf("expected original end_date preserved, got '%s'", orig)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -run "TestUpsertBidOtherDetails|TestInsertCorrigendumDoc|TestGetActiveBidIDs|TestUpdateBidEndDate" -v`
Expected: FAIL — functions don't exist yet

**Step 3: Add structs to `models.go`**

Append to end of `models.go`:

```go
// OtherDetailsResponse is the JSON from POST /public-bid-other-details/{bid_id}
type OtherDetailsResponse struct {
	Status   int    `json:"status"`
	Code     int    `json:"code"`
	Message  string `json:"message"`
	Response struct {
		Corrigendum    bool `json:"corrigendum"`
		Representation bool `json:"representation"`
	} `json:"response"`
}

// BidOtherDetails maps to the bid_other_details table
type BidOtherDetails struct {
	BidID              int
	HasCorrigendum     int
	HasRepresentation  int
	CorrigendumHTML    string
	RepresentationHTML string
	CorrigendumCount   int
	LatestEndDate      string
	LastChecked        string
}

// CorrigendumDoc maps to the corrigendum_documents table
type CorrigendumDoc struct {
	ID            int
	BidID         int
	CorrigendumID int
	DownloadURL   string
	ModifiedOn    string
	Downloaded    int
}
```

**Step 4: Add DB functions to `db.go`**

Append to end of `db.go`:

```go
func GetActiveBidIDs(db *sql.DB) ([]int, error) {
	rows, err := db.Query(`
		SELECT DISTINCT bid_id FROM bids
		WHERE end_date > datetime('now')
		AND bid_id > 0`)
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

func GetBidOtherDetails(db *sql.DB, bidID int) (*BidOtherDetails, error) {
	var d BidOtherDetails
	err := db.QueryRow(`
		SELECT bid_id, has_corrigendum, has_representation,
		       corrigendum_html, representation_html,
		       corrigendum_count, latest_end_date, last_checked
		FROM bid_other_details WHERE bid_id = ?`, bidID).Scan(
		&d.BidID, &d.HasCorrigendum, &d.HasRepresentation,
		&d.CorrigendumHTML, &d.RepresentationHTML,
		&d.CorrigendumCount, &d.LatestEndDate, &d.LastChecked)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func UpsertBidOtherDetails(db *sql.DB, d BidOtherDetails) error {
	_, err := db.Exec(`
		INSERT INTO bid_other_details (
			bid_id, has_corrigendum, has_representation,
			corrigendum_html, representation_html,
			corrigendum_count, latest_end_date, last_checked
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(bid_id) DO UPDATE SET
			has_corrigendum = excluded.has_corrigendum,
			has_representation = excluded.has_representation,
			corrigendum_html = excluded.corrigendum_html,
			representation_html = excluded.representation_html,
			corrigendum_count = excluded.corrigendum_count,
			latest_end_date = excluded.latest_end_date,
			last_checked = excluded.last_checked`,
		d.BidID, d.HasCorrigendum, d.HasRepresentation,
		d.CorrigendumHTML, d.RepresentationHTML,
		d.CorrigendumCount, d.LatestEndDate, d.LastChecked)
	return err
}

func InsertCorrigendumDoc(db *sql.DB, doc CorrigendumDoc) error {
	_, err := db.Exec(`
		INSERT OR IGNORE INTO corrigendum_documents (
			bid_id, corrigendum_id, download_url, modified_on
		) VALUES (?, ?, ?, ?)`,
		doc.BidID, doc.CorrigendumID, doc.DownloadURL, doc.ModifiedOn)
	return err
}

func GetPendingCorrigendumDownloads(db *sql.DB) ([]CorrigendumDoc, error) {
	rows, err := db.Query(`
		SELECT id, bid_id, corrigendum_id, download_url, modified_on
		FROM corrigendum_documents WHERE downloaded = 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []CorrigendumDoc
	for rows.Next() {
		var d CorrigendumDoc
		if err := rows.Scan(&d.ID, &d.BidID, &d.CorrigendumID, &d.DownloadURL, &d.ModifiedOn); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

func MarkCorrigendumDownloaded(db *sql.DB, id int) error {
	_, err := db.Exec("UPDATE corrigendum_documents SET downloaded = 1 WHERE id = ?", id)
	return err
}

func UpdateBidEndDate(db *sql.DB, bidID int, newEndDate string) error {
	_, err := db.Exec(`
		UPDATE bids SET end_date = ?
		WHERE bid_id = ? AND end_date != ?`,
		newEndDate, bidID, newEndDate)
	return err
}

func GetCorrigendumStats(db *sql.DB) (checked int, withCorr int, docsTotal int, docsDownloaded int, err error) {
	db.QueryRow("SELECT COUNT(*) FROM bid_other_details").Scan(&checked)
	db.QueryRow("SELECT COUNT(*) FROM bid_other_details WHERE has_corrigendum = 1").Scan(&withCorr)
	db.QueryRow("SELECT COUNT(*) FROM corrigendum_documents").Scan(&docsTotal)
	db.QueryRow("SELECT COUNT(*) FROM corrigendum_documents WHERE downloaded = 1").Scan(&docsDownloaded)
	return
}

func GetCorrigendumDocsForBid(db *sql.DB, bidID int) ([]CorrigendumDoc, error) {
	rows, err := db.Query(`
		SELECT id, bid_id, corrigendum_id, download_url, modified_on, downloaded
		FROM corrigendum_documents WHERE bid_id = ? ORDER BY modified_on DESC`, bidID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []CorrigendumDoc
	for rows.Next() {
		var d CorrigendumDoc
		if err := rows.Scan(&d.ID, &d.BidID, &d.CorrigendumID, &d.DownloadURL, &d.ModifiedOn, &d.Downloaded); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}
```

**Step 5: Run tests to verify they pass**

Run: `go test -v`
Expected: All PASS

**Step 6: Commit**

```bash
git add models.go db.go db_test.go
git commit -m "feat: add corrigendum models and DB helper functions"
```

---

### Task 3: HTML parsing — extract download links, end dates, corrigendum count

**Files:**
- Create: `corrigendum.go`
- Create: `corrigendum_test.go`

**Step 1: Write the failing test**

Create `corrigendum_test.go`:

```go
package main

import (
	"testing"
)

// Sample HTML from the viewCorrigendum API (with download link)
const sampleCorrigendumWithDocs = `
<div class="border block">
    <div class="block_bid_no"><p> Corrigendum Details</p></div>
    <div class="clearfix" style="height:3px"></div>
    <div class="col-md-12 col-xs-12">
        <div class="well"><div class="col-block"> <span id=span_4098546><strong >Modified On: </strong><span>2026-03-11 12:47:58</span></span></div><div class="col-block" style="width:48% !important;"><a href="/bidding/bid/showcorrigendumpdf/4098546/8960898"><span class="glyphicon glyphicon-download-alt"></span> Download</a></div><div class="clearfix"></div></div>
        <div class="well"><div class="col-block"> <span id=span_4098541><strong >Modified On: </strong><span>2026-03-11 12:46:24</span></span></div><div class="col-block" style="width:48% !important;">Bid extended to <strong>2026-03-18 09:00:00</strong></div><div class="clearfix"></div></div>
        <div class="well"><div class="col-block"><strong>&nbsp;</strong><span></span></div><div class="col-block" style="width:48%;">Bid Opening Date: <strong>2026-03-18 09:30:00</strong></div><div class="clearfix"></div></div>
        <div class="well"><div class="col-block"> <span id=span_4089281><strong >Modified On: </strong><span>2026-03-09 11:18:47</span></span></div><div class="col-block" style="width:48% !important;">Bid extended to <strong>2026-03-13 09:00:00</strong></div><div class="clearfix"></div></div>
        <div class="well"><div class="col-block"><strong>&nbsp;</strong><span></span></div><div class="col-block" style="width:48%;">Bid Opening Date: <strong>2026-03-13 09:30:00</strong></div><div class="clearfix"></div></div>
        <div class="clearfix"></div>
    </div>
    <div class="clearfix"></div>
</div>`

// Sample HTML without download links
const sampleCorrigendumNoDocs = `
<div class="border block">
    <div class="block_bid_no"><p> Corrigendum Details</p></div>
    <div class="clearfix" style="height:3px"></div>
    <div class="col-md-12 col-xs-12">
        <div class="well"><div class="col-block"><strong>&nbsp;</strong><span></span></div><div class="col-block" style="width:48%;">Bid Opening Date: <strong>2026-03-18 09:30:00</strong></div><div class="clearfix"></div></div>
        <div class="clearfix"></div>
    </div>
    <div class="clearfix"></div>
</div>`

func TestParseCorrigendumDocs(t *testing.T) {
	docs := ParseCorrigendumDocs(sampleCorrigendumWithDocs, 8960898)

	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].CorrigendumID != 4098546 {
		t.Errorf("expected corrigendum_id=4098546, got %d", docs[0].CorrigendumID)
	}
	if docs[0].DownloadURL != "/bidding/bid/showcorrigendumpdf/4098546/8960898" {
		t.Errorf("unexpected download URL: %s", docs[0].DownloadURL)
	}
	if docs[0].ModifiedOn != "2026-03-11 12:47:58" {
		t.Errorf("expected modified_on='2026-03-11 12:47:58', got '%s'", docs[0].ModifiedOn)
	}
}

func TestParseCorrigendumDocsEmpty(t *testing.T) {
	docs := ParseCorrigendumDocs(sampleCorrigendumNoDocs, 8783867)
	if len(docs) != 0 {
		t.Errorf("expected 0 documents, got %d", len(docs))
	}
}

func TestParseLatestEndDate(t *testing.T) {
	date := ParseLatestEndDate(sampleCorrigendumWithDocs)
	if date != "2026-03-18 09:00:00" {
		t.Errorf("expected '2026-03-18 09:00:00', got '%s'", date)
	}
}

func TestParseLatestEndDateNone(t *testing.T) {
	date := ParseLatestEndDate(sampleCorrigendumNoDocs)
	if date != "" {
		t.Errorf("expected empty, got '%s'", date)
	}
}

func TestParseCorrigendumCount(t *testing.T) {
	count := ParseCorrigendumCount(sampleCorrigendumWithDocs)
	if count != 5 {
		t.Errorf("expected 5 well blocks, got %d", count)
	}

	count = ParseCorrigendumCount(sampleCorrigendumNoDocs)
	if count != 1 {
		t.Errorf("expected 1 well block, got %d", count)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test -run "TestParseCorrigendum|TestParseLatest" -v`
Expected: FAIL — functions don't exist

**Step 3: Write the parsing functions**

Create `corrigendum.go`:

```go
package main

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// Matches: /bidding/bid/showcorrigendumpdf/{corrigendum_id}/{bid_id}
	reCorrigendumPDFLink = regexp.MustCompile(`href="(/bidding/bid/showcorrigendumpdf/(\d+)/(\d+))"`)

	// Matches: Modified On: </strong><span>2026-03-11 12:47:58</span>
	reModifiedOn = regexp.MustCompile(`id=span_(\d+)>.*?Modified On:.*?<span>([\d\- :]+)</span>`)

	// Matches: Bid extended to <strong>2026-03-18 09:00:00</strong>
	reBidExtended = regexp.MustCompile(`Bid extended to\s*<strong>([\d\- :]+)</strong>`)

	// Matches: <div class="well">
	reWellBlock = regexp.MustCompile(`<div class="well">`)
)

// ParseCorrigendumDocs extracts download links from corrigendum HTML.
func ParseCorrigendumDocs(html string, bidID int) []CorrigendumDoc {
	var docs []CorrigendumDoc

	linkMatches := reCorrigendumPDFLink.FindAllStringSubmatch(html, -1)
	for _, m := range linkMatches {
		corrID, _ := strconv.Atoi(m[2])
		doc := CorrigendumDoc{
			BidID:         bidID,
			CorrigendumID: corrID,
			DownloadURL:   m[1],
		}

		// Find modified_on for this corrigendum_id by looking for span_{id}
		modMatches := reModifiedOn.FindAllStringSubmatch(html, -1)
		for _, mod := range modMatches {
			if mod[1] == m[2] {
				doc.ModifiedOn = strings.TrimSpace(mod[2])
				break
			}
		}

		docs = append(docs, doc)
	}

	return docs
}

// ParseLatestEndDate extracts the latest "Bid extended to" date from corrigendum HTML.
// Returns empty string if no extension found.
func ParseLatestEndDate(html string) string {
	matches := reBidExtended.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return ""
	}

	// Dates are in "YYYY-MM-DD HH:MM:SS" format — string comparison works for finding latest
	latest := ""
	for _, m := range matches {
		date := strings.TrimSpace(m[1])
		if date > latest {
			latest = date
		}
	}
	return latest
}

// ParseCorrigendumCount counts the number of corrigendum entries (well blocks) in the HTML.
func ParseCorrigendumCount(html string) int {
	return len(reWellBlock.FindAllString(html, -1))
}
```

**Step 4: Run tests to verify they pass**

Run: `go test -run "TestParseCorrigendum|TestParseLatest" -v`
Expected: All PASS

**Step 5: Run all tests**

Run: `go test -v`
Expected: All PASS

**Step 6: Commit**

```bash
git add corrigendum.go corrigendum_test.go
git commit -m "feat: add corrigendum HTML parsing — extract docs, end dates, count"
```

---

### Task 4: API calls — check other details, fetch corrigendum, fetch representation

**Files:**
- Modify: `corrigendum.go` (add HTTP fetch functions)
- Modify: `corrigendum_test.go` (add response parsing test)

**Step 1: Write the failing test**

Add to `corrigendum_test.go`:

```go
func TestParseOtherDetailsResponse(t *testing.T) {
	raw := `{"status":1,"code":200,"message":"Request processed successfully","response":{"corrigendum":true,"representation":false}}`

	var resp OtherDetailsResponse
	err := json.Unmarshal([]byte(raw), &resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !resp.Response.Corrigendum {
		t.Error("expected corrigendum=true")
	}
	if resp.Response.Representation {
		t.Error("expected representation=false")
	}
}

func TestParseOtherDetailsBothTrue(t *testing.T) {
	raw := `{"status":1,"code":200,"message":"Request processed successfully","response":{"corrigendum":true,"representation":true}}`

	var resp OtherDetailsResponse
	err := json.Unmarshal([]byte(raw), &resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !resp.Response.Corrigendum {
		t.Error("expected corrigendum=true")
	}
	if !resp.Response.Representation {
		t.Error("expected representation=true")
	}
}
```

Add `"encoding/json"` to the imports in `corrigendum_test.go`.

**Step 2: Run tests to verify they pass** (struct already exists from Task 2)

Run: `go test -run "TestParseOtherDetails" -v`
Expected: PASS (struct already defined)

**Step 3: Add HTTP fetch functions to `corrigendum.go`**

Add to `corrigendum.go`:

```go
import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// FetchOtherDetails calls POST /public-bid-other-details/{bid_id}
// Returns whether corrigendum and representation exist for this bid.
func FetchOtherDetails(sp *SessionPair, bidID int) (hasCorr bool, hasRepr bool, err error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/public-bid-other-details/%d", baseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return false, false, err
	}
	setAjaxHeaders(req)

	resp, err := sp.Client.Do(req)
	if err != nil {
		return false, false, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return false, false, err
	}

	var apiResp OtherDetailsResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return false, false, fmt.Errorf("unmarshal: %w", err)
	}

	return apiResp.Response.Corrigendum, apiResp.Response.Representation, nil
}

// FetchCorrigendumHTML calls POST /bidding/bid/viewCorrigendum/{bid_id}
// Returns the raw HTML response body.
func FetchCorrigendumHTML(sp *SessionPair, bidID int) (string, error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/bidding/bid/viewCorrigendum/%d", baseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}
	setAjaxHeaders(req)
	req.Header.Set("Accept", "text/html, */*; q=0.01")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// FetchRepresentationHTML calls POST /bidding/bid/viewRepresentation/{bid_id}
// Returns the raw HTML response body.
func FetchRepresentationHTML(sp *SessionPair, bidID int) (string, error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/bidding/bid/viewRepresentation/%d", baseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}
	setAjaxHeaders(req)
	req.Header.Set("Accept", "text/html, */*; q=0.01")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// setAjaxHeaders sets common headers for GEM AJAX requests.
func setAjaxHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+"/all-bids")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
}

// readResponseBody reads the response body, handling gzip if needed.
func readResponseBody(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	return io.ReadAll(reader)
}
```

Note: `readResponseBody` and `setAjaxHeaders` deduplicate logic from `scraper.go:189-197` and `scraper.go:165-177`. After this task, refactor `fetchPage` in `scraper.go` to use these shared helpers (optional cleanup).

**Step 4: Run all tests**

Run: `go test -v`
Expected: All PASS

**Step 5: Commit**

```bash
git add corrigendum.go corrigendum_test.go
git commit -m "feat: add API fetch functions for other-details, corrigendum, representation"
```

---

### Task 5: Scrape orchestrator — ScrapeCorrigendums with delta detection

**Files:**
- Modify: `corrigendum.go` (add `ScrapeCorrigendums` function)

**Step 1: Write the orchestrator**

Add to `corrigendum.go`:

```go
import (
	"context"
	"database/sql"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// ScrapeCorrigendums checks all active bids for corrigendum/representation updates.
// Uses the same session pool and rate limiting pattern as ScrapeBids.
func ScrapeCorrigendums(pool *SessionPool, db *sql.DB, workers int, rps int) error {
	bidIDs, err := GetActiveBidIDs(db)
	if err != nil {
		return fmt.Errorf("get active bids: %w", err)
	}

	if len(bidIDs) == 0 {
		log.Println("[corrigendum] No active bids to check")
		return nil
	}

	log.Printf("[corrigendum] Checking %d active bids (%d workers, %d req/s)", len(bidIDs), workers, rps)

	limiter := rate.NewLimiter(rate.Limit(rps), rps*2)

	jobs := make(chan int, len(bidIDs))
	for _, id := range bidIDs {
		jobs <- id
	}
	close(jobs)

	var (
		wg       sync.WaitGroup
		checked  int64
		updated  int64
		errors   int64
		newDocs  int64
		total    = int64(len(bidIDs))
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sp := pool.Next()

			for bidID := range jobs {
				limiter.Wait(context.Background())

				err := processOneBid(sp, db, bidID, limiter, &newDocs)
				if err != nil {
					atomic.AddInt64(&errors, 1)
					// Rotate session on error
					sp = pool.Next()
					continue
				}

				atomic.AddInt64(&checked, 1)
				done := atomic.LoadInt64(&checked)
				if done%500 == 0 {
					log.Printf("[corrigendum] Progress: %d/%d checked, %d updated, %d errors",
						done, total, atomic.LoadInt64(&updated), atomic.LoadInt64(&errors))
				}
			}
		}()
	}

	wg.Wait()
	log.Printf("[corrigendum] Done: %d checked, %d updated, %d new docs, %d errors",
		checked, updated, newDocs, errors)
	return nil
}

func processOneBid(sp *SessionPair, db *sql.DB, bidID int, limiter *rate.Limiter, newDocs *int64) error {
	// Step 1: Check flags
	hasCorr, hasRepr, err := FetchOtherDetails(sp, bidID)
	if err != nil {
		return fmt.Errorf("bid %d other-details: %w", bidID, err)
	}

	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")

	// Load existing record (may not exist)
	existing, _ := GetBidOtherDetails(db, bidID)

	details := BidOtherDetails{
		BidID:             bidID,
		HasCorrigendum:    boolToInt(hasCorr),
		HasRepresentation: boolToInt(hasRepr),
		LastChecked:       now,
	}

	// Carry forward existing HTML if not re-fetching
	if existing != nil {
		details.CorrigendumHTML = existing.CorrigendumHTML
		details.RepresentationHTML = existing.RepresentationHTML
		details.CorrigendumCount = existing.CorrigendumCount
		details.LatestEndDate = existing.LatestEndDate
	}

	// Step 2: Fetch corrigendum if flagged
	if hasCorr {
		limiter.Wait(context.Background())
		html, err := FetchCorrigendumHTML(sp, bidID)
		if err != nil {
			return fmt.Errorf("bid %d corrigendum: %w", bidID, err)
		}

		// Delta detection: only process if HTML changed
		if existing == nil || html != existing.CorrigendumHTML {
			details.CorrigendumHTML = html
			details.CorrigendumCount = ParseCorrigendumCount(html)

			// Extract and insert document links
			docs := ParseCorrigendumDocs(html, bidID)
			for _, doc := range docs {
				InsertCorrigendumDoc(db, doc)
				atomic.AddInt64(newDocs, 1)
			}

			// Update bid end_date if extended
			latestDate := ParseLatestEndDate(html)
			if latestDate != "" {
				details.LatestEndDate = latestDate
				UpdateBidEndDate(db, bidID, latestDate)
			}
		}
	}

	// Step 3: Fetch representation if flagged
	if hasRepr {
		limiter.Wait(context.Background())
		html, err := FetchRepresentationHTML(sp, bidID)
		if err != nil {
			return fmt.Errorf("bid %d representation: %w", bidID, err)
		}

		// Delta detection
		if existing == nil || html != existing.RepresentationHTML {
			details.RepresentationHTML = html
		}
	}

	// Step 4: Upsert
	return UpsertBidOtherDetails(db, details)
}
```

**Step 2: Verify it compiles**

Run: `go build ./...`
Expected: No errors

**Step 3: Run all tests**

Run: `go test -v`
Expected: All PASS

**Step 4: Commit**

```bash
git add corrigendum.go
git commit -m "feat: add ScrapeCorrigendums orchestrator with delta detection"
```

---

### Task 6: Corrigendum PDF downloader

**Files:**
- Modify: `downloader.go` (add `DownloadCorrigendumPDFs` function)

**Step 1: Add the download function**

Append to `downloader.go`:

```go
func DownloadCorrigendumPDFs(db *sql.DB, downloadDir string, workers int, rps int, maxRetries int) error {
	corrDir := filepath.Join(downloadDir, "corrigendums")
	if err := os.MkdirAll(corrDir, 0755); err != nil {
		return fmt.Errorf("create corrigendum dir: %w", err)
	}

	pending, err := GetPendingCorrigendumDownloads(db)
	if err != nil {
		return fmt.Errorf("get pending: %w", err)
	}

	if len(pending) == 0 {
		log.Println("No pending corrigendum PDF downloads")
		return nil
	}

	// Filter out already-downloaded files
	var toDownload []CorrigendumDoc
	for _, doc := range pending {
		destPath := filepath.Join(corrDir, fmt.Sprintf("Corrigendum-%d-%d.pdf", doc.CorrigendumID, doc.BidID))
		if _, err := os.Stat(destPath); err == nil {
			MarkCorrigendumDownloaded(db, doc.ID)
			continue
		}
		toDownload = append(toDownload, doc)
	}

	if len(toDownload) == 0 {
		log.Println("All corrigendum PDFs already on disk")
		return nil
	}

	log.Printf("Downloading %d corrigendum PDFs with %d workers at %d req/s", len(toDownload), workers, rps)

	limiter := rate.NewLimiter(rate.Limit(rps), rps*2)

	jobs := make(chan CorrigendumDoc, len(toDownload))
	for _, doc := range toDownload {
		jobs <- doc
	}
	close(jobs)

	var (
		wg        sync.WaitGroup
		completed int64
		failed    int64
		total     = int64(len(toDownload))
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := &http.Client{Timeout: 60 * time.Second}

			for doc := range jobs {
				limiter.Wait(context.Background())

				pdfURL := baseURL + doc.DownloadURL
				destPath := filepath.Join(corrDir, fmt.Sprintf("Corrigendum-%d-%d.pdf", doc.CorrigendumID, doc.BidID))

				err := downloadCorrigendumWithRetry(client, pdfURL, destPath, maxRetries)
				if err != nil {
					log.Printf("Corrigendum download failed for %d/%d: %v", doc.CorrigendumID, doc.BidID, err)
					atomic.AddInt64(&failed, 1)
					continue
				}

				MarkCorrigendumDownloaded(db, doc.ID)
				done := atomic.AddInt64(&completed, 1)
				if done%100 == 0 {
					log.Printf("Corrigendum PDF progress: %d/%d downloaded, %d failed",
						done, total, atomic.LoadInt64(&failed))
				}
			}
		}()
	}

	wg.Wait()
	log.Printf("Corrigendum PDF download complete: %d/%d succeeded, %d failed", completed, total, failed)
	return nil
}

func downloadCorrigendumWithRetry(client *http.Client, pdfURL string, destPath string, maxRetries int) error {
	var lastErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		lastErr = downloadFile(client, pdfURL, destPath)
		if lastErr == nil {
			return nil
		}
		if attempt < maxRetries {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}
	}
	return fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

func downloadFile(client *http.Client, url string, destPath string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/pdf,application/x-pdf,*/*")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer out.Close()

	if _, err = io.Copy(out, resp.Body); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("write file: %w", err)
	}
	return nil
}
```

**Step 2: Verify it compiles**

Run: `go build ./...`
Expected: No errors

**Step 3: Commit**

```bash
git add downloader.go
git commit -m "feat: add corrigendum PDF downloader"
```

---

### Task 7: Wire into main.go — scrape and download commands

**Files:**
- Modify: `main.go`

**Step 1: Update `runScrapeCmd` to include corrigendum pass**

In `main.go:67-71`, after `ScrapeBids(...)`, add:

```go
	log.Println("=== Checking corrigendums/representations ===")
	if err := ScrapeCorrigendums(pool, db, *workers, *rps); err != nil {
		log.Printf("Corrigendum check error: %v", err)
		// Don't exit — bid scraping succeeded
	}
```

**Step 2: Update `runDownloadCmd` to include corrigendum PDFs**

In `main.go:92-96`, after `DownloadPDFs(...)`, add:

```go
	log.Println("=== Downloading corrigendum PDFs ===")
	if err := DownloadCorrigendumPDFs(db, *downloadDir, *workers, *rps, *retries); err != nil {
		log.Printf("Corrigendum download error: %v", err)
	}
```

**Step 3: Update `runStatusCmd` to show corrigendum stats**

In `main.go:113-118`, after the existing print statements, add:

```go
	checked, withCorr, docsTotal, docsDownloaded, _ := GetCorrigendumStats(db)
	fmt.Printf("Corrigendums checked: %d\n", checked)
	fmt.Printf("Bids with corrigendums: %d\n", withCorr)
	fmt.Printf("Corrigendum PDFs:     %d downloaded / %d total\n", docsDownloaded, docsTotal)
```

**Step 4: Verify it compiles**

Run: `go build ./...`
Expected: No errors

**Step 5: Run all tests**

Run: `go test -v`
Expected: All PASS

**Step 6: Commit**

```bash
git add main.go
git commit -m "feat: wire corrigendum scraping and downloading into CLI commands"
```

---

### Task 8: Web UI — corrigendum/representation tabs on tender detail page

**Files:**
- Modify: `server.go:28-45` (pass corrigendum data to template)
- Modify: `db.go` (add `HasCorrigendum`/`HasRepresentation` to `BidResult` struct)
- Modify: `web/templates/tender.tmpl` (add tabbed section)
- Modify: `web/templates/results.tmpl` (add C/R badges)

**Step 1: Add fields to BidResult and update query**

In `db.go`, add to the `BidResult` struct (line 229-242):

```go
type BidResult struct {
	ID              string
	BidID           int
	BidNumber       string
	BidNumberParent string
	BidIDParent     int
	CategoryName    string
	TotalQuantity   int
	StartDate       string
	EndDate         string
	IsHighValue     int
	MinistryName    string
	DepartmentName  string
	HasCorrigendum  int
	HasRepresentation int
}
```

Update `scanBidResults` (line 308-322) to LEFT JOIN `bid_other_details` and scan the new fields. Update all queries that use `scanBidResults` (`SearchBids`, `recentBids`, `GetBidByID`) to include the join:

```sql
LEFT JOIN bid_other_details bod ON bod.bid_id = b.bid_id
```

And select `COALESCE(bod.has_corrigendum, 0)` and `COALESCE(bod.has_representation, 0)`.

**Step 2: Update `server.go` tender handler**

In `server.go:28-45`, add corrigendum data to the template context:

```go
r.GET("/tender/:id", func(c *gin.Context) {
    id := c.Param("id")
    bid, err := GetBidByID(db, id)
    if err != nil {
        c.HTML(404, "index.tmpl", nil)
        return
    }

    pdfID := bid.BidIDParent
    if pdfID == 0 {
        pdfID = bid.BidID
    }

    // Fetch corrigendum details
    otherDetails, _ := GetBidOtherDetails(db, bid.BidID)
    corrDocs, _ := GetCorrigendumDocsForBid(db, bid.BidID)

    c.HTML(200, "tender.tmpl", gin.H{
        "Bid":          bid,
        "PDFID":        pdfID,
        "OtherDetails": otherDetails,
        "CorrDocs":     corrDocs,
    })
})
```

Add a route to serve corrigendum PDFs:

```go
r.GET("/corrigendum-pdf/:corrId/:bidId", func(c *gin.Context) {
    corrID := c.Param("corrId")
    bidID := c.Param("bidId")
    filePath := filepath.Join(downloadDir, "corrigendums",
        fmt.Sprintf("Corrigendum-%s-%s.pdf", corrID, bidID))
    c.File(filePath)
})
```

**Step 3: Update `tender.tmpl`**

After the `</div>` closing `tender-detail` (line 54), before the `pdf-container` div, add:

```html
{{ if .OtherDetails }}
<div class="corrigendum-section">
    <div class="tabs">
        {{ if eq .OtherDetails.HasCorrigendum 1 }}
        <button class="tab active" onclick="showTab('corrigendum')">
            Corrigendum <span class="badge">{{ .OtherDetails.CorrigendumCount }}</span>
        </button>
        {{ end }}
        {{ if eq .OtherDetails.HasRepresentation 1 }}
        <button class="tab" onclick="showTab('representation')">Representation</button>
        {{ end }}
    </div>

    {{ if eq .OtherDetails.HasCorrigendum 1 }}
    <div id="tab-corrigendum" class="tab-content active">
        {{ if .CorrDocs }}
        <h4>Corrigendum Documents</h4>
        <ul class="corr-docs">
            {{ range .CorrDocs }}
            <li>
                {{ if eq .Downloaded 1 }}
                <a href="/corrigendum-pdf/{{ .CorrigendumID }}/{{ .BidID }}">Download PDF</a>
                {{ else }}
                <span class="pending">Pending download</span>
                {{ end }}
                {{ if .ModifiedOn }}<span class="date">Modified: {{ .ModifiedOn }}</span>{{ end }}
            </li>
            {{ end }}
        </ul>
        {{ end }}
        <div class="corr-html">
            {{ .OtherDetails.CorrigendumHTML }}
        </div>
    </div>
    {{ end }}

    {{ if eq .OtherDetails.HasRepresentation 1 }}
    <div id="tab-representation" class="tab-content" style="display:none;">
        <div class="repr-html">
            {{ .OtherDetails.RepresentationHTML }}
        </div>
    </div>
    {{ end }}
</div>

<script>
function showTab(name) {
    document.querySelectorAll('.tab-content').forEach(el => el.style.display = 'none');
    document.querySelectorAll('.tab').forEach(el => el.classList.remove('active'));
    document.getElementById('tab-' + name).style.display = 'block';
    event.target.classList.add('active');
}
</script>
{{ end }}
```

Note: The `CorrigendumHTML` and `RepresentationHTML` contain raw HTML from GEM. Use Go's `template.HTML` type or the `| safeHTML` pipeline to render it unescaped. You'll need to add a template function in `server.go`:

```go
r.SetFuncMap(template.FuncMap{
    "safeHTML": func(s string) template.HTML { return template.HTML(s) },
})
```

Then use `{{ .OtherDetails.CorrigendumHTML | safeHTML }}` in the template.

**Step 4: Update `results.tmpl` with C/R badges**

In `results.tmpl:19`, after the high-value badge, add:

```html
{{ if eq .HasCorrigendum 1 }}<span class="badge corrigendum">C</span>{{ end }}
{{ if eq .HasRepresentation 1 }}<span class="badge representation">R</span>{{ end }}
```

**Step 5: Verify it compiles**

Run: `go build ./...`
Expected: No errors

**Step 6: Commit**

```bash
git add server.go db.go web/templates/tender.tmpl web/templates/results.tmpl
git commit -m "feat: add corrigendum/representation tabs to tender UI with C/R badges"
```

---

### Task 9: Add CSS styles for corrigendum UI

**Files:**
- Modify: `web/static/style.css`

**Step 1: Add styles**

Append to `web/static/style.css`:

```css
/* Corrigendum/Representation tabs */
.corrigendum-section { margin: 1.5rem 0; }
.tabs { display: flex; gap: 0; border-bottom: 2px solid #dee2e6; }
.tab { padding: 0.5rem 1.2rem; border: none; background: transparent; cursor: pointer; font-weight: 500; color: #666; border-bottom: 2px solid transparent; margin-bottom: -2px; }
.tab.active { color: #0d6efd; border-bottom-color: #0d6efd; }
.tab .badge { margin-left: 0.4rem; }
.tab-content { padding: 1rem 0; }
.tab-content .well { background: #f8f9fa; border: 1px solid #dee2e6; border-radius: 4px; padding: 0.75rem; margin-bottom: 0.5rem; }
.corr-docs { list-style: none; padding: 0; margin: 0 0 1rem 0; }
.corr-docs li { padding: 0.5rem 0; border-bottom: 1px solid #eee; display: flex; align-items: center; gap: 1rem; }
.corr-docs .pending { color: #999; font-style: italic; }
.corr-docs .date { color: #666; font-size: 0.85rem; }

/* C/R badges on result cards */
.badge.corrigendum { background: #fd7e14; color: white; }
.badge.representation { background: #6f42c1; color: white; }
```

**Step 2: Commit**

```bash
git add web/static/style.css
git commit -m "feat: add CSS styles for corrigendum tabs and badges"
```

---

### Task 10: Integration test — end-to-end corrigendum flow with test DB

**Files:**
- Modify: `db_test.go`

**Step 1: Write integration test**

Add to `db_test.go`:

```go
func TestCorrigendumEndToEnd(t *testing.T) {
	dbPath := "/tmp/test_gem_e2e_corr.db"
	defer os.Remove(dbPath)

	db, err := InitDB(dbPath)
	if err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	defer db.Close()

	// Insert a bid with future end_date
	InsertBid(db, BidDoc{
		ID:      "100",
		BidID:   []int{100},
		EndDate: []string{"2099-12-31T00:00:00Z"},
	})

	// Verify it shows as active
	active, _ := GetActiveBidIDs(db)
	if len(active) != 1 || active[0] != 100 {
		t.Fatalf("expected active bid [100], got %v", active)
	}

	// Simulate: other-details says corrigendum=true
	details := BidOtherDetails{
		BidID:           100,
		HasCorrigendum:  1,
		CorrigendumHTML: `<div class="well">first version</div>`,
		CorrigendumCount: 1,
		LastChecked:     "2026-03-17T12:00:00Z",
	}
	UpsertBidOtherDetails(db, details)

	// Insert a corrigendum document
	InsertCorrigendumDoc(db, CorrigendumDoc{
		BidID:         100,
		CorrigendumID: 555,
		DownloadURL:   "/bidding/bid/showcorrigendumpdf/555/100",
		ModifiedOn:    "2026-03-17 10:00:00",
	})

	// Verify pending download
	pending, _ := GetPendingCorrigendumDownloads(db)
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending download, got %d", len(pending))
	}

	// Simulate: corrigendum updated with new content
	details.CorrigendumHTML = `<div class="well">first version</div><div class="well">second</div>`
	details.CorrigendumCount = 2
	UpsertBidOtherDetails(db, details)

	got, _ := GetBidOtherDetails(db, 100)
	if got.CorrigendumCount != 2 {
		t.Errorf("expected count=2 after update, got %d", got.CorrigendumCount)
	}

	// Simulate: mark downloaded
	MarkCorrigendumDownloaded(db, pending[0].ID)
	pending2, _ := GetPendingCorrigendumDownloads(db)
	if len(pending2) != 0 {
		t.Errorf("expected 0 pending after download, got %d", len(pending2))
	}

	// Stats
	checked, withCorr, docsTotal, docsDownloaded, _ := GetCorrigendumStats(db)
	if checked != 1 || withCorr != 1 || docsTotal != 1 || docsDownloaded != 1 {
		t.Errorf("stats mismatch: checked=%d withCorr=%d total=%d downloaded=%d",
			checked, withCorr, docsTotal, docsDownloaded)
	}
}
```

**Step 2: Run test**

Run: `go test -run TestCorrigendumEndToEnd -v`
Expected: PASS

**Step 3: Run full test suite**

Run: `go test -v`
Expected: All PASS

**Step 4: Commit**

```bash
git add db_test.go
git commit -m "test: add end-to-end integration test for corrigendum flow"
```

---

## Summary

| Task | Description | Files |
|------|-------------|-------|
| 1 | DB schema — new tables + migration | `db.go`, `db_test.go` |
| 2 | Models + DB helper functions | `models.go`, `db.go`, `db_test.go` |
| 3 | HTML parsing — extract docs, dates, count | `corrigendum.go`, `corrigendum_test.go` |
| 4 | API fetch functions | `corrigendum.go`, `corrigendum_test.go` |
| 5 | Scrape orchestrator with delta detection | `corrigendum.go` |
| 6 | Corrigendum PDF downloader | `downloader.go` |
| 7 | Wire into main.go CLI commands | `main.go` |
| 8 | Web UI — tabs + badges | `server.go`, `db.go`, templates |
| 9 | CSS styles | `style.css` |
| 10 | Integration test | `db_test.go` |

**Note:** The representation endpoint URL (`/bidding/bid/viewRepresentation/{bid_id}`) needs to be confirmed by capturing it from the browser network tab. If the endpoint is different, update `FetchRepresentationHTML` in Task 4.
