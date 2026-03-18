# UI Redesign Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Redesign all three pages (index, results, tender detail) as a modern flat SaaS-style dashboard portal using Tailwind CSS via CDN.

**Architecture:** Pure frontend change — rewrite 3 Go templates and 1 CSS file. No Go code changes. Tailwind via CDN replaces most custom CSS. HTMX behavior preserved exactly.

**Tech Stack:** Tailwind CSS (CDN), HTMX (existing), Go html/template (existing)

---

### Task 1: Rewrite index.tmpl with Tailwind + persistent nav search

**Files:**
- Modify: `web/templates/index.tmpl`

**Step 1: Rewrite index.tmpl**

Replace the entire file with a new layout:
- Add Tailwind CDN `<script src="https://cdn.tailwindcss.com"></script>`
- Fixed top nav: `bg-slate-900` full-width bar
  - Left: "GEM Tender Discovery" in `text-white font-semibold text-lg`
  - Center: search `<input>` with `bg-slate-800 text-white placeholder-slate-400 rounded-full px-5 py-2.5 w-full max-w-2xl` and search icon (inline SVG)
  - Keep all existing HTMX attributes on the input (`hx-get`, `hx-trigger`, `hx-target`, `hx-indicator`)
- Body: `bg-slate-50 min-h-screen`
- Main: `max-w-7xl mx-auto px-6 py-8 pt-24` (pt-24 to offset fixed nav)
- Results div: `<div id="results">` with same HTMX load trigger
- Spinner: styled with `text-slate-400 text-sm`

**Step 2: Verify**

Run: `go build -tags fts5 && ./gemtenders serve`
Open browser to localhost, confirm:
- Nav bar renders dark with centered search
- Search triggers results load
- HTMX still works (type → results update)

**Step 3: Commit**

```bash
git add web/templates/index.tmpl
git commit -m "feat(ui): redesign index page with Tailwind dashboard layout"
```

---

### Task 2: Rewrite results.tmpl with modern card design

**Files:**
- Modify: `web/templates/results.tmpl`

**Step 1: Rewrite results.tmpl**

Replace with Tailwind-styled markup. This is a partial (no `<html>` wrapper — loaded by HTMX into `#results`):

- Error state: `bg-red-50 text-red-700 rounded-xl p-4`
- Results header: `text-sm text-slate-500 mb-4 pb-3 border-b border-slate-200`
- Each result card `<a>`:
  - Outer: `block bg-white rounded-xl p-5 mb-3 border border-slate-200 hover:shadow-md hover:border-slate-300 transition-all duration-150 no-underline text-inherit cursor-pointer`
  - Left border accent: `border-l-4 border-l-amber-400` if high-value, else `border-l-4 border-l-slate-300`
  - Card header row (flex, space-between):
    - Left: Bid number `font-semibold text-slate-900`, parent bid `text-blue-600` with arrow
    - Right: Badges — High Value (`bg-amber-100 text-amber-800`), C (`bg-orange-500 text-white`), R (`bg-purple-500 text-white`), all `text-xs font-semibold px-2.5 py-1 rounded-full`
  - Card body (grid, 3 columns on desktop, 1 on mobile):
    - Col 1: Category label + value, Quantity label + value
    - Col 2: "Department" label, ministry name `text-blue-600 font-medium`, dept name `text-slate-500`
    - Col 3: Start date `text-emerald-600`, End date `text-red-500`
  - Labels: `text-xs text-slate-400 uppercase tracking-wide`
  - Values: `text-sm text-slate-700`
- No results: `text-center py-12 text-slate-400`
- Pagination: `flex items-center justify-center gap-3 mt-6 pt-4`
  - Buttons: `px-4 py-2 bg-white border border-slate-200 rounded-full text-sm hover:bg-slate-900 hover:text-white hover:border-slate-900 transition-colors`
  - Page indicator: `text-sm text-slate-500`

**Step 2: Verify**

Reload browser, confirm:
- Cards render with left accent borders
- Badges appear correctly
- Grid layout works (3-col desktop, stacks on mobile)
- Hover states work
- Pagination styled as pills

**Step 3: Commit**

