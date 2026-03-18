package server

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"

	"gemtenders/internal/manager"
	"gemtenders/internal/store"
)

// StartServer initializes routes and starts the HTTP server.
func StartServer(db *sql.DB, downloadDir string, addr string, sm *manager.ScrapeManager, dbPath string, sessionCount int) {
	// Init FTS (create virtual table if not exists) — do NOT rebuild on startup
	if err := store.InitFTS(db); err != nil {
		log.Fatalf("Failed to init FTS: %v", err)
	}

	r := gin.Default()
	r.SetFuncMap(template.FuncMap{
		"safeHTML": func(s string) template.HTML { return template.HTML(s) },
		"formatDate": func(s string) string {
			// GEM API dates have a Z suffix but are actually IST, not UTC.
			// Parse as literal strings without timezone conversion.
			formats := []string{
				"2006-01-02T15:04:05Z",
				"2006-01-02T15:04:05",
				"2006-01-02 15:04:05",
			}
			for _, f := range formats {
				if t, err := time.Parse(f, s); err == nil {
					return t.Format("02 Jan 2006, 3:04 PM") + " IST"
				}
			}
			return s
		},
	})
	r.LoadHTMLGlob("web/templates/*")
	r.Static("/static", "web/static")

	r.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.tmpl", nil)
	})
	r.GET("/search", SearchHandler(db))

	r.GET("/tender/:id", TenderHandler(db))

	r.GET("/pdf/:id", func(c *gin.Context) {
		id := c.Param("id")
		filePath := downloadDir + "/GeM-Bidding-" + id + ".pdf"
		c.File(filePath)
	})

	r.GET("/corrigendum-pdf/:corrId/:bidId", func(c *gin.Context) {
		corrID := c.Param("corrId")
		bidID := c.Param("bidId")
		filePath := filepath.Join(downloadDir, "corrigendums",
			fmt.Sprintf("Corrigendum-%s-%s.pdf", corrID, bidID))
		c.File(filePath)
	})

	// Dashboard
	r.GET("/dashboard", DashboardPage)

	// Stats API
	r.GET("/api/stats/summary", jsonHandler(func(c *gin.Context) (any, error) {
		return store.GetSummaryStats(db)
	}))
	r.GET("/api/stats/pipeline", jsonHandler(func(c *gin.Context) (any, error) {
		return store.GetPipelineStats(db)
	}))
	r.GET("/api/stats/departments", jsonHandler(func(c *gin.Context) (any, error) {
		return store.GetTopDepartments(db, 10)
	}))
	r.GET("/api/stats/categories", jsonHandler(func(c *gin.Context) (any, error) {
		return store.GetTopCategories(db, 10)
	}))
	r.GET("/api/stats/timeline", jsonHandler(func(c *gin.Context) (any, error) {
		return store.GetBidsTimeline(db, 30)
	}))

	// Scrape control API
	r.POST("/api/scrape/start", ScrapeStartHandler(sm, dbPath, sessionCount, downloadDir))
	r.GET("/api/scrape/status", ScrapeStatusHandler(sm))
	r.GET("/api/scrape/progress", ScrapeProgressSSEHandler(sm))

	// Typeahead API
	r.GET("/api/departments", DepartmentTypeaheadHandler(db))
	r.GET("/api/categories", CategoryTypeaheadHandler(db))

	log.Printf("Starting server on %s", addr)
	r.Run(addr)
}

// jsonHandler wraps a handler function that returns (any, error) into a gin.HandlerFunc.
func jsonHandler(fn func(*gin.Context) (any, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		result, err := fn(c)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, result)
	}
}
