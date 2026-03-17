package main

import (
	"database/sql"
	"log"

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
	r.LoadHTMLGlob("web/templates/*")
	r.Static("/static", "web/static")

	r.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.tmpl", nil)
	})
	r.GET("/search", SearchHandler(db))

	log.Printf("Starting server on %s", addr)
	r.Run(addr)
}
