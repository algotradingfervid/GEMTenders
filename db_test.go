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
