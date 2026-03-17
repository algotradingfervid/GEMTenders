package main

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// Matches: /bidding/bid/showcorrigendumpdf/{corrigendum_id}/{bid_id}
	reCorrigendumPDFLink = regexp.MustCompile(`href="(/bidding/bid/showcorrigendumpdf/(\d+)/(\d+))"`)

	// Matches: Modified On: </strong><span>2026-03-11 12:47:58</span>
	reModifiedOn = regexp.MustCompile(`id=span_(\d+)>.*?Modified On:.*?<span>([\d\- :]+)</span>`)

	// Matches: Bid extended to <strong>2026-03-18 09:00:00</strong>
	reBidExtended = regexp.MustCompile(`Bid extended to\s*<strong>([\d\- :]+)</strong>`)

	// Matches: <div class="well">
	reWellBlock = regexp.MustCompile(`<div class="well">`)
)

// ParseCorrigendumDocs extracts download links from corrigendum HTML.
func ParseCorrigendumDocs(html string, bidID int) []CorrigendumDoc {
	var docs []CorrigendumDoc

	linkMatches := reCorrigendumPDFLink.FindAllStringSubmatch(html, -1)
	for _, m := range linkMatches {
		corrID, _ := strconv.Atoi(m[2])
		doc := CorrigendumDoc{
			BidID:         bidID,
			CorrigendumID: corrID,
			DownloadURL:   m[1],
		}

		// Find modified_on for this corrigendum_id by looking for span_{id}
		modMatches := reModifiedOn.FindAllStringSubmatch(html, -1)
		for _, mod := range modMatches {
			if mod[1] == m[2] {
				doc.ModifiedOn = strings.TrimSpace(mod[2])
				break
			}
		}

		docs = append(docs, doc)
	}

	return docs
}

// ParseLatestEndDate extracts the latest "Bid extended to" date from corrigendum HTML.
// Returns empty string if no extension found.
func ParseLatestEndDate(html string) string {
	matches := reBidExtended.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return ""
	}

	// Dates are in "YYYY-MM-DD HH:MM:SS" format — string comparison works for finding latest
	latest := ""
	for _, m := range matches {
		date := strings.TrimSpace(m[1])
		if date > latest {
			latest = date
		}
	}
	return latest
}

// ParseCorrigendumCount counts the number of corrigendum entries (well blocks) in the HTML.
func ParseCorrigendumCount(html string) int {
	return len(reWellBlock.FindAllString(html, -1))
}
