# TokenSaver functions

**Version 2.6.0**

Windows PC helper that Claude hands work to, so the steps run on the PC instead of costing tokens in the chat. Runs as an MCP server in Claude desktop. Everything it keeps is in `D:\Claude Apps\TokenSaver`.

## What it can do

- **scan** â€” folder overview: file count, size, types, dates, largest files
- **find** â€” find files by name, size, date or text inside (text, code, Office, PDF) across many folders; shows up to 3 matching lines per file (lines) and how many more
- **read** â€” read just part of a file: head, tail, lines, grep, plain text of Office/HTML/PDF, OCR of scans and pictures, chosen PDF pages, a small copy of a picture or PDF page, JSON values; changes-only re-reads; hash (sha256/sha1/md5, verify against an expected value)
- **run_job** â€” background file jobs: copy, move, sync, delete (Recycle Bin only), rename, mkdir, download, extract, edit, write; filters and dry-run
- **job** â€” status, list, cancel, undo, notify when a job ends
- **saved** â€” saved jobs, run by Claude or by double-click
- **local_ai** â€” local model asks, summarizes or sorts files, PDFs and pictures so they stay out of the chat; starts and stops on its own; warns first if the GPU is busy
- **run** â€” runs commands in the background and returns only the useful lines (or the local AI's summary of a long output); admin commands without UAC prompts, destructive ones need a yes
- **system** â€” PC info (drives, GPU, CUDA, installed tools), processes, ports, kill
- **web** â€” search the web and read pages and PDFs on the PC; local AI can answer from pages
- **browser** â€” real Chrome with its own TokenSaver profile (sign in once, stays signed in): open, text, find, links, click, fill, press, wait; text only, for JavaScript and signed-in pages
- **media** â€” audio/video as text: info, transcript (local Whisper on the GPU, cached), frames (stills + their OCR text)
- **office** â€” Word, Excel, PowerPoint, PDF: create (from Markdown or rows), edit (replace text, add/delete paragraphs or slides, set cells and formulas, add rows and sheets; PDF merge, pick/reorder pages, rotate, stamp page numbers or watermark, split), read (outline, cells with formulas, PDF pages; old .doc/.xls/.ppt too), convert through installed Office (e.g. to PDF); old copy kept on every edit
- **rag** â€” search your files by meaning (C: and D:, documents and code; index updated only when asked) and keep memory notes Claude can recall in later chats; uses the CPU by itself while ComfyUI or training holds the GPU
- **phone** â€” your Android phone through the AdbSaver engine on it (same tools: find, read, write, scan, run, ui, screen, apps, notifications, system, web, local_ai, saved) over the LAN, Tailscale from anywhere, or plain adb when the engine is down; status, pair, config, adb, tunnel, public, push_routes
- **serve** / **install-link** â€” the phone reaches this PC's tools the same way: a token-protected HTTP front (port 8766) run by the "TokenSaver Link" task, reachable over LAN, Tailscale, a forwarded public port (`phone public setup`) or a cloudflared tunnel (`phone tunnel start`, works behind CGNAT, no Tailscale needed); the phone gets the current routes at every report
- **peer** â€” your other computers running TokenSaver over Tailscale: call any of their tools from here (pairs on its own), status, pair; only allowlisted devices signed in as the tailnet's owner can pair (POST /pair, checked with Tailscale whois)
- **git** â€” Git in one call, never as admin, GitHub sign-in straight to the browser: status, log, diff, commit, push, pull, clone (updates an existing clone instead of failing), compare (two folders or repo versions file by file: only on one side, changed lines, same; pairs x_windows.go with x_linux.go), release (bumps the version, adds the notes to this file, commits and pushes), handoff (notes for another project from the commits and diff, summarized by the local AI)
- **outline** â€” functions and types with line numbers for a code file or project (universal-ctags, regex fallback), so only the needed lines get read
- **check** â€” lint and type-check code (Ruff + mypy for Python, ESLint + tsc for JS/TS, go vet + staticcheck for Go, Android lint for Gradle projects; skips gitignored files) on the PC, on the phone over ADB, or in a GitHub repo; safe autofixes with fix; build: compile + run the project's tests and return only the failing lines; review: a deep code review made for AI-written code (security, secrets, vulnerable or made-up packages, CI workflows, scripts, Kotlin, complexity, build + tests, unfinished or fake work) of the latest changes, with a BLOCK/FIX/PASS verdict
- **window** â€” read on-screen text without screenshots: list windows, a window's text (UI Automation, works on Chrome/Electron/Steam), OCR of a window or area, copy-all via clipboard (restored), do: click buttons and fill fields by name, click on OCR text, click/move/drag at a position, scroll, hold keys, press keys, several steps per call (never in Claude's own windows); OCR with positions; clipboard get/set; small screenshot of a window or area, or a local AI answer about it instead; long text saved to a file, optional local AI summary; wait for a window or text to show; changes-only re-reads
- **Terminal** (double-click TokenSaver) â€” record or build macros, pick screen spots and colors, set your own triggers (hotkey, or a color showing/changing in a screen area â†’ click or macro), run saved jobs and any tool; Ctrl+Alt+Esc stops

