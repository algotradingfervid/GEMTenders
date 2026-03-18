package main

import (
	"database/sql"
	"fmt"
	"time"
)

// SummaryStats holds high-level counts for the dashboard.
type SummaryStats struct {
	TotalBids         int    `json:"total_bids"`
	PDFsDownloaded    int    `json:"pdfs_downloaded"`
	PDFsPending       int    `json:"pdfs_pending"`
	CorrDocsTracked   int    `json:"corr_docs_tracked"`
	CorrDocsDownloaded int   `json:"corr_docs_downloaded"`
	BidsLast24h       int    `json:"bids_last_24h"`
	LastScrapeTime    string `json:"last_scrape_time"`
}

// PipelineStats holds tender pipeline breakdown by deadline proximity.
type PipelineStats struct {
	Active    int `json:"active"`
	Expired   int `json:"expired"`
	Closing24h int `json:"closing_24h"`
	Closing48h int `json:"closing_48h"`
	Closing7d  int `json:"closing_7d"`
}

// BreakdownItem represents a single name/count pair for grouped queries.
type BreakdownItem struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// TimelinePoint represents a single date/count pair for time-series data.
type TimelinePoint struct {
	Date  string `json:"date"`
	Count int    `json:"count"`
}

// GetSummaryStats returns aggregate dashboard statistics.
func GetSummaryStats(db *sql.DB) (*SummaryStats, error) {
	s := &SummaryStats{}

	if err := db.QueryRow("SELECT COUNT(*) FROM bids").Scan(&s.TotalBids); err != nil {
		return nil, fmt.Errorf("count bids: %w", err)
	}

	if err := db.QueryRow(`
		SELECT COUNT(DISTINCT CASE WHEN bid_id_parent > 0 THEN bid_id_parent ELSE bid_id END)
		FROM bids WHERE pdf_downloaded = 1
	`).Scan(&s.PDFsDownloaded); err != nil {
		return nil, fmt.Errorf("count pdfs downloaded: %w", err)
	}

	if err := db.QueryRow(`
		SELECT COUNT(DISTINCT CASE WHEN bid_id_parent > 0 THEN bid_id_parent ELSE bid_id END)
		FROM bids WHERE pdf_downloaded = 0 AND (bid_id > 0 OR bid_id_parent > 0)
	`).Scan(&s.PDFsPending); err != nil {
		return nil, fmt.Errorf("count pdfs pending: %w", err)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM corrigendum_documents").Scan(&s.CorrDocsTracked); err != nil {
		return nil, fmt.Errorf("count corrigendum docs: %w", err)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM corrigendum_documents WHERE downloaded = 1").Scan(&s.CorrDocsDownloaded); err != nil {
		return nil, fmt.Errorf("count corrigendum downloaded: %w", err)
	}

	if err := db.QueryRow(`
		SELECT COUNT(*) FROM bids WHERE created_at >= datetime('now', '-1 day')
	`).Scan(&s.BidsLast24h); err != nil {
		return nil, fmt.Errorf("count bids last 24h: %w", err)
	}

	var lastScrape sql.NullString
	if err := db.QueryRow("SELECT MAX(created_at) FROM bids").Scan(&lastScrape); err != nil {
		return nil, fmt.Errorf("last scrape time: %w", err)
	}
	if lastScrape.Valid {
		s.LastScrapeTime = lastScrape.String
	}

	return s, nil
}

