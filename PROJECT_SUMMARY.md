# GEMTenders - Project Summary

## What Is This Project?

GEMTenders is a **tender discovery and analytics platform** built for India's Government e-Marketplace (GeM). It automatically finds, collects, organizes, and presents government tender data so users can search, track, and analyze procurement opportunities in one place.

India's GeM portal hosts over 42,000 active tenders at any given time. Navigating that portal directly is slow and limited. GEMTenders solves this by pulling all that data into a fast, searchable system with rich analytics.

---

## Who Is It For?

- **Government procurement officers** who need to monitor the tender pipeline in their ministry or department
- **Vendors and contractors** looking for relevant tender opportunities to bid on
- **Bid analysts** tracking amendments, deadlines, and high-value opportunities
- **Anyone** who needs faster, deeper access to GeM tender data than the official portal provides

---

## What Does It Do?

The system handles the full lifecycle of tender data, from collection to presentation:

### 1. Data Collection (Scraping)

The system automatically visits the GeM portal and collects information about all active tenders. It does this using multiple parallel scrapers that work simultaneously to gather data quickly and reliably. The scraping process:

- Fetches all active bid listings from the GeM portal
- Extracts key details: bid numbers, categories, ministries, departments, dates, quantities, and more
- Detects whether tenders have been amended (corrigendums) and captures those changes
- Downloads the full PDF documents for each tender
- Runs on a schedule (every 6 hours) to keep data current

### 2. Data Storage

All collected tender data is stored in a local database. This includes:

- **Core bid information** - bid numbers, categories, ministry/department, start and end dates, quantities, high-value flags
- **Amendments** - any changes or extensions to tenders (corrigendums), including updated deadlines
- **Documents** - the actual PDF files for bids and their amendments
- **Search index** - a specially optimized index that enables fast full-text search across all tenders

### 3. Search & Browse

The main interface lets users find tenders quickly:

- **Real-time search** - type to search across bid numbers, categories, ministries, and departments with results appearing instantly
- **Advanced filters** - narrow results by department, category, or date range using intuitive dropdown selectors
- **Result cards** - each tender shows its key details at a glance, with badges indicating high-value bids or amendments
- **Tender detail pages** - click any tender to see its full information, amendment history, and embedded PDF viewer

### 4. Analytics Dashboard

A visual dashboard provides insights into the tender landscape:

- **Overview statistics** - total bids, PDFs downloaded, amendment counts, last scrape time
- **Tender pipeline** - charts showing active, expiring soon (24h/48h/7 days), and expired tenders
- **Top departments** - which government departments are issuing the most bids
- **Top categories** - which procurement categories are most active
- **30-day timeline** - a chart showing the daily flow of new tenders arriving over the past month
- **Scrape controls** - buttons to manually trigger data collection, PDF downloads, or amendment checks

### 5. Document Access

- Full bid PDFs are downloaded and stored locally for offline access
- Amendment (corrigendum) PDFs are also tracked and downloadable
- Documents are viewable directly in the browser via an embedded PDF viewer

---

## How It Works Day-to-Day

Once deployed, the system runs largely on autopilot:

1. **Every 6 hours**, the scraper automatically visits GeM and collects the latest tender data
2. **Shortly after**, PDF documents for any new tenders are downloaded
3. **Users visit the web interface** to search for tenders, review documents, and check the analytics dashboard
4. **The dashboard** shows live progress when a scrape is running, including how many tenders have been processed and any errors encountered

---

## Key Features at a Glance

| Feature | Description |
|---|---|
| **Automated collection** | Gathers 42,000+ tenders every 6 hours without manual intervention |
| **Fast search** | Full-text search with relevance ranking across all tender fields |
| **Smart filters** | Filter by department, category, and date ranges |
| **Amendment tracking** | Detects and records changes to tenders, including deadline extensions |
| **PDF library** | Downloads and serves all bid documents for offline access |
| **Visual analytics** | Charts and statistics for tender pipeline analysis |
| **Live progress** | Real-time status updates when data collection is running |
| **Single deployment** | Runs as a single application with minimal infrastructure needs |

---

## Deployment

The system is designed for simple deployment:

- Runs as a **single application** on a Linux server
- Uses a **lightweight local database** (SQLite) - no separate database server needed
- Stores PDFs locally (approximately 15-20 GB for a full collection)
- Can be placed behind a web server (Nginx) for public access
- Total footprint: roughly 20 GB including all documents