**Local mode (what ContextSaver was):** the same program named `contextsaver` (or `TOKENSAVER_MODE=local`, `mcp --local`, `mcp --for <client>`) serves local models in AnythingLLM, LM Studio or Cline with one small `pc` tool (short descriptions, `help{c}` for a command's options, capped results paged with `more`) so the model's context stays free; risky calls wait for an approval (in RemoteAI or in the chat); model switching (`start`, `stop`, `model`), approvals, a console chat, `new` project scaffolds, compile for other targets, tests, plan, deps, export, phone app installs. It keeps ContextSaver's folder (`D:\Claude Apps\ContextSaver`) and shares TokenSaver's search index and notes and its one admin helper.

Also: GitHub repos as paths (owner/name[@ref][/sub] in find, read, scan, check, outline and git) read from a temporary copy TokenSaver keeps on the PC, updated as needed and removed after a day unused, so Claude never clones repos itself; tells Claude desktop to prefer TokenSaver over its built-in tools, undo for file jobs, job-end signals through GitHub (no polling), protected system folders; the terminal also reconnects Claude desktop and installs or removes the admin helper.

## What's new
### 2.6.0
- Project mode (`tokensaver project <folder>|off|status`, or window action:project): a chat app such as Perplexity codes in that one folder without asking first (edits, deletes, commands, builds, tests, commits, push); the server instructions say so and auto-yes runs with it, also answering delete/overwrite questions that name nothing outside the folder; force-push, reset --hard, uninstall, format and registry questions still wait for the user; refuses a whole drive or the home folder; off by itself after 8 hours
### 2.5.0
- check action:build_app: review, lint, build, emulator test and (push:true) git push in one call; stops at the first failing step with the exact error and lists every step as DONE or NOT DONE
- Tool labels for chat apps: read-only on scan, find, read, outline, web; non-destructive on check, git, media, chats, local_ai; tools that can delete stay unlabelled
- Auto-yes (`tokensaver autoyes on|off|status`, or window action:auto_yes): answers yes in the Perplexity window when its newest message only asks to confirm; off by default, off by itself after 2 hours or when the window closes, never for deleting or overwriting, every reply logged
- From the boot service, build_app and auto-yes run as the signed-in user, so the emulator, the Gradle cache and the GitHub sign-in work through the connector too
### 2.4.0
- Works better with other chat apps (Perplexity and the like): paths and run's cwd accept ~, $HOME, %USERPROFILE% and $env:USERPROFILE (the signed-in user's folder, also when TokenSaver runs as the boot service); a relative path is refused with an example
- run and the server instructions say it plainly: create or edit files with run_job write/edit steps, one per file, never inside a command
- scan, find, read, outline and web are labelled read-only (MCP readOnlyHint), so a chat app can tell them from the tools that write
- check recognises an Android project that has no Gradle wrapper; check action:build adds the wrapper, goes online once when the first build needs downloads, and shows where the APK is
- check action:new starts a project (android, go, web, python, rust, c), the same starter local mode has; Android comes with the Gradle wrapper
- phone pair: works with the release app without copying a token (MobileTokenSaver 0.2.1+), picks the right phone when several Android devices are on Tailscale, and no longer says paired when the phone rejects the token
- phone: LAN and Tailscale are tried together, so a dead address no longer hides one that answers; push_routes reports what the phone answered
### 2.3.0
- Permanent connector address: `tokensaver connector permanent` (or phone tunnel mode funnel) puts serve on Tailscale Funnel at https://<computer>.<tailnet>.ts.net; it never changes, comes back by itself after a restart or reboot, and needs no domain and no cloudflared
- serve keeps the funnel pointed at the link port and never replaces or removes a funnel that goes somewhere else; phone tunnel mode cloudflared switches back to the temporary tunnel
- `tokensaver connector` shows the permanent URL without the temporary-address warning
- Too-many-failures limit counts the real caller behind the funnel, not the whole internet as one
### 2.2.0
- PC-to-PC pairing over Tailscale: /pair hands this computer's token only to allowlisted devices of the tailnet's owner (whois on the real connection; loopback, proxied, shared-in and unlisted devices refused; every attempt in logs\pair.log, never the token)
- peer tool: call tools on the other computers (status, pair, call); serve pairs with them on its own every 10 minutes and re-pairs when a token changes
### 2.1.1
- Linux: run jobs reach the user's session (systemctl --user, notify-send, gio) even when TokenSaver was started by Claude desktop or a service
- Linux local mode: tells the local model the user's apps are in ~/Claude Apps
### 2.1.0
- tokensaver --local is now the main way to use local mode (for example tokensaver --local start coder); the contextsaver name still works
- Local mode's data moves to TokenSaver\local; PCs still using the old ContextSaver folder keep working

