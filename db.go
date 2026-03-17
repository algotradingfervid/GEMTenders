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
`

func InitDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", dbPath+"?_journal_mode=WAL")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if _, err := db.Exec(createTableSQL); err != nil {
		return nil, fmt.Errorf("create table: %w", err)
	}
	// Migration: add end_date_original column if missing
	db.Exec("ALTER TABLE bids ADD COLUMN end_date_original TEXT DEFAULT ''")
	// Backfill: set end_date_original = end_date where not yet set
	db.Exec("UPDATE bids SET end_date_original = end_date WHERE end_date_original = '' AND end_date != ''")
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
	// Count unique download IDs (files), not bids — multiple bids can share one PDF
	err = db.QueryRow(`SELECT COUNT(DISTINCT CASE WHEN bid_id_parent > 0 THEN bid_id_parent ELSE bid_id END)
		FROM bids WHERE pdf_downloaded = 1`).Scan(&downloaded)
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
	ID                string
	BidID             int
	BidNumber         string
	BidNumberParent   string
	BidIDParent       int
	CategoryName      string
	TotalQuantity     int
	StartDate         string
	EndDate           string
	IsHighValue       int
	MinistryName      string
	DepartmentName    string
	HasCorrigendum    int
	HasRepresentation int
}

func GetBidByID(db *sql.DB, id string) (*BidResult, error) {
	var r BidResult
	err := db.QueryRow(`
		SELECT b.id, b.bid_id, b.bid_number, b.bid_number_parent, b.bid_id_parent,
		       b.category_name, b.total_quantity, b.start_date, b.end_date,
		       b.is_high_value, b.ministry_name, b.department_name,
		       COALESCE(bod.has_corrigendum, 0), COALESCE(bod.has_representation, 0)
		FROM bids b
		LEFT JOIN bid_other_details bod ON bod.bid_id = b.bid_id
		WHERE b.id = ?
	`, id).Scan(&r.ID, &r.BidID, &r.BidNumber, &r.BidNumberParent,
		&r.BidIDParent, &r.CategoryName, &r.TotalQuantity,
		&r.StartDate, &r.EndDate, &r.IsHighValue,
		&r.MinistryName, &r.DepartmentName,
		&r.HasCorrigendum, &r.HasRepresentation)
	if err != nil {
		return nil, err
	}
	return &r, nil
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
		       b.is_high_value, b.ministry_name, b.department_name,
		       COALESCE(bod.has_corrigendum, 0), COALESCE(bod.has_representation, 0)
		FROM bids_fts f
		JOIN bids b ON f.rowid = b.rowid
		LEFT JOIN bid_other_details bod ON bod.bid_id = b.bid_id
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
		SELECT b.id, b.bid_id, b.bid_number, b.bid_number_parent, b.bid_id_parent,
		       b.category_name, b.total_quantity, b.start_date, b.end_date,
		       b.is_high_value, b.ministry_name, b.department_name,
		       COALESCE(bod.has_corrigendum, 0), COALESCE(bod.has_representation, 0)
		FROM bids b
		LEFT JOIN bid_other_details bod ON bod.bid_id = b.bid_id
		ORDER BY b.end_date DESC LIMIT ? OFFSET ?
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
			&r.MinistryName, &r.DepartmentName,
			&r.HasCorrigendum, &r.HasRepresentation)
		if err != nil {
			return nil, 0, err
		}
		results = append(results, r)
	}
	return results, total, rows.Err()
}

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
	// Preserve the original end_date before the first update
	_, err := db.Exec(`
		UPDATE bids SET end_date_original = end_date
		WHERE bid_id = ? AND (end_date_original = '' OR end_date_original IS NULL)`,
		bidID)
	if err != nil {
		return err
	}
	_, err = db.Exec(`
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
