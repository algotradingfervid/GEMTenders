package main

import (
	"encoding/json"
	"testing"
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

	var resp OtherDetailsResponse
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

	var resp OtherDetailsResponse
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
