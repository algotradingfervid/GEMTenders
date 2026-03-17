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
		ID:      "1",
		BidID:   []int{1},
		EndDate: []string{"2099-12-31T00:00:00Z"},
	})
	InsertBid(db, BidDoc{
		ID:      "2",
		BidID:   []int{2},
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
		BidID:            100,
		HasCorrigendum:   1,
		CorrigendumHTML:  `<div class="well">first version</div>`,
		CorrigendumCount: 1,
		LastChecked:      "2026-03-17T12:00:00Z",
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

	// Simulate: corrigendum updated with new content (delta)
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
