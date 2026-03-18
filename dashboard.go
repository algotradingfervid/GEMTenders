package main

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// DashboardPage renders the dashboard template.
func DashboardPage(c *gin.Context) {
	c.HTML(http.StatusOK, "dashboard.tmpl", nil)
}

// SummaryHandler returns aggregate dashboard statistics as JSON.
func SummaryHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats, err := GetSummaryStats(db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, stats)
	}
}

// PipelineHandler returns tender pipeline breakdown as JSON.
func PipelineHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		stats, err := GetPipelineStats(db)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, stats)
	}
}

// DepartmentsBreakdownHandler returns top 10 departments by bid count as JSON.
func DepartmentsBreakdownHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := GetTopDepartments(db, 10)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, items)
	}
}

// CategoriesBreakdownHandler returns top 10 categories by bid count as JSON.
func CategoriesBreakdownHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := GetTopCategories(db, 10)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, items)
	}
}

// TimelineHandler returns daily bid counts for the last 30 days as JSON.
func TimelineHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		points, err := GetBidsTimeline(db, 30)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, points)
	}
}

// DepartmentTypeaheadHandler returns matching department names for autocomplete.
func DepartmentTypeaheadHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := c.Query("q")
		values, err := SearchDistinctValues(db, "department_name", q, 10)
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
		values, err := SearchDistinctValues(db, "category_name", q, 10)
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
func ScrapeStartHandler(sm *ScrapeManager, dbPath string, sessionCount int) gin.HandlerFunc {
	validTasks := map[string]ScrapeTask{
		"scrape":      TaskScrape,
		"download":    TaskDownload,
		"corrigendum": TaskCorrigendum,
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

		var tasks []ScrapeTask
		for _, t := range req.Tasks {
			st, ok := validTasks[t]
			if !ok {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid task: %q", t)})
				return
			}
			tasks = append(tasks, st)
		}

		if err := sm.Start(dbPath, tasks, sessionCount); err != nil {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{"status": "started", "tasks": req.Tasks})
	}
}

// ScrapeStatusHandler returns the current scrape status as JSON.
func ScrapeStatusHandler(sm *ScrapeManager) gin.HandlerFunc {
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
func ScrapeProgressSSEHandler(sm *ScrapeManager) gin.HandlerFunc {
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
				if p.Status == "completed" {
					return false
				}
				return true
			case <-c.Request.Context().Done():
				return false
			}
		})
	}
}
