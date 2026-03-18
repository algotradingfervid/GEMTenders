#!/usr/bin/env python3
"""
Standalone GEM Tender Results Scraper
Fetches bid listings from bidplus.gem.gov.in and extracts tender result data.

Uses Playwright (headless Chromium) to bypass GEM's F5 WAF/TLS fingerprinting.
Dependencies: pip install playwright beautifulsoup4 && playwright install chromium

Usage:
    python gem_scraper.py                    # Fetch first page of bids
    python gem_scraper.py --pages 5          # Fetch 5 pages of bid listings
    python gem_scraper.py --bid-id 300535    # Fetch a specific bid result
    python gem_scraper.py --fetch-results    # Fetch listings + each bid's results
    python gem_scraper.py --headed           # Run with visible browser window
"""

import argparse
import csv
import json
import re
import sys
import time
from dataclasses import dataclass, field, asdict

from bs4 import BeautifulSoup
from playwright.sync_api import sync_playwright, Page

BASE_URL = "https://bidplus.gem.gov.in"
ALL_BIDS_DATA_URL = f"{BASE_URL}/all-bids-data"
BID_RESULT_URL = f"{BASE_URL}/bidding/bid/getBidResultView"

PAGE_SIZE = 10  # GEM returns 10 bids per page


# ── Data classes ──────────────────────────────────────────────────────────────

@dataclass
class BidListing:
    bid_id: int = 0
    bid_number: str = ""
    category: str = ""
    quantity: int = 0
    status: int = 0
    start_date: str = ""
    end_date: str = ""
    ministry: str = ""
    department: str = ""
    is_high_value: bool = False
    bid_type: int = 0  # 0=product, 1=service


@dataclass
class SellerTechnical:
    sno: int = 0
    name: str = ""
    offered_item: str = ""
    make: str = ""
    model: str = ""
    participated_on: str = ""
    mse_mii_status: str = ""
    status: str = ""  # Qualified / Disqualified


@dataclass
class SellerFinancial:
    sno: int = 0
    name: str = ""
    offered_item: str = ""
    total_price: float = 0.0
    rank: str = ""  # L1, L2, ...


@dataclass
class BidResult:
    bid_id: int = 0
    bid_number: str = ""
    bid_status: str = ""
    quantity: str = ""
    start_date: str = ""
    end_date: str = ""
    lifecycle_days: str = ""
    buyer_name: str = ""
    buyer_address: str = ""
    buyer_state: str = ""
    buyer_department: str = ""
    buyer_ministry: str = ""
    buyer_organisation: str = ""
    buyer_office: str = ""
    technical_sellers: list = field(default_factory=list)
    financial_sellers: list = field(default_factory=list)
    ra_note: str = ""


# ── Scraper ───────────────────────────────────────────────────────────────────

