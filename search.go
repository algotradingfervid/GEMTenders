package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

func SearchHandler(db *sql.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Query("q")
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		if page < 1 {
			page = 1
		}
		limit := 20
		offset := (page - 1) * limit

		filters := SearchFilters{
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

		var results []BidResult
		var total int
		var err error

		if hasFilters {
			results, total, err = SearchBidsFiltered(db, filters, limit, offset)
		} else {
			results, total, err = SearchBids(db, query, limit, offset)
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
