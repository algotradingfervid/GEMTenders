package main

import (
	"database/sql"
	"fmt"
	"html/template"
	"log"
	"path/filepath"

	"github.com/gin-gonic/gin"
)

func StartServer(db *sql.DB, downloadDir string, addr string) {
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

	log.Printf("Starting server on %s", addr)
	r.Run(addr)
}