class GEMScraper:
    def __init__(self, headed: bool = False, delay: float = 1.5):
        self.headed = headed
        self.delay = delay
        self._pw = None
        self._browser = None
        self._page: Page | None = None
        self.csrf_token = ""

    def start(self):
        self._pw = sync_playwright().start()
        self._browser = self._pw.chromium.launch(headless=not self.headed)
        self._page = self._browser.new_page()
        self._page.set_extra_http_headers({
            "Accept-Language": "en-GB,en-US;q=0.9,en;q=0.8",
        })

    def stop(self):
        if self._browser:
            self._browser.close()
        if self._pw:
            self._pw.stop()

    def _wait(self):
        time.sleep(self.delay)

    def init_session(self):
        """Navigate to all-bids to establish WAF cookies and get CSRF token."""
        print("[*] Initializing session (loading GEM portal)...")
        self._page.goto(f"{BASE_URL}/all-bids", wait_until="networkidle", timeout=30000)

        # Extract CSRF from cookies
        cookies = self._page.context.cookies()
        for c in cookies:
            if c["name"] == "csrf_gem_cookie":
                self.csrf_token = c["value"]
                break

        if not self.csrf_token:
            # Fallback: extract from page
            content = self._page.content()
            m = re.search(r'csrf_bd_gem_nk["\s]*(?:value="|:\s*["\'])([a-f0-9]+)', content)
            if m:
                self.csrf_token = m.group(1)

        if self.csrf_token:
            print(f"[+] CSRF token: {self.csrf_token[:16]}...")
        else:
            print("[!] Warning: Could not extract CSRF token")

    # ── Bid Listings (JSON API via page.evaluate) ─────────────────────────

    def fetch_bid_listings(
        self,
        page: int = 0,
        search: str = "",
        status_type: str = "bidrastatus",
        by_type: str = "all",
        sort: str = "Bid-End-Date-Latest",
        date_from: str = "",
        date_to: str = "",
    ) -> tuple[list[BidListing], int]:
        payload_obj = {
            "param": {
                "searchBid": search,
                "searchType": "fullText",
            },
            "filter": {
                "bidStatusType": status_type,
                "byType": by_type,
                "highBidValue": "",
                "byEndDate": {"from": date_from, "to": date_to},
                "sort": sort,
            },
        }
        if page > 0:
            payload_obj["param"]["start"] = page * PAGE_SIZE

        payload_json = json.dumps(payload_obj)
        csrf = self.csrf_token

        # Use fetch() inside the browser context (has all WAF cookies)
        js = f"""
        async () => {{
            const resp = await fetch("{ALL_BIDS_DATA_URL}", {{
                method: "POST",
                headers: {{
                    "Content-Type": "application/x-www-form-urlencoded; charset=UTF-8",
                    "X-Requested-With": "XMLHttpRequest",
                }},
                body: "payload=" + encodeURIComponent('{payload_json}') + "&csrf_bd_gem_nk={csrf}",
            }});
            return await resp.json();
        }}
        """
        data = self._page.evaluate(js)

        if data.get("status") != 1:
            print(f"[!] API error: {data.get('message', 'unknown')}")
            return [], 0

        inner = data["response"]["response"]
        total = inner.get("numFound", 0)
        docs = inner.get("docs", [])

        listings = []
        for doc in docs:
            listing = BidListing(
                bid_id=doc.get("b_id", [0])[0],
                bid_number=doc.get("b_bid_number", [""])[0],
                category=doc.get("b_category_name", [""])[0],
                quantity=doc.get("b_total_quantity", [0])[0],
                status=doc.get("b_status", [0])[0],
                start_date=doc.get("final_start_date_sort", [""])[0],
                end_date=doc.get("final_end_date_sort", [""])[0],
                ministry=(doc.get("ba_official_details_minName") or [""])[0],
                department=(doc.get("ba_official_details_deptName") or [""])[0],
                is_high_value=(
                    doc.get("is_high_value", [False])[0]
                    if isinstance(doc.get("is_high_value"), list)
                    else doc.get("is_high_value", False)
                ),
                bid_type=doc.get("b_type", [0])[0],
            )
            listings.append(listing)

        return listings, total

    # ── Bid Result Detail (HTML page) ─────────────────────────────────────

    def fetch_bid_result(self, bid_id: int) -> BidResult | None:
        url = f"{BID_RESULT_URL}/{bid_id}"
        self._page.goto(url, wait_until="networkidle", timeout=30000)
        html = self._page.content()

        soup = BeautifulSoup(html, "html.parser")
        result = BidResult(bid_id=bid_id)

        self._parse_details(soup, result)
        result.technical_sellers = self._parse_technical_table(soup)
        result.financial_sellers = self._parse_financial_table(soup)

        # Check for RA note
        for el in soup.find_all(["div", "span", "p"]):
            text = el.get_text(strip=True)
            if "No participation" in text or "Please view Bid Results" in text:
                result.ra_note = text
                break

        return result

    # ── HTML Parsers ──────────────────────────────────────────────────────

    def _parse_details(self, soup: BeautifulSoup, result: BidResult):
        text = soup.get_text(" ", strip=True)

        def extract(pattern, default=""):
            m = re.search(pattern, text)
            return m.group(1).strip() if m else default

        result.bid_number = extract(r"(?:Bid|RA)\s*Number:\s*(GEM/\S+)")
        result.bid_status = extract(r"(?:Bid|RA)\s*Status:\s*(\w+)")
        result.quantity = extract(r"Quantity:\s*(\d+)")
        result.lifecycle_days = extract(r"Life Cycle.*?:\s*(\d+)")
        result.start_date = extract(r"Start Date\s*/\s*Time:\s*([\d\-: ]+)")
        result.end_date = extract(r"End Date\s*/\s*Time:\s*([\d\-: ]+)")
        result.buyer_name = extract(r"Name:\s*(.+?)(?:\s*Address:)")
        result.buyer_address = extract(r"Address:\s*(.+?)(?:\s*(?:State|Ministry):)")
        result.buyer_state = extract(r"State:\s*(.+?)(?:\s*(?:Department|Ministry):)")
        result.buyer_ministry = extract(r"Ministry:\s*(.+?)(?:\s*Department:)")
        result.buyer_department = extract(r"Department:\s*(.+?)(?:\s*Organisation:)")
        result.buyer_organisation = extract(r"Organisation:\s*(.+?)(?:\s*Office:)")
        result.buyer_office = extract(r"Office:\s*(.+?)(?:\s*(?:×|Consignee|$))")

    @staticmethod
    def _clean_seller_name(raw: str) -> str:
        return re.sub(
            r"\s*Under\s+(PMA|Make\s+in\s+India).*", "", raw, flags=re.IGNORECASE
        ).strip()

    def _find_table_in_panel(self, soup: BeautifulSoup, keyword: str):
        """Find a table inside a .panel whose heading contains keyword."""
        for panel in soup.find_all("div", class_="panel"):
            heading_el = panel.find(class_="panel-heading")
            if not heading_el:
                heading_el = panel.find(["h4", "h3", "h2"])
            if heading_el and keyword in heading_el.get_text(strip=True).upper():
                return panel.find("table")
        # Fallback: heading search
        for h in soup.find_all(["h4", "h3", "h2"]):
            if keyword in h.get_text(strip=True).upper():
                return h.find_next("table")
        return None

    def _parse_technical_table(self, soup: BeautifulSoup) -> list[dict]:
        sellers = []
        table = self._find_table_in_panel(soup, "TECHNICAL EVALUATION")
        if not table:
            table = self._find_table_in_panel(soup, "RA EVALUATION")
        if not table:
            return sellers

        for row in table.find_all("tr"):
            cells = [td.get_text(strip=True) for td in row.find_all(["td", "th"])]
            if not cells or not cells[0].isdigit():
                continue

            s = SellerTechnical()
            s.sno = int(cells[0])
            s.name = self._clean_seller_name(cells[1]) if len(cells) > 1 else ""

            offered = cells[2] if len(cells) > 2 else ""
            s.offered_item = offered
            make_m = re.search(r"Make\s*:\s*(.+?)(?:\s*Model|$)", offered)
            model_m = re.search(r"Model\s*:\s*(.+)", offered)
            s.make = make_m.group(1).strip() if make_m else ""
            s.model = model_m.group(1).strip() if model_m else ""

            s.participated_on = cells[3] if len(cells) > 3 else ""
            s.mse_mii_status = cells[4] if len(cells) > 4 else ""
            s.status = cells[5] if len(cells) > 5 else (cells[-1] if cells else "")

            sellers.append(asdict(s))

        return sellers

    def _parse_financial_table(self, soup: BeautifulSoup) -> list[dict]:
        sellers = []
        table = self._find_table_in_panel(soup, "FINANCIAL EVALUATION")
        if not table:
            return sellers

        for row in table.find_all("tr"):
            cells = [td.get_text(strip=True) for td in row.find_all(["td", "th"])]
            if not cells or not cells[0].isdigit():
                continue

            s = SellerFinancial()
            s.sno = int(cells[0])
            s.name = self._clean_seller_name(cells[1]) if len(cells) > 1 else ""
            s.offered_item = cells[2] if len(cells) > 2 else ""

            # GEM uses backtick (`) as rupee symbol
            if len(cells) > 3:
                price_str = re.sub(r"[₹`',\s]", "", cells[3])
                try:
                    s.total_price = float(price_str)
                except ValueError:
                    s.total_price = 0.0

            s.rank = cells[4] if len(cells) > 4 else (cells[-1] if cells else "")
            sellers.append(asdict(s))

        return sellers

    # ── High-level workflows ──────────────────────────────────────────────

    def scrape_listings(self, num_pages: int = 1, **kwargs) -> list[BidListing]:
        all_listings = []
        total = None

        for pg in range(num_pages):
            print(f"[*] Fetching listings page {pg + 1}/{num_pages}...")
            listings, found = self.fetch_bid_listings(page=pg, **kwargs)
            if total is None:
                total = found
                print(f"[+] Total bids in GEM: {total:,}")

            all_listings.extend(listings)
            print(f"    Got {len(listings)} bids (collected: {len(all_listings)})")

            if not listings or len(all_listings) >= total:
                break
            self._wait()

        return all_listings

    def scrape_results(self, bid_ids: list[int]) -> list[BidResult]:
        results = []
        for i, bid_id in enumerate(bid_ids):
            print(f"[*] Fetching result {i + 1}/{len(bid_ids)}: bid_id={bid_id}")
            try:
                result = self.fetch_bid_result(bid_id)
                if result:
                    tech = len(result.technical_sellers)
                    fin = len(result.financial_sellers)
                    print(f"    {result.bid_number} — {tech} tech, {fin} financial sellers")
                    results.append(result)
            except Exception as e:
                print(f"    [!] Error: {e}")
            self._wait()

        return results


