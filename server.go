package main

import (
	"database/sql"
	"log"

	"github.com/gin-gonic/gin"
)

func StartServer(db *sql.DB, downloadDir string, addr string) {
	r := gin.Default()
	r.LoadHTMLGlob("web/templates/*")
	r.Static("/static", "web/static")

	r.GET("/", func(c *gin.Context) {
		c.HTML(200, "index.tmpl", nil)
	})

	log.Printf("Starting server on %s", addr)
	r.Run(addr)
}
