# TokenSaver functions

**Version 1.18.4**

Windows PC helper that Claude hands work to, so the steps run on the PC instead of costing tokens in the chat. Runs as an MCP server in Claude desktop. Everything it keeps is in `D:\Claude Apps\TokenSaver`.

## What it can do

- **scan** â€” folder overview: file count, size, types, dates, largest files
- **find** â€” find files by name, size, date or text inside (text, code, Office, PDF) across many folders; shows up to 3 matching lines per file (lines) and how many more
- **read** â€” read just part of a file: head, tail, lines, grep, plain text of Office/HTML/PDF, OCR of scans and pictures, chosen PDF pages, a small copy of a picture or PDF page, JSON values; changes-only re-reads
- **run_job** â€” background file jobs: copy, move, sync, delete (Recycle Bin only), rename, mkdir, download, extract, edit, write; filters and dry-run
- **job** â€” status, list, cancel, undo, notify when a job ends
- **saved** â€” saved jobs, run by Claude or by double-click
- **local_ai** â€” local model asks, summarizes or sorts files, PDFs and pictures so they stay out of the chat; starts and stops on its own; warns first if the GPU is busy
- **run** â€” runs commands in the background and returns only the useful lines; admin commands without UAC prompts, destructive ones need a yes
- **system** â€” PC info (drives, GPU, CUDA, installed tools), processes, ports, kill
- **web** â€” search the web and read pages and PDFs on the PC; local AI can answer from pages
- **browser** â€” real Chrome with its own TokenSaver profile (sign in once, stays signed in): open, text, find, links, click, fill, press, wait; text only, for JavaScript and signed-in pages
- **media** â€” audio/video as text: info, transcript (local Whisper on the GPU, cached), frames (stills + their OCR text)
- **office** â€” Word, Excel, PowerPoint, PDF: create (from Markdown or rows), edit (replace text, add/delete paragraphs or slides, set cells and formulas, add rows and sheets; PDF merge, pick/reorder pages, rotate, stamp page numbers or watermark, split), read (outline, cells with formulas, PDF pages; old .doc/.xls/.ppt too), convert through installed Office (e.g. to PDF); old copy kept on every edit
- **rag** â€” search your files by meaning (C: and D:, documents and code; index updated only when asked) and keep memory notes Claude can recall in later chats; uses the CPU by itself while ComfyUI or training holds the GPU
- **phone** â€” your Android phone through the AdbSaver engine on it (same tools: find, read, write, scan, run, ui, screen, apps, notifications, system, web, local_ai, saved) over the LAN, Tailscale from anywhere, or plain adb when the engine is down; status, pair, config, adb, tunnel, public, push_routes
- **serve** / **install-link** â€” the phone reaches this PC's tools the same way: a token-protected HTTP front (port 8766) run by the "TokenSaver Link" task, reachable over LAN, Tailscale, a forwarded public port (`phone public setup`) or a cloudflared tunnel (`phone tunnel start`, works behind CGNAT, no Tailscale needed); the phone gets the current routes at every report
- **check** â€” lint and type-check code (Ruff + mypy for Python, ESLint + tsc for JS/TS, go vet + staticcheck for Go, Android lint for Gradle projects; skips gitignored files) on the PC, on the phone over ADB, or in a GitHub repo; safe autofixes with fix
- **window** â€” read on-screen text without screenshots: list windows, a window's text (UI Automation, works on Chrome/Electron/Steam), OCR of a window or area, copy-all via clipboard (restored), do: click buttons and fill fields by name, click on OCR text, click/move/drag at a position, scroll, hold keys, press keys, several steps per call (never in Claude's own windows); OCR with positions; clipboard get/set; small screenshot of a window or area, or a local AI answer about it instead; long text saved to a file, optional local AI summary; wait for a window or text to show; changes-only re-reads
- **Terminal** (double-click TokenSaver) â€” record or build macros, pick screen spots and colors, set your own triggers (hotkey, or a color showing/changing in a screen area â†’ click or macro), run saved jobs and any tool; Ctrl+Alt+Esc stops

Also: tells Claude desktop to prefer TokenSaver over its built-in tools, undo for file jobs, job-end signals through GitHub (no polling), protected system folders; the terminal also reconnects Claude desktop and installs or removes the admin helper.

## What's new

### 1.18.4
- phone status re-reads phone.json on every call (last report and adb_serial no longer stale); the phone's report can carry AdbPort (its live Wireless-debugging port), which pair and the adb fallback try before mDNS
- After an update: the installed helper replaces an old one still holding the elevated pipe (the archived .old exe), so admin runs work straight away; the helper task restarts cleanly on each update
- An MCP session still running an older TokenSaver puts "TokenSaver updated to X.Y.Z — restart Claude desktop" in front of every tool result instead of silently running old code
- **system** restart_claude: quits and reopens Claude desktop so it loads the new version

