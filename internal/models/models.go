package models

import (
	"encoding/json"
	"time"
)

// ProgressFunc is a callback for reporting task progress to the ScrapeManager.
type ProgressFunc func(current, total, errors int64, msg string)

// ScrapeConfig holds configuration for scraping operations.
type ScrapeConfig struct {
	Scrapers   int
	StaggerSec int
	Workers    int
	RPS        int
}

// DownloadConfig holds configuration for download operations.
type DownloadConfig struct {
	Workers    int
	RPS        int
	MaxRetries int
}

// DefaultScrapeConfig provides reasonable defaults for scraping.
var DefaultScrapeConfig = ScrapeConfig{Scrapers: 5, StaggerSec: 30, Workers: 100, RPS: 50}

// DefaultDownloadConfig provides reasonable defaults for downloading.
var DefaultDownloadConfig = DownloadConfig{Workers: 100, RPS: 50, MaxRetries: 5}

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
			NumFound      int      `json:"numFound"`
			Start         int      `json:"start"`
			NumFoundExact bool     `json:"numFoundExact"`
			Docs          []BidDoc `json:"docs"`
		} `json:"response"`
	} `json:"response"`
}

// BidDoc is a single bid record from the API
// All fields come as arrays from Solr — we flatten them during DB insert
type BidDoc struct {
	ID              string   `json:"id"`
	BidID           []int    `json:"b_id"`
	BidNumber       []string `json:"b_bid_number"`
	BidNumberParent []string `json:"b_bid_number_parent"`
	BidIDParent     []int    `json:"b_id_parent"`
	CategoryName    []string `json:"b_category_name"`
	TotalQuantity   []int    `json:"b_total_quantity"`
	Status          []int    `json:"b_status"`
	BidType         []int    `json:"b_bid_type"`
	Type            []int    `json:"b_type"`
	IsBunch         []int    `json:"b_is_bunch"`
	BidToRA         []int    `json:"b_bid_to_ra"`
	StartDate       []string `json:"final_start_date_sort"`
	EndDate         []string `json:"final_end_date_sort"`
	IsHighValue     []bool   `json:"is_high_value"`
	MinistryName    []string `json:"ba_official_details_minName"`
	DepartmentName  []string `json:"ba_official_details_deptName"`
	IsGlobalTender  []int    `json:"ba_is_global_tendering"`
	IsRCBid         []int    `json:"is_rc_bid"`
	IsCustomItem    []int    `json:"b_is_custom_item"`
}

// Helper to safely get first element from Solr arrays
func FirstStr(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func FirstInt(s []int) int {
	if len(s) > 0 {
		return s[0]
	}
	return 0
}

func FirstBool(s []bool) bool {
	if len(s) > 0 {
		return s[0]
	}
	return false
}

func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

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

// BidResult holds a bid result from search/detail queries.
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

// DownloadID returns the ID used for PDF downloads.
// Uses BidIDParent if set, otherwise BidID.
func (b *BidResult) DownloadID() int {
	if b.BidIDParent > 0 {
		return b.BidIDParent
	}
	return b.BidID
}

// SearchFilters holds filter parameters for bid searches.
type SearchFilters struct {
	Query       string
	Departments []string
	Categories  []string
	StartDate   string // YYYY-MM-DD
	EndDate     string // YYYY-MM-DD
}

// BidSelectCols is the standard set of columns selected in bid queries.
const BidSelectCols = `b.id, b.bid_id, b.bid_number, b.bid_number_parent, b.bid_id_parent,
	b.category_name, b.total_quantity, b.start_date, b.end_date,
	b.is_high_value, b.ministry_name, b.department_name,
	COALESCE(bod.has_corrigendum, 0), COALESCE(bod.has_representation, 0)`

// SummaryStats holds high-level counts for the dashboard.
type SummaryStats struct {
	TotalBids          int    `json:"total_bids"`
	PDFsDownloaded     int    `json:"pdfs_downloaded"`
	PDFsPending        int    `json:"pdfs_pending"`
	CorrDocsTracked    int    `json:"corr_docs_tracked"`
	CorrDocsDownloaded int    `json:"corr_docs_downloaded"`
	BidsLast24h        int    `json:"bids_last_24h"`
	LastScrapeTime     string `json:"last_scrape_time"`
}

// PipelineStats holds tender pipeline breakdown by deadline proximity.
type PipelineStats struct {
	Active     int `json:"active"`
	Expired    int `json:"expired"`
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

// ScrapeTask identifies a pipeline stage.
type ScrapeTask string

const (
	TaskScrape      ScrapeTask = "scrape"
	TaskDownload    ScrapeTask = "download"
	TaskCorrigendum ScrapeTask = "corrigendum"
)

// ScrapeStatus represents the status of a scrape operation.
type ScrapeStatus string

const (
	StatusRunning   ScrapeStatus = "running"
	StatusCompleted ScrapeStatus = "completed"
	StatusError     ScrapeStatus = "error"
)

// ScrapeProgress is the real-time status snapshot sent to SSE listeners.
type ScrapeProgress struct {
	Task       ScrapeTask   `json:"task"`
	Status     ScrapeStatus `json:"status"`
	Message    string       `json:"message"`
	Current    int64        `json:"current"`
	Total      int64        `json:"total"`
	Errors     int64        `json:"errors"`
	StartedAt  time.Time    `json:"started_at"`
	ElapsedSec float64      `json:"elapsed_sec"`
}

// JSON serialises the progress for SSE data lines.
func (p ScrapeProgress) JSON() string {
	p.ElapsedSec = time.Since(p.StartedAt).Seconds()
	b, _ := json.Marshal(p)
	return string(b)
}

// RunWithProgress is a helper that wraps a task function with start/end progress callbacks.
func RunWithProgress(name string, onProgress ProgressFunc, fn func(ProgressFunc) error) error {
	if onProgress != nil {
		onProgress(0, 0, 0, "Starting "+name+"...")
	}
	err := fn(onProgress)
	if err != nil {
		if onProgress != nil {
			onProgress(0, 0, 1, name+" error: "+err.Error())
		}
		return err
	}
	if onProgress != nil {
		onProgress(0, 0, 0, name+" completed")
	}
	return nil
}