# ── Export helpers ─────────────────────────────────────────────────────────────

def export_listings_csv(listings: list[BidListing], path: str):
    if not listings:
        return
    with open(path, "w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=asdict(listings[0]).keys())
        writer.writeheader()
        for item in listings:
            writer.writerow(asdict(item))
    print(f"[+] Listings saved to {path}")


def export_results_json(results: list[BidResult], path: str):
    if not results:
        return
    data = [asdict(r) for r in results]
    with open(path, "w", encoding="utf-8") as f:
        json.dump(data, f, indent=2, ensure_ascii=False)
    print(f"[+] Results saved to {path}")


def export_results_csv(results: list[BidResult], path: str):
    """Export flattened results — one row per financial seller."""
    if not results:
        return
    rows = []
    for r in results:
        base = {
            "bid_id": r.bid_id,
            "bid_number": r.bid_number,
            "bid_status": r.bid_status,
            "quantity": r.quantity,
            "start_date": r.start_date,
            "end_date": r.end_date,
            "buyer_name": r.buyer_name,
            "buyer_department": r.buyer_department,
            "buyer_ministry": r.buyer_ministry,
            "buyer_state": r.buyer_state,
        }
        if r.financial_sellers:
            for s in r.financial_sellers:
                rows.append({**base, **{f"seller_{k}": v for k, v in s.items()}})
        else:
            rows.append({**base, "seller_note": r.ra_note or "No financial data"})

    if not rows:
        return
    with open(path, "w", newline="", encoding="utf-8") as f:
        writer = csv.DictWriter(f, fieldnames=rows[0].keys())
        writer.writeheader()
        writer.writerows(rows)
    print(f"[+] Results CSV saved to {path}")


# ── CLI ───────────────────────────────────────────────────────────────────────

def main():
    parser = argparse.ArgumentParser(description="GEM Tender Results Scraper")
    parser.add_argument("--pages", type=int, default=1, help="Listing pages to fetch (10 bids/page)")
    parser.add_argument("--bid-id", type=int, nargs="+", help="Specific bid ID(s) to fetch results for")
    parser.add_argument("--fetch-results", action="store_true", help="Also fetch detailed results for each listing")
    parser.add_argument("--status", default="bidrastatus", help="Bid status filter (default: bidrastatus)")
    parser.add_argument("--sort", default="Bid-End-Date-Latest", help="Sort order")
    parser.add_argument("--search", default="", help="Search keyword")
    parser.add_argument("--date-from", default="", help="Filter from date (DD-MM-YYYY)")
    parser.add_argument("--date-to", default="", help="Filter to date (DD-MM-YYYY)")
    parser.add_argument("--delay", type=float, default=1.5, help="Delay between requests (seconds)")
    parser.add_argument("--output", default="gem_output", help="Output file prefix")
    parser.add_argument("--headed", action="store_true", help="Run with visible browser")
    args = parser.parse_args()

    scraper = GEMScraper(headed=args.headed, delay=args.delay)
    try:
        scraper.start()
        scraper.init_session()

        if args.bid_id:
            results = scraper.scrape_results(args.bid_id)
            export_results_json(results, f"{args.output}_results.json")
            export_results_csv(results, f"{args.output}_results.csv")
            return

        listings = scraper.scrape_listings(
            num_pages=args.pages,
            search=args.search,
            status_type=args.status,
            sort=args.sort,
            date_from=args.date_from,
            date_to=args.date_to,
        )
        export_listings_csv(listings, f"{args.output}_listings.csv")

        if args.fetch_results and listings:
            bid_ids = [l.bid_id for l in listings]
            results = scraper.scrape_results(bid_ids)
            export_results_json(results, f"{args.output}_results.json")
            export_results_csv(results, f"{args.output}_results.csv")

        print(f"\n[+] Done. Scraped {len(listings)} listings.")
    finally:
        scraper.stop()


if __name__ == "__main__":
    main()
