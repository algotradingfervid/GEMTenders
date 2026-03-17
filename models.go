package main

import "net/http"

// Session holds CSRF token and cookies from browser bootstrap
type Session struct {
	CSRFToken string
	Cookies   []*http.Cookie
}

// APIPayload is the POST body for /all-bids-data
type APIPayload struct {
	Page   int       `json:"page"`
	Param  APIParam  `json:"param"`
	Filter APIFilter `json:"filter"`
}

type APIParam struct {
	SearchBid  string `json:"searchBid"`
	SearchType string `json:"searchType"`
}

type APIFilter struct {
	BidStatusType string       `json:"bidStatusType"`
	ByType        string       `json:"byType"`
	HighBidValue  string       `json:"highBidValue"`
	ByEndDate     APIDateRange `json:"byEndDate"`
	Sort          string       `json:"sort"`
}

type APIDateRange struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// APIResponse is the top-level JSON response
type APIResponse struct {
	Status   int    `json:"status"`
	Code     int    `json:"code"`
	Message  string `json:"message"`
	Response struct {
		Response struct {
			NumFound      int      `json:"numFound"`
			Start         int      `json:"start"`
			NumFoundExact bool     `json:"numFoundExact"`
			Docs          []BidDoc `json:"docs"`
		} `json:"response"`
	} `json:"response"`
}

// BidDoc is a single bid record from the API
// All fields come as arrays from Solr — we flatten them during DB insert
type BidDoc struct {
	ID              string   `json:"id"`
	BidID           []int    `json:"b_id"`
	BidNumber       []string `json:"b_bid_number"`
	BidNumberParent []string `json:"b_bid_number_parent"`
	BidIDParent     []int    `json:"b_id_parent"`
	CategoryName    []string `json:"b_category_name"`
	TotalQuantity   []int    `json:"b_total_quantity"`
	Status          []int    `json:"b_status"`
	BidType         []int    `json:"b_bid_type"`
	Type            []int    `json:"b_type"`
	IsBunch         []int    `json:"b_is_bunch"`
	BidToRA         []int    `json:"b_bid_to_ra"`
	StartDate       []string `json:"final_start_date_sort"`
	EndDate         []string `json:"final_end_date_sort"`
	IsHighValue     []bool   `json:"is_high_value"`
	MinistryName    []string `json:"ba_official_details_minName"`
	DepartmentName  []string `json:"ba_official_details_deptName"`
	IsGlobalTender  []int    `json:"ba_is_global_tendering"`
	IsRCBid         []int    `json:"is_rc_bid"`
	IsCustomItem    []int    `json:"b_is_custom_item"`
}

// Helper to safely get first element from Solr arrays
func firstStr(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}

func firstInt(s []int) int {
	if len(s) > 0 {
		return s[0]
	}
	return 0
}

func firstBool(s []bool) bool {
	if len(s) > 0 {
		return s[0]
	}
	return false
}
