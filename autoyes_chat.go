package main

// Auto-yes reads Perplexity's chat from its accessibility tree in document order instead of
// screen order. Screen order mixed three things into "the newest answer": the sidebar history
// (a separate "Perplexity Sidebar" document drawn on the same rows), the user's own query
// bubbles (right-aligned text right after [Copy query], including the "yes" it typed itself)
// and the input box. Here only the chat document counts, the query bubble is skipped and the
// answer runs from the newest query to its [Helpful]/[Fork] row.

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const ayChatScript = `param([long]$Hwnd)
$ErrorActionPreference = 'Stop'
$out = New-Object System.IO.StreamWriter([Console]::OpenStandardOutput(), (New-Object System.Text.UTF8Encoding $false))
try {
  Add-Type -AssemblyName UIAutomationClient, UIAutomationTypes
  $A = [System.Windows.Automation.AutomationElement]
  $CT = [System.Windows.Automation.ControlType]
  $P = [System.Windows.Automation.AutomationElement]::ControlTypeProperty
  $root = $A::FromHandle([IntPtr]$Hwnd)
  $docCond = New-Object System.Windows.Automation.PropertyCondition($P, $CT::Document)
  $kinds = @($CT::Text, $CT::Button, $CT::Edit, $CT::Hyperlink) | ForEach-Object { New-Object System.Windows.Automation.PropertyCondition($P, $_) }
  $leafCond = New-Object System.Windows.Automation.OrCondition($kinds)
  $docs = $root.FindAll([System.Windows.Automation.TreeScope]::Descendants, $docCond)
  foreach ($d in $docs) {
    $out.WriteLine('D' + [char]9 + ($d.Current.Name -replace '\s+', ' '))
    foreach ($e in $d.FindAll([System.Windows.Automation.TreeScope]::Descendants, $leafCond)) {
      try {
        $c = $e.Current
        $n = $c.Name -replace '[\t\r\n]+', ' '
        if (-not $n) { continue }
        $r = $c.BoundingRectangle
        if ($r.IsEmpty) { continue }
        $t = $c.ControlType.ProgrammaticName -replace '^ControlType\.', ''
        $out.WriteLine([string]::Join([char]9, @('I', $t, [int]$r.Left, [int]$r.Top, [int]$r.Width, [int]$r.Height, [int]$c.IsOffscreen, $n)))
      } catch { }
    }
  }
  $out.Flush()
} catch {
  $out.WriteLine('E ' + ($_.Exception.Message -replace '\s+', ' ')); $out.Flush(); exit 1
}
`

type ayItem struct {
	kind       string
	x, y, w, h int
	off        bool
	name       string
}

type ayDoc struct {
	name  string
	items []ayItem
}

// parseAyDump reads ayChatScript's output.
func parseAyDump(out string) ([]ayDoc, error) {
	var docs []ayDoc
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.HasPrefix(l, "E "):
			return nil, errors.New("UI Automation: " + l[2:])
		case strings.HasPrefix(l, "D\t"):
			docs = append(docs, ayDoc{name: l[2:]})
		case strings.HasPrefix(l, "I\t") && len(docs) > 0:
			f := strings.SplitN(l, "\t", 8)
			if len(f) < 8 {
				continue
			}
			it := ayItem{kind: f[1], off: f[6] == "1", name: f[7]}
			it.x, _ = strconv.Atoi(f[2])
			it.y, _ = strconv.Atoi(f[3])
			it.w, _ = strconv.Atoi(f[4])
			it.h, _ = strconv.Atoi(f[5])
			docs[len(docs)-1].items = append(docs[len(docs)-1].items, it)
		}
	}
	return docs, nil
}

func ayIsButton(it ayItem, names ...string) bool {
	if it.kind != "Button" {
		return false
	}
	for _, n := range names {
		if strings.EqualFold(strings.TrimSpace(it.name), n) || strings.HasPrefix(strings.ToLower(it.name), strings.ToLower(n)+" (") {
			return true
		}
	}
	return false
}

// ayChatDoc picks the chat: the document holding the query/answer buttons, never the sidebar or app bar.
func ayChatDoc(docs []ayDoc) (chat, side *ayDoc) {
	for i := range docs {
		d := &docs[i]
		ln := strings.ToLower(d.name)
		if strings.Contains(ln, "sidebar") {
			side = d
			continue
		}
		if strings.Contains(ln, "app bar") {
			continue
		}
		for _, it := range d.items {
			if ayIsButton(it, "Copy query", "Edit query", "Helpful", "Fork") || it.kind == "Edit" && strings.Contains(strings.ToLower(it.name), "ask a follow-up") {
				if chat == nil {
					chat = d
				}
				break
			}
		}
	}
	return chat, side
}

