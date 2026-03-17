package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// FetchOtherDetails calls POST /public-bid-other-details/{bid_id}
// Returns whether corrigendum and representation exist for this bid.
func FetchOtherDetails(sp *SessionPair, bidID int) (hasCorr bool, hasRepr bool, err error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/public-bid-other-details/%d", baseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return false, false, err
	}
	setAjaxHeaders(req)

	resp, err := sp.Client.Do(req)
	if err != nil {
		return false, false, fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return false, false, err
	}

	var apiResp OtherDetailsResponse
	if err := json.Unmarshal(body, &apiResp); err != nil {
		return false, false, fmt.Errorf("unmarshal: %w", err)
	}

	return apiResp.Response.Corrigendum, apiResp.Response.Representation, nil
}

// FetchCorrigendumHTML calls POST /bidding/bid/viewCorrigendum/{bid_id}
// Returns the raw HTML response body.
func FetchCorrigendumHTML(sp *SessionPair, bidID int) (string, error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/bidding/bid/viewCorrigendum/%d", baseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}
	setAjaxHeaders(req)
	req.Header.Set("Accept", "text/html, */*; q=0.01")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// FetchRepresentationHTML calls POST /bidding/bid/viewRepresentation/{bid_id}
// Returns the raw HTML response body.
func FetchRepresentationHTML(sp *SessionPair, bidID int) (string, error) {
	formData := url.Values{}
	formData.Set("csrf_bd_gem_nk", sp.CSRFToken)

	reqURL := fmt.Sprintf("%s/bidding/bid/viewRepresentation/%d", baseURL, bidID)
	req, err := http.NewRequest("POST", reqURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}
	setAjaxHeaders(req)
	req.Header.Set("Accept", "text/html, */*; q=0.01")

	resp, err := sp.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	body, err := readResponseBody(resp)
	if err != nil {
		return "", err
	}

	return string(body), nil
}

// setAjaxHeaders sets common headers for GEM AJAX requests.
func setAjaxHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Origin", baseURL)
	req.Header.Set("Referer", baseURL+"/all-bids")
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
}

// readResponseBody reads the response body, handling gzip if needed.
func readResponseBody(resp *http.Response) ([]byte, error) {
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var reader io.Reader = resp.Body
	if resp.Header.Get("Content-Encoding") == "gzip" {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gzReader.Close()
		reader = gzReader
	}

	return io.ReadAll(reader)
}
