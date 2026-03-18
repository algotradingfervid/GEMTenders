package store

import (
	"database/sql"
	"fmt"
	"time"

	"gemtenders/internal/models"
)

// GetSummaryStats returns aggregate dashboard statistics using consolidated queries.
func GetSummaryStats(db *sql.DB) (*models.SummaryStats, error) {
	s := &models.SummaryStats{}

	// Query 1: bids stats (consolidated)
	var lastScrape sql.NullString
	err := db.QueryRow(`
		SELECT COUNT(*),
			COUNT(DISTINCT CASE WHEN pdf_downloaded=1 THEN CASE WHEN bid_id_parent>0 THEN bid_id_parent ELSE bid_id END END),
			COUNT(DISTINCT CASE WHEN pdf_downloaded=0 AND (bid_id>0 OR bid_id_parent>0) THEN CASE WHEN bid_id_parent>0 THEN bid_id_parent ELSE bid_id END END),
			SUM(CASE WHEN created_at >= datetime('now','-1 day') THEN 1 ELSE 0 END),
			MAX(created_at)
		FROM bids
	`).Scan(&s.TotalBids, &s.PDFsDownloaded, &s.PDFsPending, &s.BidsLast24h, &lastScrape)
	if err != nil {
		return nil, fmt.Errorf("bids stats: %w", err)
	}
	if lastScrape.Valid {
		s.LastScrapeTime = lastScrape.String
	}

	// Query 2: corrigendum stats (consolidated)
	err = db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN downloaded=1 THEN 1 ELSE 0 END), 0)
		FROM corrigendum_documents
	`).Scan(&s.CorrDocsTracked, &s.CorrDocsDownloaded)
	if err != nil {
		return nil, fmt.Errorf("corrigendum stats: %w", err)
	}

	return s, nil
}

// GetPipelineStats returns counts of active, expired, and soon-closing tenders using a single query.
func GetPipelineStats(db *sql.DB) (*models.PipelineStats, error) {
	p := &models.PipelineStats{}

	t := time.Now().UTC()
	now := t.Format("2006-01-02T15:04:05")
	h24 := t.Add(24 * time.Hour).Format("2006-01-02T15:04:05")
	h48 := t.Add(48 * time.Hour).Format("2006-01-02T15:04:05")
	d7 := t.Add(7 * 24 * time.Hour).Format("2006-01-02T15:04:05")

	err := db.QueryRow(`
		SELECT
			SUM(CASE WHEN end_date > ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN end_date <= ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN end_date > ? AND end_date <= ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN end_date > ? AND end_date <= ? THEN 1 ELSE 0 END),
			SUM(CASE WHEN end_date > ? AND end_date <= ? THEN 1 ELSE 0 END)
		FROM bids
	`, now, now, now, h24, now, h48, now, d7).Scan(
		&p.Active, &p.Expired, &p.Closing24h, &p.Closing48h, &p.Closing7d)
	if err != nil {
		return nil, fmt.Errorf("pipeline stats: %w", err)
	}

	return p, nil
}

// GetTopDepartments returns the top N departments by bid count.
func GetTopDepartments(db *sql.DB, limit int) ([]models.BreakdownItem, error) {
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
func GetTopCategories(db *sql.DB, limit int) ([]models.BreakdownItem, error) {
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
func scanBreakdownItems(rows *sql.Rows) ([]models.BreakdownItem, error) {
	var items []models.BreakdownItem
	for rows.Next() {
		var item models.BreakdownItem
		if err := rows.Scan(&item.Name, &item.Count); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// GetBidsTimeline returns daily bid counts for the last N days.
func GetBidsTimeline(db *sql.DB, days int) ([]models.TimelinePoint, error) {
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

	var points []models.TimelinePoint
	for rows.Next() {
		var pt models.TimelinePoint
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