// GetPipelineStats returns counts of active, expired, and soon-closing tenders.
func GetPipelineStats(db *sql.DB) (*PipelineStats, error) {
	p := &PipelineStats{}

	now := time.Now().UTC().Format("2006-01-02T15:04:05")

	if err := db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date > ?", now).Scan(&p.Active); err != nil {
		return nil, fmt.Errorf("count active: %w", err)
	}

	if err := db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date <= ?", now).Scan(&p.Expired); err != nil {
		return nil, fmt.Errorf("count expired: %w", err)
	}

	h24 := time.Now().UTC().Add(24 * time.Hour).Format("2006-01-02T15:04:05")
	if err := db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date > ? AND end_date <= ?", now, h24).Scan(&p.Closing24h); err != nil {
		return nil, fmt.Errorf("count closing 24h: %w", err)
	}

	h48 := time.Now().UTC().Add(48 * time.Hour).Format("2006-01-02T15:04:05")
	if err := db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date > ? AND end_date <= ?", now, h48).Scan(&p.Closing48h); err != nil {
		return nil, fmt.Errorf("count closing 48h: %w", err)
	}

	d7 := time.Now().UTC().Add(7 * 24 * time.Hour).Format("2006-01-02T15:04:05")
	if err := db.QueryRow("SELECT COUNT(*) FROM bids WHERE end_date > ? AND end_date <= ?", now, d7).Scan(&p.Closing7d); err != nil {
		return nil, fmt.Errorf("count closing 7d: %w", err)
	}

	return p, nil
}

// GetTopDepartments returns the top N departments by bid count.
func GetTopDepartments(db *sql.DB, limit int) ([]BreakdownItem, error) {
	rows, err := db.Query(`
		SELECT department_name, COUNT(*) as cnt
		FROM bids
		WHERE department_name != ''
		GROUP BY department_name
		ORDER BY cnt DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("top departments: %w", err)
	}
	defer rows.Close()

	return scanBreakdownItems(rows)
}

// GetTopCategories returns the top N categories by bid count.
func GetTopCategories(db *sql.DB, limit int) ([]BreakdownItem, error) {
	rows, err := db.Query(`
		SELECT category_name, COUNT(*) as cnt
		FROM bids
		WHERE category_name != ''
		GROUP BY category_name
		ORDER BY cnt DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("top categories: %w", err)
	}
	defer rows.Close()

	return scanBreakdownItems(rows)
}

// scanBreakdownItems reads rows of (name, count) into a slice of BreakdownItem.
func scanBreakdownItems(rows *sql.Rows) ([]BreakdownItem, error) {
	var items []BreakdownItem
	for rows.Next() {
		var item BreakdownItem
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetBidsTimeline returns daily bid counts for the last N days.
func GetBidsTimeline(db *sql.DB, days int) ([]TimelinePoint, error) {
	rows, err := db.Query(`
		SELECT date(created_at) as d, COUNT(*) as cnt
		FROM bids
		WHERE created_at >= datetime('now', ? || ' days')
		GROUP BY d
		ORDER BY d ASC
	`, fmt.Sprintf("-%d", days))
	if err != nil {
		return nil, fmt.Errorf("bids timeline: %w", err)
	}
	defer rows.Close()

	var points []TimelinePoint
	for rows.Next() {
		var pt TimelinePoint
		if err := rows.Scan(&pt.Date, &pt.Count); err != nil {
			return nil, err
		}
		points = append(points, pt)
	}
	return points, rows.Err()
}

// allowedDistinctColumns is the allowlist for SearchDistinctValues to prevent SQL injection.
var allowedDistinctColumns = map[string]bool{
	"department_name": true,
	"category_name":   true,
}

// SearchDistinctValues returns distinct values from an allowed column matching a prefix query.
// Only "department_name" and "category_name" are permitted column names.
func SearchDistinctValues(db *sql.DB, column string, query string, limit int) ([]string, error) {
	if !allowedDistinctColumns[column] {
		return nil, fmt.Errorf("column %q is not allowed; must be one of: department_name, category_name", column)
	}

	// Column name is from the allowlist, so it is safe to interpolate.
	sqlStr := fmt.Sprintf(
		"SELECT DISTINCT %s FROM bids WHERE %s LIKE ? AND %s != '' ORDER BY %s LIMIT ?",
		column, column, column, column,
	)

	rows, err := db.Query(sqlStr, query+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("search distinct %s: %w", column, err)
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
	return values, rows.Err()
}