### 1.18.2
- check: Android lint for Gradle projects (Kotlin/Java), skips gitignored files, go vet + staticcheck for Go modules

### 1.18.1
- Cleanup: all staticcheck findings fixed (dead code removed, lowercase error strings, http.Status* constants)

### 1.18.0
- **phone tunnel** start/stop/status: cloudflared tunnel supervised by the link server (restarts itself); the phone reaches the PC without Tailscale, even behind CGNAT; new URLs are pushed to the phone and returned in every report
- **phone public** setup/set: UPnP port forward + firewall rule for all profiles + CGNAT check; `public_host` (DDNS/IP) goes into the phone link
- **phone push_routes** and PC routes in **phone status**; `/phone/report` answers with the current routes
- Link server listens on all addresses; repeated bad tokens are rate-limited
- `tokensaver phone â€¦` also works from the command line

### 1.18.0
- **check**: skips gitignored files; Go modules checked with go vet + staticcheck

### 1.17.0
- **phone** tool and the **TokenSaver Link** server: PC and phone reach each other anywhere over Tailscale
- git/gh always run in your own session (the admin helper has no credential store)
- A helper still serving an old or archived exe is restarted automatically, no prompt
- Tests run on GitHub on every push

### 1.16.0
- Admin runs keep working right after an update (no restart needed); installing from an admin command restarts the helper too

### 1.15.0
- **window** do: click_text, click_at, move, drag, scroll, hold
- **window** ocr boxes:true (positions), clipboard get/set, screenshot (or question for local AI)
- **find**: count of hidden matching lines; lines option

### 1.14.0
- **rag** tool: meaning search over files, memory notes
- **phone** â€” your Android phone through the AdbSaver engine on it (same tools: find, read, write, scan, run, ui, screen, apps, notifications, system, web, local_ai, saved) over the LAN, Tailscale from anywhere, or plain adb when the engine is down; status, pair, config, adb
- **serve** / **install-link** â€” the phone reaches this PC's tools the same way: a token-protected HTTP front on the Tailscale address (port 8766) run by the "TokenSaver Link" task
- **check** â€” lint and type-check code (Ruff + mypy for Python, ESLint + tsc for JS/TS, go vet + staticcheck for Go, Android lint for Gradle projects; skips gitignored files) on the PC, on the phone over ADB, or in a GitHub repo; safe autofixes with fix
### 1.13.0
- **office** tool: create, edit, read, convert Word/Excel/PowerPoint/PDF
- **read**: Excel shows every sheet with cell refs and values; old .doc/.xls/.ppt readable

### 1.12.0
- **media** tool: info, transcript, frames

### 1.11.0
- **window** wait
- changes:true on **read**/**window**: only what changed
- Tool descriptions ~45% shorter (fewer tokens per message)
- **run** tail:0 = no extra lines

### 1.10.0
- **browser** tool (JS and signed-in pages as text)

### 1.9.0
- **window** do: click/type by name, multi-step

### 1.8.0
- **window** tool: list, text, ocr, copy

### 1.7.0
- Terminal replaces the menu
- Macros: record, build, play
- Triggers: hotkey or screen color â†’ click or macro
- Ctrl+Alt+Esc stop

### 1.6.0
- PDF text in **read**, **find**, **local_ai**, **web** (pages, page numbers in results)
- OCR for scanned PDFs and pictures (Windows' own, cached)
- **read** image: small copy of a picture or PDF page
- **local_ai** reads pictures (vision model or OCR)
- Claude desktop told to prefer TokenSaver

### 1.5.0
- **web** tool: search, find, read, links, ask
- Job-end notify through GitHub

### 1.4.0
- Everything kept under `D:\Claude Apps\TokenSaver`
- Elevated helper repairs itself if the install path changes

### 1.3.1
- Finds Claude desktop's config in the Store version

### 1.3.0
- Elevated helper: admin commands without UAC prompts

### 1.2.0
- **find** searches in parallel and skips cache folders
- Program Files readable
- **local_ai** checks if the GPU is busy before loading
- App renamed to TokenSaver

### 1.1.0
- **run** and **system** tools
- File ops: download, extract, edit, write

### 1.0.0
- **scan**, **find**, **read**, **run_job**, **job**, **saved**, **local_ai**
- File ops: copy, move, sync, delete, rename, mkdir
- Undo and saved jobs
