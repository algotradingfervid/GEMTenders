package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"path/filepath"
	"time"

	"github.com/gin-gonic/gin"
)

func StartServer(db *sql.DB, downloadDir string, addr string, sm *ScrapeManager, dbPath string, sessionCount int) {
	// Init FTS
	if err := InitFTS(db); err != nil {
		log.Fatalf("Failed to init FTS: %v", err)
	}
	if err := RebuildFTS(db); err != nil {
		log.Fatalf("Failed to rebuild FTS: %v", err)
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

	r.GET("/tender/:id", func(c *gin.Context) {
		id := c.Param("id")
		bid, err := GetBidByID(db, id)
		if err != nil {
			c.HTML(404, "index.tmpl", nil)
			return
		}

		pdfID := bid.BidIDParent
		if pdfID == 0 {
			pdfID = bid.BidID
		}

		// Fetch corrigendum details
		otherDetails, _ := GetBidOtherDetails(db, bid.BidID)
		corrDocs, _ := GetCorrigendumDocsForBid(db, bid.BidID)

		c.HTML(200, "tender.tmpl", gin.H{
			"Bid":          bid,
			"PDFID":        pdfID,
			"OtherDetails": otherDetails,
			"CorrDocs":     corrDocs,
		})
	})

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
	r.GET("/api/stats/summary", SummaryHandler(db))
	r.GET("/api/stats/pipeline", PipelineHandler(db))
	r.GET("/api/stats/departments", DepartmentsBreakdownHandler(db))
	r.GET("/api/stats/categories", CategoriesBreakdownHandler(db))
	r.GET("/api/stats/timeline", TimelineHandler(db))

	// Scrape control API
	r.POST("/api/scrape/start", ScrapeStartHandler(sm, dbPath, sessionCount))
	r.GET("/api/scrape/status", ScrapeStatusHandler(sm))
	r.GET("/api/scrape/progress", ScrapeProgressSSEHandler(sm))

	// Typeahead API
	r.GET("/api/departments", DepartmentTypeaheadHandler(db))
	r.GET("/api/categories", CategoryTypeaheadHandler(db))

	log.Printf("Starting server on %s", addr)
	r.Run(addr)
}