// buttons of Perplexity's own interface: never part of what the answer says
var ayUIButtons = map[string]bool{"copy": true, "helpful": true, "not helpful": true, "fork": true, "more actions": true, "share": true,
	"rewrite": true, "copy code": true, "wrap lines": true, "edit query": true, "copy query": true, "show more": true, "show less": true,
	"scroll to end": true, "export as pdf": true, "export as markdown": true, "export as docx": true}

// ayTurn is the newest finished answer of a chat.
type ayTurn struct {
	query   string   // the user's message it answers
	lines   []string // the answer, row by row
	waiting bool     // Perplexity is still writing
}

// ayNewestAnswer finds the newest answer that is finished and not yet followed by a newer query.
// ok is false while Perplexity is answering, when the user (or auto-yes) has written since, or
// when there is no answer at all.
func ayNewestAnswer(d ayDoc) (t ayTurn, ok bool) {
	qi, hi, answering := ayTurnBounds(d.items)
	if answering {
		return ayTurn{waiting: true}, false
	}
	if hi < 0 || hi < qi { // no finished answer, or a newer query stands after it
		return t, false
	}
	start, query := ayQueryBubble(d.items, qi, hi)
	t.query = query
	t.lines = ayRows(d.items[start:hi])
	return t, len(t.lines) > 0
}

// ayTurnBounds: index of the newest [Copy query] and of the newest answer's [Helpful]/[Fork] row.
func ayTurnBounds(its []ayItem) (qi, hi int, answering bool) {
	qi, hi = -1, -1
	for i, it := range its {
		switch {
		case it.kind == "Button" && strings.HasPrefix(strings.ToLower(it.name), "stop response"):
			return qi, hi, true
		case ayIsButton(it, "Copy query", "Edit query"):
			qi = i
		case ayIsButton(it, "Helpful", "Fork"):
			hi = i
		}
	}
	return qi, hi, false
}

// ayQueryBubble skips the user's message after [Copy query] (right-aligned text, "Show more")
// and returns where the answer starts plus the message's text.
func ayQueryBubble(its []ayItem, qi, hi int) (int, string) {
	col := 1 << 30 // the answer column: the leftmost text in the chat
	for _, it := range its {
		if it.kind == "Text" && it.w > 0 {
			col = min(col, it.x)
		}
	}
	var q []string
	i := qi + 1
	for ; i < hi; i++ {
		it := its[i]
		if ayIsButton(it, "Edit query", "Copy query", "Show more", "Show less") {
			continue
		}
		if it.kind != "Text" || it.x <= col+150 {
			break
		}
		q = append(q, strings.TrimSpace(it.name))
	}
	return i, strings.Join(q, " ")
}

// ayRows lays answer items out row by row, leaving out Perplexity's own buttons and the input box.
func ayRows(its []ayItem) []string {
	var lines []string
	var b strings.Builder
	lastY, lastH, lastR := -1<<30, 0, 0
	flush := func() {
		if l := strings.Join(strings.Fields(b.String()), " "); l != "" {
			lines = append(lines, l)
		}
		b.Reset()
	}
	for _, it := range its {
		if it.kind == "Edit" || it.kind == "Button" && ayUIButtons[strings.ToLower(strings.TrimSpace(it.name))] {
			continue
		}
		if b.Len() > 0 {
			if absInt(it.y-lastY) > max(4, min(it.h, lastH)/2) {
				flush()
			} else if it.x > lastR+16 {
				b.WriteString(" ")
			}
		}
		b.WriteString(it.name)
		lastY, lastH, lastR = it.y, it.h, it.x+it.w
	}
	flush()
	return lines
}

// ayGenericReply is the bare "Reply yes to proceed or no to cancel" under an action card.
var ayGenericReply = regexp.MustCompile(`(?i)^\W*(type|reply|say|respond|answer|send)\b[^?]{0,40}\byes\b[^?]{0,60}$`)

// aySentence narrows a line to the sentence holding [a, b).
func aySentence(line string, a, b int) string {
	start := 0
	for i := a - 1; i > 0; i-- {
		if (line[i-1] == '.' || line[i-1] == '!' || line[i-1] == '?') && line[i] == ' ' {
			start = i
			break
		}
	}
	end := b
	if end < len(line) && line[end] != '.' && line[end] != '?' && line[end] != '!' {
		if k := strings.IndexAny(line[end:], ".?!"); k >= 0 {
			end += k + 1
		} else {
			end = len(line)
		}
	} else if end < len(line) {
		end++
	}
	return strings.TrimSpace(line[start:end])
}

