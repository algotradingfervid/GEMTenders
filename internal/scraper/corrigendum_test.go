package scraper

import (
	"encoding/json"
	"testing"

	"gemtenders/internal/models"
)

// Sample HTML from the viewCorrigendum API (with download link)
const sampleCorrigendumWithDocs = `
<div class="border block">
    <div class="block_bid_no"><p> Corrigendum Details</p></div>
    <div class="clearfix" style="height:3px"></div>
    <div class="col-md-12 col-xs-12">
        <div class="well"><div class="col-block"> <span id=span_4098546><strong >Modified On: </strong><span>2026-03-11 12:47:58</span></span></div><div class="col-block" style="width:48% !important;"><a href="/bidding/bid/showcorrigendumpdf/4098546/8960898"><span class="glyphicon glyphicon-download-alt"></span> Download</a></div><div class="clearfix"></div></div>
        <div class="well"><div class="col-block"> <span id=span_4098541><strong >Modified On: </strong><span>2026-03-11 12:46:24</span></span></div><div class="col-block" style="width:48% !important;">Bid extended to <strong>2026-03-18 09:00:00</strong></div><div class="clearfix"></div></div>
        <div class="well"><div class="col-block"><strong>&nbsp;</strong><span></span></div><div class="col-block" style="width:48%;">Bid Opening Date: <strong>2026-03-18 09:30:00</strong></div><div class="clearfix"></div></div>
        <div class="well"><div class="col-block"> <span id=span_4089281><strong >Modified On: </strong><span>2026-03-09 11:18:47</span></span></div><div class="col-block" style="width:48% !important;">Bid extended to <strong>2026-03-13 09:00:00</strong></div><div class="clearfix"></div></div>
        <div class="well"><div class="col-block"><strong>&nbsp;</strong><span></span></div><div class="col-block" style="width:48%;">Bid Opening Date: <strong>2026-03-13 09:30:00</strong></div><div class="clearfix"></div></div>
        <div class="clearfix"></div>
    </div>
    <div class="clearfix"></div>
</div>`

// Sample HTML without download links
const sampleCorrigendumNoDocs = `
<div class="border block">
    <div class="block_bid_no"><p> Corrigendum Details</p></div>
    <div class="clearfix" style="height:3px"></div>
    <div class="col-md-12 col-xs-12">
        <div class="well"><div class="col-block"><strong>&nbsp;</strong><span></span></div><div class="col-block" style="width:48%;">Bid Opening Date: <strong>2026-03-18 09:30:00</strong></div><div class="clearfix"></div></div>
        <div class="clearfix"></div>
    </div>
    <div class="clearfix"></div>
</div>`

func TestParseCorrigendumDocs(t *testing.T) {
	docs := ParseCorrigendumDocs(sampleCorrigendumWithDocs, 8960898)

	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].CorrigendumID != 4098546 {
		t.Errorf("expected corrigendum_id=4098546, got %d", docs[0].CorrigendumID)
	}
	if docs[0].DownloadURL != "/bidding/bid/showcorrigendumpdf/4098546/8960898" {
		t.Errorf("unexpected download URL: %s", docs[0].DownloadURL)
	}
	if docs[0].ModifiedOn != "2026-03-11 12:47:58" {
		t.Errorf("expected modified_on='2026-03-11 12:47:58', got '%s'", docs[0].ModifiedOn)
	}
}

func TestParseCorrigendumDocsEmpty(t *testing.T) {
	docs := ParseCorrigendumDocs(sampleCorrigendumNoDocs, 8783867)
	if len(docs) != 0 {
		t.Errorf("expected 0 documents, got %d", len(docs))
	}
}

func TestParseLatestEndDate(t *testing.T) {
	date := ParseLatestEndDate(sampleCorrigendumWithDocs)
	if date != "2026-03-18 09:00:00" {
		t.Errorf("expected '2026-03-18 09:00:00', got '%s'", date)
	}
}

func TestParseLatestEndDateNone(t *testing.T) {
	date := ParseLatestEndDate(sampleCorrigendumNoDocs)
	if date != "" {
		t.Errorf("expected empty, got '%s'", date)
	}
}

func TestParseCorrigendumCount(t *testing.T) {
	count := ParseCorrigendumCount(sampleCorrigendumWithDocs)
	if count != 5 {
		t.Errorf("expected 5 well blocks, got %d", count)
	}

	count = ParseCorrigendumCount(sampleCorrigendumNoDocs)
	if count != 1 {
		t.Errorf("expected 1 well block, got %d", count)
	}
}

func TestParseOtherDetailsResponse(t *testing.T) {
	raw := `{"status":1,"code":200,"message":"Request processed successfully","response":{"corrigendum":true,"representation":false}}`

	var resp models.OtherDetailsResponse
	err := json.Unmarshal([]byte(raw), &resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !resp.Response.Corrigendum {
		t.Error("expected corrigendum=true")
	}
	if resp.Response.Representation {
		t.Error("expected representation=false")
	}
}

func TestParseOtherDetailsBothTrue(t *testing.T) {
	raw := `{"status":1,"code":200,"message":"Request processed successfully","response":{"corrigendum":true,"representation":true}}`

	var resp models.OtherDetailsResponse
	err := json.Unmarshal([]byte(raw), &resp)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if !resp.Response.Corrigendum {
		t.Error("expected corrigendum=true")
	}
	if !resp.Response.Representation {
		t.Error("expected representation=true")
	}
}

func TestAPIResponseParsing(t *testing.T) {
	raw := `{"status":1,"code":200,"message":"Bid result","response":{"response":{"numFound":41158,"start":0,"numFoundExact":true,"docs":[{"id":"9119628","b_id":[9119628],"b_bid_number":["GEM/2026/R/643199"],"b_category_name":["Tea CTC 500 Gms Pack"],"b_total_quantity":[2600],"b_status":[1],"b_bid_type":[2],"b_is_bunch":[6],"b_type":[0],"b_bid_to_ra":[1],"final_start_date_sort":["2026-03-14T18:00:00Z"],"final_end_date_sort":["2026-03-17T14:11:14Z"],"b_is_custom_item":[0],"b_bid_number_parent":["GEM/2025/B/7001990"],"b_id_parent":[8714749],"is_high_value":[true],"b_ra_to_bid":[1],"ra_b_status":[1],"ba_official_details_minName":["Ministry of Defence"],"ba_official_details_deptName":["Department of Military Affairs"],"ba_is_global_tendering":[0],"is_rc_bid":[0]}]}}}`

	var resp models.APIResponse
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
	if models.FirstInt(doc.BidIDParent) != 8714749 {
		t.Errorf("expected b_id_parent=8714749, got %d", models.FirstInt(doc.BidIDParent))
	}
	if models.FirstStr(doc.BidNumberParent) != "GEM/2025/B/7001990" {
		t.Errorf("expected parent bid number GEM/2025/B/7001990, got %s", models.FirstStr(doc.BidNumberParent))
	}
	if models.FirstStr(doc.MinistryName) != "Ministry of Defence" {
		t.Errorf("expected Ministry of Defence, got %s", models.FirstStr(doc.MinistryName))
	}
}

func TestFirstHelpers(t *testing.T) {
	if models.FirstStr(nil) != "" {
		t.Error("FirstStr(nil) should be empty")
	}
	if models.FirstStr([]string{"a", "b"}) != "a" {
		t.Error("FirstStr should return first element")
	}
	if models.FirstInt(nil) != 0 {
		t.Error("FirstInt(nil) should be 0")
	}
	if models.FirstBool(nil) != false {
		t.Error("FirstBool(nil) should be false")
	}
}