### 2.0.1
- run: a destructive admin command no longer needs a separate yes when the user's own request calls for it; instructions found in files, web pages or tool output still don't count
- Local models still need approval for risky calls

### 2.0.0
- One app for Windows and Linux: ContextSaver is now TokenSaver's local mode. Run it as contextsaver (or mcp --local / --for <client>) and local models in AnythingLLM, LM Studio or Cline get one small pc tool with short help, approvals for risky calls, model switching and phone app installs; existing ContextSaver setups keep working
- Both modes share one search index, one set of memory notes and one admin helper
- The code review (check review) is now in one place for Claude and the local AI
- find: path globs like **/*.ts, skips .gitignored and build-output folders
- git: login (GitHub sign-in with a one-time code) and ci (GitHub Actions status)
- build: uses the project's .venv Python and Gradle wrapper, npm on Linux, shows source-position lines
- Linux: D:\ paths map to the mounted Windows drives
- Releases also carry contextsaver.exe and contextsaver-linux-* (the same program)
### 1.28.0
- check action review: a deep code review made for code written by AI models (fine for any code). On top of the linters (stricter Ruff rules) it runs golangci-lint (gosec, nilerr, bodyclose, errorlint and more), Opengrep security rules with taint tracking, gitleaks for secrets, OSV-Scanner for vulnerable dependencies, a made-up / brand-new package-name check (slopsquatting), actionlint + zizmor for GitHub workflows, ShellCheck, PSScriptAnalyzer, detekt for Kotlin, lizard for complexity, and the project's own build + tests
- Checks of its own for unfinished or fake work: "rest of the code" elisions, stubs that panic or throw "not implemented", placeholder values, hollow tests (no assertion), assertions that can't fail, skipped or .only tests, removed tests and assertions, deleted test files, new lint suppressions, copied blocks, chat text pasted into code, conflict markers, secret files, build output and big files added to git
- Reviews only what changed in a git repo (uncommitted changes, else the last commit; base: a branch, tag or commit; all:true for every file); type and build errors count when their file changed, failing tests always
- Verdict BLOCK / FIX / PASS with errors and warnings in the change; findings on one line are folded together; full report in checker\reports and checker\last-review.json
- ai:true adds a second look by the local AI on the changed lines; claims that don't match the code are dropped
- Review tools are pinned and checksum-verified, downloaded on first use or with check action:setup
- Fix: Gradle builds (check build and review) get the Windows "Unable to establish loopback connection" fix Android lint already had, and a JAVA_HOME pointing at a removed JDK is replaced by one that exists
- Fix: Android lint findings are reported again when lint fails the build on a second run (its up-to-date reports were taken for stale ones)
### 1.27.2
- phone now talks to MobileTokenSaver (AdbSaver was merged into it); an existing AdbSaver setup switches over by itself, then run phone pair once
- phone pair accepts token (copied from the app's Engine section) when it can't read it over adb
### 1.27.1
- GPU check is now only a reminder: when ComfyUI, AI Toolkit training or heavy VRAM use has the GPU, local_ai, web ask, window screenshot questions and media transcripts say what is running and let you choose: gpu:"stop" shuts those programs down and goes ahead, gpu:"continue" goes ahead anyway (force:true still means continue)
- Same reminder on Windows and Linux, including transcripts
### 1.27.0
- One repo for every platform: the Linux version (until now the separate TokenSaver-Linux repo) is merged in, so each release builds tokensaver.exe (Windows), tokensaver-linux-amd64 and tokensaver-linux-arm64 from the same code
- Linux gets all Windows features it was missing: local_ai, web ask, run/window summaries, screenshot questions, GitHub repos as paths, git compare, outline, claude.ai custom connector
- Platform-only parts live in *_windows.go / *_linux.go files; tool descriptions name bash, Trash and ~/Claude Chats on Linux
- Fix: garbled arrows and symbols (→, —, °, ·) in some messages
### 1.26.0
- claude.ai custom connector: `tokensaver serve` also answers at the secret path /c/<connector_token>/mcp, so chats on the phone (no desktop app link) can use the PC's TokenSaver tools; the path is the login, because claude.ai cannot send headers
- `tokensaver connector` prints the connector URL (warns when it is a temporary trycloudflare one); `tokensaver connector permanent` switches to an address that never changes (Tailscale Funnel, https://<computer>.<tailnet>.ts.net, kept across restarts and reboots); `tokensaver connector rotate` makes a new secret; also in the console (connector)
- connector_token in link.json is separate from the phone token; serve logs only tool names, never the token or URL
- MCP over HTTP: notifications answer 202, GET answers 405 + Allow: POST; the bearer /mcp route is unchanged
### 1.25.1
- Fix: git clone now recognises a folder that is already that clone even when its path uses Windows short names (like RUNNER~1), instead of saying it is not a clone
### 1.25.0
- GitHub repos as paths: owner/name[@ref][/sub] in find, read, scan, check, outline and git reads from a temporary copy TokenSaver keeps in data\repos (cloned with your GitHub sign-in, updated after 5 min, removed after a day unused); results show owner/name paths, so Claude never clones repos itself
- git clone updates a folder that already holds that clone (pull --ff-only) instead of failing; with no folder it gives the temporary copy; returns path, branch and last commit
- New git compare: two folders or repo versions file by file: files only on one side, changed files by changed lines, how many are the same; pair:true matches x_windows.go with x_linux.go/x.go; paths + full:true shows those files' changed lines
- New outline tool: functions and types with line numbers (universal-ctags, regex fallback)
- Commit, push and release are refused on the temporary copies
### 1.24.0
- New chats tool: saves every claude.ai chat (all projects + regular chats) to D:\Claude Chats as Markdown, one file per chat, through TokenSaver's signed-in browser; the session cookie never leaves the browser and chat text never passes through Claude
- Only changed chats are re-fetched (index.json keeps id, path, updated_at); renamed chats move to their new file name
- After each sync the folder is re-indexed in RAG (added as a root once); chats search queries it
- Sign-in or Cloudflare walls stop the sync with 'needs sign-in' instead of being worked around
- Scheduled task TokenSaver\Chats Sync runs hidden every 6 hours; Sync Claude chats.cmd in the app folder runs it by hand
### 1.23.0
- Phone link (tokensaver serve) now runs as the Windows service TokenSaverLink: starts at boot as LocalSystem, before anyone signs in; install-link sets it up through the elevated helper and removes the old sign-in task
- Only one cloudflared: it lives in a kill-on-close job object, leftovers for port 8766 are killed, serve holds a single-instance lock, restarts stop the old process first
- Tunnel starts only once port 8766 answers; serve logs bind failures to data\logs\serve.log and keeps retrying
- Firewall rules for tokensaver.exe and cloudflared.exe are pre-created (duplicates removed), so no Windows prompts
- Updates: the service notices the replaced program and restarts itself on it, no orphans, no clicks
- phone tunnel status shows service state, cloudflared count, port 8766, tunnel URL and firewall rules
- New tunnel URLs are re-sent to the phone until it has them (works after a reboot with nobody signed in)
- Fixed: port 8766 looked 'not listening' because Windows reports serve's socket with an odd state that netstat hides
### 1.22.0
- Local AI server is now shared: a server that was already running (e.g. remoteai's) is marked external and never stopped by TokenSaver
- No more instant shutdown: a server TokenSaver started stays up until idle for ai_idle_minutes (default 15); any request from any app resets the timer
- Never stops a busy server: checks llama.cpp /slots (and /metrics) before any stop
- keep_running pin (local_ai models keep_running:true) blocks all auto-stops; models shows running, owner, pinned, busy, idle minutes left
- Safe manual stop: local_ai stop / job cancel id local_ai refuse while busy unless confirmed:true
- Tests for external, busy, idle-timeout and pin cases run on every push
### 1.21.0
- Phone adb now goes to <tailscale-ip>:5555 first (AdbSaver's fixed TCP port); LAN wireless debugging/mDNS is only a fallback
- serve keeps that adb link connected every 30 s (drops offline entries, reconnects)
- All adb calls (phone adb, adb fallback, check on the phone, pair) use that one -s serial, so stale mDNS duplicates are never picked
- phone status shows the adb route; adb is also found when it isn't on PATH (winget install)
### 1.20.1
- data folder gets its own go.mod, so go test ./... no longer trips over job backup copies
### 1.20.0
- rag config keeps config.json.bak + last 10 dated copies (data\rag\config-backups)
- rag update refuses to drop over half the index without confirm:true; dry run warns and names changed folders
- rag config refuses temp-folder roots for the real index, or replacing every root at once, without confirm:true

### 1.19.1
- Fix: system restart_claude failed with a WMI type mismatch (ShowWindow must be a uint16)
### 1.19.0
- **git** tool: status, log, diff, commit, push, pull, clone, release, handoff; always in the user session (no admin), GitHub sign-in goes straight to the browser
- **git release**: bumps the version (main.go appVersion, package.json, pyproject.toml, build.gradle), adds the notes under What's new in FUNCTIONS.md, commits "version: message" and pushes
- **git handoff**: handoff notes for another project (ContextSaver, APIChat) from the commits and diff since a ref or the last tag, written by the local AI
- **check** action build: go build -trimpath + go test, npm run build + npm test, gradlew assembleDebug + unit tests, python compileall + pytest; only failing lines come back, full log in cache\build
- **run** summary:true: the local AI summarizes a long finished output instead of the tail
- **read** mode hash: sha256/sha1/md5 of a file, pattern = expected hash -> MATCH/MISMATCH
### 1.18.5
- Fix: the system tool definition was broken JSON in 1.18.4, so Claude desktop timed out on tools/list and TokenSaver never connected; a test now checks every tool definition

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
- **git** â€” Git in one call, never as admin, GitHub sign-in straight to the browser: status, log, diff, commit, push, pull, clone, release (bumps the version, adds the notes to this file, commits and pushes), handoff (notes for another project from the commits and diff, summarized by the local AI)
- **check** â€” lint and type-check code (Ruff + mypy for Python, ESLint + tsc for JS/TS, go vet + staticcheck for Go, Android lint for Gradle projects; skips gitignored files) on the PC, on the phone over ADB, or in a GitHub repo; safe autofixes with fix; build: compile + run the project's tests and return only the failing lines; review: a deep code review made for AI-written code (security, secrets, vulnerable or made-up packages, CI workflows, scripts, Kotlin, complexity, build + tests, unfinished or fake work) of the latest changes, with a BLOCK/FIX/PASS verdict
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