// ayAnswerQuestion returns the go-ahead question the answer closes with (the question alone,
// for the log), the closing lines it sits in (for the delete/overwrite check), and a key that
// is the same however often the answer is read again.
func ayAnswerQuestion(t ayTurn, anyQuestion bool) (question, around, key string) {
	n := len(t.lines)
	from := max(0, n-4) // the close of the answer: a question asked earlier is not waiting
	tail := t.lines[from:]
	for li := len(tail) - 1; li >= max(0, len(tail)-2) && question == ""; li-- { // the question is in the last two rows
		line := tail[li]
		best := []int(nil)
		pats := ayAsk
		if li == len(tail)-1 { // "just say yes", "(yes / no)": only as the answer's very last line
			pats = append(append([]*regexp.Regexp{}, ayAsk...), ayYes)
		}
		for _, re := range pats {
			if locs := re.FindAllStringIndex(line, -1); len(locs) > 0 {
				if l := locs[len(locs)-1]; best == nil || l[0] > best[0] {
					best = l
				}
			}
		}
		if best == nil && anyQuestion && li >= len(tail)-2 {
			if locs := ayAnyQuestion.FindAllStringIndex(line, -1); len(locs) > 0 {
				best = locs[len(locs)-1]
			}
		}
		if best != nil {
			question = aySentence(line, best[0], best[1])
			if ayGenericReply.MatchString(question) { // log the action card's own question instead
				for k := li - 1; k >= 0; k-- {
					if strings.HasSuffix(tail[k], "?") {
						question = tail[k]
						break
					}
				}
			}
		}
	}
	question = strings.TrimSpace(question)
	if question == "" || ayOwnReply(question) {
		return "", "", ""
	}
	around = strings.Join(tail, "\n")
	sum := sha256.Sum256([]byte(strings.ToLower(around)))
	return question, around, hex.EncodeToString(sum[:8])
}

// ayOwnReply: what auto-yes itself types; never something to answer.
func ayOwnReply(s string) bool {
	s = strings.ToLower(strings.Trim(strings.TrimSpace(s), `"'“”.!`))
	return s == "yes" || s == "y" || s == "no"
}

// background threads that wait on the user, as the sidebar shows them
var ayWaitingRe = regexp.MustCompile(`(?i)waiting for (your )?(response|reply|input|approval|confirmation)|needs your (input|response|approval|attention)|awaiting (your )?(response|input)`)

// ayWaitingThreads lists sidebar entries marked as waiting, with the point to click.
func ayWaitingThreads(side *ayDoc) []ayItem {
	if side == nil {
		return nil
	}
	var out []ayItem
	for i, it := range side.items {
		if !ayWaitingRe.MatchString(it.name) {
			continue
		}
		hit := it
		if it.kind != "Button" { // the marker is a label: click the history entry on its row
			for k := i - 1; k >= 0 && k >= i-6; k-- {
				if b := side.items[k]; b.kind == "Button" && absInt(b.y-it.y) < 80 && !ayIsButton(b, "Session options") {
					hit = b
					break
				}
			}
		}
		out = append(out, hit)
	}
	return out
}

// ayReadWindow reads one window's documents (Windows only).
func ayReadWindow(w winInfo) ([]ayDoc, error) {
	if !isWin {
		return nil, errNoInput
	}
	out, _, err := runUIAScript("uiachat.ps1", ayChatScript, w.hwnd, 30*time.Second)
	docs, perr := parseAyDump(string(out))
	if perr != nil {
		return nil, perr
	}
	if err != nil && len(docs) == 0 {
		return nil, fmt.Errorf("UI Automation failed: %v", err)
	}
	return docs, nil
}

// ayWindows lists every Perplexity window: the app's, and browser windows on perplexity.ai.
func ayWindows() []winInfo {
	var out []winInfo
	for _, w := range listWindowsRaw() {
		exe := strings.ToLower(strings.TrimSuffix(w.exe, ".exe"))
		title := strings.ToLower(w.title)
		switch {
		case exe == "perplexity" && w.w > 300 && w.h > 300:
		case (exe == "chrome" || exe == "msedge" || exe == "brave" || exe == "firefox") && strings.Contains(title, "perplexity"):
		default:
			continue
		}
		out = append(out, w)
	}
	return out
}