```bash
git add web/templates/results.tmpl
git commit -m "feat(ui): redesign result cards with Tailwind dashboard style"
```

---

### Task 3: Rewrite tender.tmpl with detail page layout

**Files:**
- Modify: `web/templates/tender.tmpl`

**Step 1: Rewrite tender.tmpl**

Full page template with Tailwind CDN (same as index.tmpl):
- Same nav bar as index (but search bar links back to `/` on submit, or just logo links home)
- Back link: `text-sm text-slate-500 hover:text-slate-900 transition-colors` with left arrow
- **Header card** (`bg-white rounded-xl p-6 border border-slate-200 shadow-sm mb-6`):
  - Top row: bid number `text-xl font-bold text-slate-900` + high-value badge if applicable
  - Metadata grid `grid grid-cols-2 md:grid-cols-3 gap-6 mt-5`:
    - Each item: label `text-xs uppercase tracking-wider text-slate-400 font-semibold` + value `text-sm text-slate-800 mt-1`
    - Start date value in `text-emerald-600`, end date in `text-red-500`
- **Corrigendum card** (if OtherDetails exists, `bg-white rounded-xl border border-slate-200 shadow-sm mb-6`):
  - Tab bar inside card top: `border-b border-slate-200 px-6 pt-4`
    - Tab buttons: `pb-3 px-1 mr-6 text-sm font-medium border-b-2` — active: `border-blue-600 text-blue-600`, inactive: `border-transparent text-slate-500 hover:text-slate-700`
    - Corrigendum count badge: `bg-orange-500 text-white text-xs px-2 py-0.5 rounded-full ml-2`
  - Tab content area: `p-6`
  - Corrigendum docs list: each as a row with `flex items-center gap-4 py-3 border-b border-slate-100`
    - Download link: `text-blue-600 hover:text-blue-800 font-medium text-sm`
    - Date: `text-xs text-slate-400`
    - Pending: `text-slate-400 italic text-sm`
  - Raw HTML content rendered below docs
- **PDF card** (`bg-white rounded-xl border border-slate-200 shadow-sm`):
  - Header: `text-lg font-semibold text-slate-800 p-6 border-b border-slate-100`
  - Iframe: `w-full border-0 rounded-b-xl` height 800px
- Keep existing `showTab()` JS function, just update to match new element structure

**Step 2: Verify**

Navigate to a tender detail page, confirm:
- Nav bar consistent with index
- Metadata grid renders cleanly
- Corrigendum tabs work (click switches content)
- PDF iframe loads
- Back link works

**Step 3: Commit**

```bash
git add web/templates/tender.tmpl
git commit -m "feat(ui): redesign tender detail page with Tailwind dashboard style"
```

---

### Task 4: Clean up style.css

**Files:**
- Modify: `web/static/style.css`

**Step 1: Reduce style.css to minimal overrides**

Since Tailwind handles all styling, reduce `style.css` to only:
- HTMX indicator visibility rules (`.htmx-indicator`, `.htmx-request .htmx-indicator`, `.htmx-request.htmx-indicator`)
- Any styles needed for raw HTML injected from `corrigendum_html` / `representation_html` (these are stored HTML blobs from the GEM API and can't be Tailwind-ified)

Remove all other CSS rules since Tailwind utility classes replace them.

**Step 2: Verify**

Full smoke test:
- Search page loads, type a query, results appear
- Click a result, detail page loads
- Corrigendum tabs work
- PDF loads
- Mobile responsive (resize browser)

**Step 3: Commit**

```bash
git add web/static/style.css
git commit -m "refactor(ui): reduce style.css to HTMX + raw HTML overrides"
```

---

### Task 5: Final visual polish pass

**Step 1: Review all three pages**

Open each page and check:
- Consistent spacing and typography
- Badges render correctly at all sizes
- No layout overflow on mobile
- Hover states feel smooth
- Corrigendum raw HTML doesn't break the card layout (may need `prose` class or overflow containment)

**Step 2: Fix any issues found**

Apply targeted fixes to templates.

**Step 3: Commit**

```bash
git add web/
git commit -m "fix(ui): visual polish and consistency fixes"
```
