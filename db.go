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

// MarkPDFDownloaded marks bids as downloaded by their download ID.
// The download ID is bid_id_parent if set, otherwise bid_id.
func MarkPDFDownloaded(db *sql.DB, downloadID int) error {
	_, err := db.Exec(`UPDATE bids SET pdf_downloaded = 1
		WHERE (bid_id_parent = ? AND bid_id_parent > 0)
		   OR (bid_id = ? AND (bid_id_parent = 0 OR bid_id_parent IS NULL))`,
		downloadID, downloadID)
	return err
}

// GetPendingDownloads returns download IDs for bids without PDFs.
// Uses bid_id_parent if available, falls back to bid_id.
func GetPendingDownloads(db *sql.DB) ([]int, error) {
	rows, err := db.Query(`
		SELECT DISTINCT
			CASE WHEN bid_id_parent > 0 THEN bid_id_parent ELSE bid_id END as download_id
		FROM bids
		WHERE pdf_downloaded = 0
		AND (bid_id > 0 OR bid_id_parent > 0)`)
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
}

func SearchBids(db *sql.DB, query string, limit int, offset int) ([]BidResult, int, error) {
	if query == "" {
		return recentBids(db, limit, offset)
	}

	var total int
	err := db.QueryRow(`SELECT COUNT(*) FROM bids_fts WHERE bids_fts MATCH ?`, query).Scan(&total)
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
		FROM bids ORDER BY end_date DESC LIMIT ? OFFSET ?
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
