package server

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"gemtenders/internal/manager"
	"gemtenders/internal/models"
	"gemtenders/internal/store"
)

// DashboardPage renders the dashboard template.
func DashboardPage(c *gin.Context) {
	c.HTML(http.StatusOK, "dashboard.tmpl", nil)
}

// SearchHandler handles search requests with optional filters.
func SearchHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Query("q")
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		if page < 1 {
			page = 1
		}
		limit := 20
		offset := (page - 1) * limit

		filters := models.SearchFilters{
			Query:     query,
			StartDate: c.Query("start_date"),
			EndDate:   c.Query("end_date"),
		}
		if depts := c.Query("departments"); depts != "" {
			filters.Departments = strings.Split(depts, ",")
		}
		if cats := c.Query("categories"); cats != "" {
			filters.Categories = strings.Split(cats, ",")
		}

		hasFilters := len(filters.Departments) > 0 || len(filters.Categories) > 0 ||
			filters.StartDate != "" || filters.EndDate != ""

		var results []models.BidResult
		var total int
		var err error

		if hasFilters {
			results, total, err = store.SearchBidsFiltered(db, filters, limit, offset)
		} else {
			results, total, err = store.SearchBids(db, query, limit, offset)
		}

		if err != nil {
			c.HTML(http.StatusInternalServerError, "results.tmpl", gin.H{
				"Error": err.Error(),
			})
			return
		}

		totalPages := (total + limit - 1) / limit
		startRecord := offset + 1
		endRecord := offset + len(results)

		c.HTML(http.StatusOK, "results.tmpl", gin.H{
			"Results":     results,
			"Query":       query,
			"Total":       total,
			"Page":        page,
			"TotalPages":  totalPages,
			"HasPrev":     page > 1,
			"HasNext":     page < totalPages,
			"PrevPage":    page - 1,
			"NextPage":    page + 1,
			"StartRecord": startRecord,
			"EndRecord":   endRecord,
			"Departments": c.Query("departments"),
			"Categories":  c.Query("categories"),
			"StartDate":   c.Query("start_date"),
			"EndDate":     c.Query("end_date"),
		})
	}
}

// TenderHandler renders the tender detail page.
func TenderHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.Param("id")
		bid, err := store.GetBidByID(db, id)
		if err != nil {
			c.HTML(404, "index.tmpl", nil)
			return
		}

		pdfID := bid.DownloadID()

		// Fetch corrigendum details
		otherDetails, _ := store.GetBidOtherDetails(db, bid.BidID)
		corrDocs, _ := store.GetCorrigendumDocsForBid(db, bid.BidID)

		c.HTML(200, "tender.tmpl", gin.H{
			"Bid":          bid,
			"PDFID":        pdfID,
			"OtherDetails": otherDetails,
			"CorrDocs":     corrDocs,
		})
	}
}

// DepartmentTypeaheadHandler returns matching department names for autocomplete.
func DepartmentTypeaheadHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		values, err := store.SearchDistinctValues(db, "department_name", q, 10)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, values)
	}
}

// CategoryTypeaheadHandler returns matching category names for autocomplete.
func CategoryTypeaheadHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		values, err := store.SearchDistinctValues(db, "category_name", q, 10)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, values)
	}
}

// scrapeStartRequest is the JSON body for the scrape start endpoint.
type scrapeStartRequest struct {
	Tasks []string `json:"tasks"`
}

// ScrapeStartHandler starts a background scrape run with the specified tasks.
func ScrapeStartHandler(sm *manager.ScrapeManager, dbPath string, sessionCount int, downloadDir string) gin.HandlerFunc {
	validTasks := map[string]models.ScrapeTask{
		"scrape":      models.TaskScrape,
		"download":    models.TaskDownload,
		"corrigendum": models.TaskCorrigendum,
	}

	return func(c *gin.Context) {
		var req scrapeStartRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
			return
		}

		if len(req.Tasks) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no tasks specified"})
			return
		}

		var tasks []models.ScrapeTask
		for _, t := range req.Tasks {
			st, ok := validTasks[t]
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid task: %q", t)})
				return
			}
			tasks = append(tasks, st)
		}

		if err := sm.Start(dbPath, tasks, sessionCount, downloadDir); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "started", "tasks": req.Tasks})
	}
}

// ScrapeStatusHandler returns the current scrape status as JSON.
func ScrapeStatusHandler(sm *manager.ScrapeManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		running := sm.IsRunning()
		progress := sm.GetProgress()
		lastRun := sm.GetLastRun()

		resp := gin.H{
			"running":  running,
			"progress": progress,
		}
		if lastRun != nil {
			resp["last_run"] = lastRun
		}

		c.JSON(http.StatusOK, resp)
	}
}

// ScrapeProgressSSEHandler streams scrape progress via Server-Sent Events.
func ScrapeProgressSSEHandler(sm *manager.ScrapeManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		ch := sm.Subscribe()
		defer sm.Unsubscribe(ch)

		// Send current status immediately
		current := sm.GetProgress()
		c.SSEvent("message", current.JSON())
		c.Writer.Flush()

		c.Stream(func(w io.Writer) bool {
			select {
			case p, ok := <-ch:
				if !ok {
					return false
				}
				c.SSEvent("message", p.JSON())
				if p.Status == models.StatusCompleted {
					return false
				}
				return true
			case <-c.Request.Context().Done():
				return false
			}
		})
	}
}
