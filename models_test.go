package main

import (
	"encoding/json"
	"testing"
)

func TestAPIResponseParsing(t *testing.T) {
	raw := `{"status":1,"code":200,"message":"Bid result","response":{"response":{"numFound":41158,"start":0,"numFoundExact":true,"docs":[{"id":"9119628","b_id":[9119628],"b_bid_number":["GEM/2026/R/643199"],"b_category_name":["Tea CTC 500 Gms Pack"],"b_total_quantity":[2600],"b_status":[1],"b_bid_type":[2],"b_is_bunch":[6],"b_type":[0],"b_bid_to_ra":[1],"final_start_date_sort":["2026-03-14T18:00:00Z"],"final_end_date_sort":["2026-03-17T14:11:14Z"],"b_is_custom_item":[0],"b_bid_number_parent":["GEM/2025/B/7001990"],"b_id_parent":[8714749],"is_high_value":[true],"b_ra_to_bid":[1],"ra_b_status":[1],"ba_official_details_minName":["Ministry of Defence"],"ba_official_details_deptName":["Department of Military Affairs"],"ba_is_global_tendering":[0],"is_rc_bid":[0]}]}}}`

	var resp APIResponse
	err := json.Unmarshal([]byte(raw), &resp)
	if err != nil {
		t.Fatalf("failed to parse: %v", err)
	}

	if resp.Response.Response.NumFound != 41158 {
		t.Errorf("expected numFound=41158, got %d", resp.Response.Response.NumFound)
	}
	if len(resp.Response.Response.Docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(resp.Response.Response.Docs))
	}

	doc := resp.Response.Response.Docs[0]
	if doc.ID != "9119628" {
		t.Errorf("expected id=9119628, got %s", doc.ID)
	}
	if firstInt(doc.BidIDParent) != 8714749 {
		t.Errorf("expected b_id_parent=8714749, got %d", firstInt(doc.BidIDParent))
	}
	if firstStr(doc.BidNumberParent) != "GEM/2025/B/7001990" {
		t.Errorf("expected parent bid number GEM/2025/B/7001990, got %s", firstStr(doc.BidNumberParent))
	}
	if firstStr(doc.MinistryName) != "Ministry of Defence" {
		t.Errorf("expected Ministry of Defence, got %s", firstStr(doc.MinistryName))
	}
}

func TestFirstHelpers(t *testing.T) {
	if firstStr(nil) != "" {
		t.Error("firstStr(nil) should be empty")
	}
	if firstStr([]string{"a", "b"}) != "a" {
		t.Error("firstStr should return first element")
	}
	if firstInt(nil) != 0 {
		t.Error("firstInt(nil) should be 0")
	}
	if firstBool(nil) != false {
		t.Error("firstBool(nil) should be false")
	}
}
