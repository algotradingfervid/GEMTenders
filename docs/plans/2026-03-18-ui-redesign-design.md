# UI Redesign: Dashboard Portal

## Overview
Redesign all three pages (search home, results, tender detail) as a modern SaaS-style client-facing portal using Tailwind CSS via CDN. Flat design, no gradients. Single binary deployment preserved.

## Tech Stack
- Tailwind CSS via CDN `<script>` tag (no build step)
- HTMX (existing, kept)
- Go/Gin templates (existing, kept)

## Color Palette
- Primary: slate-900 (`#0f172a`) — nav bar, headings
- Accent: amber — high-value tender indicators
- Background: slate-50 (`#f8fafc`)
- Cards: white with `border-slate-200`, `shadow-sm`, `rounded-xl`
- Dates: green (start), red (end)
- Ministry text: blue tint

## Layout & Navigation
- Fixed top nav bar (`bg-slate-900`, full-width)
  - Left: "GEM Tender Discovery" logo text
  - Center: persistent search bar (~60% width, rounded, search icon)
  - Right: placeholder space
- Main content: max-width 1280px, centered, `px-6 py-8`
- Page background: `bg-slate-50`

## Search Home Page
- Search bar in nav IS the search — no separate landing
- Results load immediately below (all tenders, paginated)

## Results Page
- Results header: "Showing X - Y of Z tenders" (`text-slate-500 text-sm`)
- Result cards: horizontal rows with zones:
  - Left accent border: amber (high-value) or slate (normal)
  - Bid info: bid number bold navy, parent bid below
  - Category: item name + quantity
  - Department: ministry (blue) + department stacked
  - Dates: start (green) / end (red) stacked
  - Badges: right-aligned pills (High Value, C, R)
- Hover: `shadow-md` + border color change
- Pagination: centered pill buttons

## Tender Detail Page
- Back link: "Back to search" top-left
- Header card: bid number `text-xl font-bold`, high-value badge, 2x3 metadata grid
- Corrigendum card: integrated tab bar, document download rows
- PDF card: "Bid Document" header, full-width iframe 800px

## Constraints
- No gradients
- Single binary deployment (CDN-only external deps)
- Responsive (mobile-friendly)
