# TokenSaver functions

**Version 1.14.0**

Windows PC helper that Claude hands work to, so the steps run on the PC instead of costing tokens in the chat. Runs as an MCP server in Claude desktop. Everything it keeps is in `D:\Claude Apps\TokenSaver`.

## What it can do

- **scan** — folder overview: file count, size, types, dates, largest files
- **find** — find files by name, size, date or text inside (text, code, Office, PDF) across many folders
- **read** — read just part of a file: head, tail, lines, grep, plain text of Office/HTML/PDF, OCR of scans and pictures, chosen PDF pages, a small copy of a picture or PDF page, JSON values; changes-only re-reads
- **run_job** — background file jobs: copy, move, sync, delete (Recycle Bin only), rename, mkdir, download, extract, edit, write; filters and dry-run
- **job** — status, list, cancel, undo, notify when a job ends
- **saved** — saved jobs, run by Claude or by double-click
- **local_ai** — local model asks, summarizes or sorts files, PDFs and pictures so they stay out of the chat; starts and stops on its own; warns first if the GPU is busy
- **run** — runs commands in the background and returns only the useful lines; admin commands without UAC prompts, destructive ones need a yes
- **system** — PC info (drives, GPU, CUDA, installed tools), processes, ports, kill
- **web** — search the web and read pages and PDFs on the PC; local AI can answer from pages
- **browser** — real Chrome with its own TokenSaver profile (sign in once, stays signed in): open, text, find, links, click, fill, press, wait; text only, for JavaScript and signed-in pages
- **media** — audio/video as text: info, transcript (local Whisper on the GPU, cached), frames (stills + their OCR text)
- **office** — Word, Excel, PowerPoint, PDF: create (from Markdown or rows), edit (replace text, add/delete paragraphs or slides, set cells and formulas, add rows and sheets; PDF merge, pick/reorder pages, rotate, stamp page numbers or watermark, split), read (outline, cells with formulas, PDF pages; old .doc/.xls/.ppt too), convert through installed Office (e.g. to PDF); old copy kept on every edit
- **rag** — search your files by meaning (C: and D:, documents and code; index updated only when asked) and keep memory notes Claude can recall in later chats; uses the CPU by itself while ComfyUI or training holds the GPU
- **window** — read on-screen text without screenshots: list windows, a window's text (UI Automation, works on Chrome/Electron/Steam), OCR of a window or area, copy-all via clipboard (restored), do: click buttons and fill fields by name, press keys, several steps per call (never in Claude's own windows); long text saved to a file, optional local AI summary; wait for a window or text to show; changes-only re-reads
- **Terminal** (double-click TokenSaver) — record or build macros, pick screen spots and colors, set your own triggers (hotkey, or a color showing/changing in a screen area → click or macro), run saved jobs and any tool; Ctrl+Alt+Esc stops

Also: tells Claude desktop to prefer TokenSaver over its built-in tools, undo for file jobs, job-end signals through GitHub (no polling), protected system folders, console menu for saved jobs and setup.

## What's new
### 1.14.0
- **rag** tool: meaning search over files, memory notes
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
- Triggers: hotkey or screen color → click or macro
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
