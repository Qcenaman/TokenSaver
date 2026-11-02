package main

// GitHub repos as paths: owner/name[@ref][/sub/path] (not absolute) in find, read,
// scan, check, outline and git means that repo. TokenSaver keeps a temporary copy in
// data\repos\<owner>\<name> (a partial clone: all history, file contents fetched as
// needed), made with the user's GitHub sign-in, reused while Claude works, fetched and
// fast-forwarded when older than repoFresh and deleted once unused for repoKeep. @ref
// (branch, tag or commit) is a worktree beside it, <name>@<ref>, sharing its objects.
// Only folders in data\repos are ever changed or deleted; the user's own clones never.

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	repoURLBase = "https://github.com/"
	repoFresh   = 5 * time.Minute
	repoKeep    = 24 * time.Hour
	repoWait    = 50 * time.Second
)

var (
	repoOwnerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}$`)
	repoNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
)

type repoSpec struct {
	Owner, Name string
	Ref         string // after @: the ref, possibly followed by /sub/path (split once the copy exists)
	Sub         string // path inside the repo when there is no @ref
	HasRef      bool
}

// parseRepoSpec reads owner/name[@ref][/sub/path] (also github.com/owner/name...);
// ok is false for anything else: absolute paths, ~, %VAR%, drive letters, other hosts.
func parseRepoSpec(p string) (repoSpec, bool) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(p), `"`))
	low := strings.ToLower(s)
	for _, pre := range []string{"https://github.com/", "http://github.com/", "github.com/"} {
		if strings.HasPrefix(low, pre) {
			s = s[len(pre):]
			break
		}
	}
	if s == "" || strings.ContainsAny(s, ":%~*?\"<>|") || strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) || filepath.IsAbs(s) {
		return repoSpec{}, false
	}
	s = strings.ReplaceAll(s, `\`, "/")
	owner, rest, ok := strings.Cut(s, "/")
	if !ok || !repoOwnerRe.MatchString(owner) {
		return repoSpec{}, false
	}
	name, tail := rest, ""
	if i := strings.IndexAny(rest, "/@"); i >= 0 {
		name, tail = rest[:i], rest[i:]
	}
	name = strings.TrimSuffix(name, ".git")
	if !repoNameRe.MatchString(name) || strings.Trim(name, ".") == "" {
		return repoSpec{}, false
	}
	sp := repoSpec{Owner: owner, Name: name}
	if strings.HasPrefix(tail, "@") {
		sp.HasRef, sp.Ref = true, strings.Trim(tail[1:], "/")
		if sp.Ref == "" {
			return repoSpec{}, false
		}
	} else {
		sp.Sub = strings.Trim(tail, "/")
	}
	for _, part := range strings.Split(sp.Ref+"/"+sp.Sub, "/") {
		if part == ".." {
			return repoSpec{}, false
		}
	}
	return sp, true
}

func reposDir() string { return filepath.Join(appHome, "repos") }

func repoURL(sp repoSpec) string { return repoURLBase + sp.Owner + "/" + sp.Name + ".git" }

func repoBaseDir(sp repoSpec) string {
	return filepath.Join(reposDir(), strings.ToLower(sp.Owner), strings.ToLower(sp.Name))
}

// repoStateFile: lock and time stamps live beside the copies, never inside them.
func repoStateFile(sp repoSpec, file string) string {
	return filepath.Join(reposDir(), ".state", strings.ToLower(sp.Owner), file)
}

func safeRef(r string) string {
	return strings.Map(func(c rune) rune {
		if strings.ContainsRune(`/\:*?"<>| `, c) {
			return '_'
		}
		return c
	}, r)
}

func firstPart(s string) string { return strings.SplitN(s, "/", 2)[0] }

func gitArgsNet(args ...string) []string { return append(append([]string{}, gitNetFlags...), args...) }

type repoCopy struct {
	Root  string // the copy's folder (main copy or @ref worktree)
	Local string // Root plus the path inside the repo
	Sub   string // that path, with slashes
	Label string // owner/name or owner/name@ref
	Note  string // set when the copy could not be updated
}

// short writes the copy's local paths in s as owner/name[@ref]/..., which can be passed back.
func (c *repoCopy) short(s string) string {
	s = strings.ReplaceAll(s, c.Root+string(filepath.Separator), c.Label+"/")
	return strings.ReplaceAll(s, c.Root, c.Label)
}

// repoFixer shortens the copies' paths in a tool result and puts any update notes first.
func repoFixer(cs []*repoCopy) func(string) string {
	sort.Slice(cs, func(i, j int) bool { return len(cs[i].Root) > len(cs[j].Root) })
	return func(s string) string {
		var notes []string
		for _, c := range cs {
			s = c.short(s)
			if c.Note != "" {
				notes = append(notes, c.Note)
			}
		}
		if len(notes) > 0 {
			s = strings.Join(notes, "\n") + "\n" + s
		}
		return s
	}
}

// within runs fn but answers after repoWait at most; fn finishes in the background and
// the next call finds its work done.
func within(what string, fn func() error) error {
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("internal error: %v", r)
			}
		}()
		done <- fn()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(repoWait):
		return fmt.Errorf("still %s (a big repo); call again in a minute", what)
	}
}

// repoResolve gives TokenSaver's copy for an owner/name[@ref][/sub] path, cloning or
// updating it first; ok is false when p is not such a path.
func repoResolve(p string) (*repoCopy, bool, error) {
	sp, ok := parseRepoSpec(p)
	if !ok {
		return nil, false, nil
	}
	c, err := repoGet(sp)
	return c, true, err
}

func repoGet(sp repoSpec) (*repoCopy, error) {
	var c *repoCopy
	err := within("getting "+sp.Owner+"/"+sp.Name+" onto the PC", func() error {
		var e error
		c, e = repoEnsure(sp)
		return e
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// fileLock takes an exclusive lock file shared by every TokenSaver process (Claude
// desktop runs more than one), waiting up to wait; a lock older than 30 minutes is stale.
func fileLock(p string, wait time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprint(f, os.Getpid())
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if fi, serr := os.Stat(p); serr == nil && time.Since(fi.ModTime()) > 30*time.Minute {
			os.Remove(p)
			continue
		}
		if !time.Now().Before(deadline) {
			return nil, errors.New("busy: " + filepath.Base(p))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func touchStamp(p string) {
	now := time.Now()
	if os.Chtimes(p, now, now) != nil {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, nil, 0o644)
	}
}

func stampTime(p string) time.Time {
	if fi, err := os.Stat(p); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

func repoEnsure(sp repoSpec) (*repoCopy, error) {
	name := strings.ToLower(sp.Name)
	unlock, err := fileLock(repoStateFile(sp, name+".lock"), 25*time.Minute)
	if err != nil {
		return nil, err
	}
	defer unlock()
	label := sp.Owner + "/" + sp.Name
	base := repoBaseDir(sp)
	fetchedFile := repoStateFile(sp, name+".fetched")
	note, fresh := "", false
	if !exists(filepath.Join(base, ".git", "HEAD")) {
		if err := repoClone(sp, base); err != nil {
			return nil, err
		}
		touchStamp(fetchedFile)
		fresh = true
	} else if time.Since(stampTime(fetchedFile)) >= repoFresh {
		if err := repoUpdate(base); err != nil {
			note = fmt.Sprintf("(%s not updated: %s; using the copy already on the PC)", label, err)
		} else {
			touchStamp(fetchedFile)
			fresh = true
		}
	}
	root, sub := base, sp.Sub
	if sp.HasRef {
		ref, rest, commit, ok := repoFindRef(base, sp.Ref)
		if !ok && !fresh && repoUpdate(base) == nil {
			touchStamp(fetchedFile)
			ref, rest, commit, ok = repoFindRef(base, sp.Ref)
		}
		if !ok {
			return nil, fmt.Errorf("%s has no branch, tag or commit %q", label, firstPart(sp.Ref))
		}
		root = base + "@" + safeRef(ref)
		if err := repoWorktree(base, root, commit); err != nil {
			return nil, fmt.Errorf("%s@%s: %v", label, ref, err)
		}
		sub, label = rest, label+"@"+ref
	}
	touchStamp(repoStateFile(sp, filepath.Base(root)+".used"))
	go repoSweep()
	c := &repoCopy{Root: root, Local: root, Sub: sub, Label: label, Note: note}
	if sub != "" {
		c.Local = filepath.Join(root, filepath.FromSlash(sub))
		if !exists(c.Local) {
			return nil, fmt.Errorf("%s has no %s", label, sub)
		}
	}
	return c, nil
}

// repoClone makes the copy in a .cloning folder first, so a half-done clone is never used.
func repoClone(sp repoSpec, base string) error {
	tmp := base + ".cloning"
	if exists(tmp) {
		repoRemoveAll(tmp) // left over from an interrupted clone
	}
	if err := os.MkdirAll(filepath.Dir(base), 0o755); err != nil {
		return err
	}
	out, code, err := gitExec(filepath.Dir(base), 25*time.Minute, gitArgsNet("clone", "--quiet", "--filter=blob:none", repoURL(sp), tmp)...)
	if err == nil && code != 0 {
		err = fmt.Errorf("couldn't clone %s/%s: %s", sp.Owner, sp.Name, gitShort(out, 4))
	}
	if err != nil {
		repoRemoveAll(tmp)
		return err
	}
	if exists(base) {
		repoRemoveAll(base) // a broken copy
	}
	for i := 0; ; i++ { // antivirus can hold a fresh file for a moment
		if err = os.Rename(tmp, base); err == nil || i == 10 {
			return err
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// repoUpdate fetches and fast-forwards the main copy; a rewritten history is taken over
// as it is, since the copy is TokenSaver's own.
func repoUpdate(base string) error {
	out, code, err := gitExec(base, 10*time.Minute, gitArgsNet("fetch", "--quiet", "--prune", "--tags", "--force", "origin")...)
	if err != nil {
		return err
	}
	if code != 0 {
		return errors.New(gitShort(out, 2))
	}
	if _, code, _ := gitExec(base, 5*time.Minute, gitArgsNet("merge", "--ff-only", "--quiet", "@{u}")...); code != 0 {
		if out, code, err := gitExec(base, 5*time.Minute, gitArgsNet("reset", "--hard", "--quiet", "@{u}")...); err != nil || code != 0 {
			return fmt.Errorf("reset failed: %s", firstErr(out, err))
		}
	}
	return nil
}

// repoFindRef splits ref[/sub] into a branch, tag or commit of the copy and the path
// after it (branch names can hold slashes, so each split is tried, shortest first).
func repoFindRef(base, refSub string) (ref, sub, commit string, ok bool) {
	parts := strings.Split(refSub, "/")
	for n := 1; n <= len(parts); n++ {
		r := strings.Join(parts[:n], "/")
		for _, cand := range []string{"refs/remotes/origin/" + r, "refs/tags/" + r, r} {
			out, code, err := gitExec(base, 30*time.Second, "rev-parse", "--verify", "--quiet", cand+"^{commit}")
			if err == nil && code == 0 && out != "" {
				return r, strings.Join(parts[n:], "/"), strings.TrimSpace(out), true
			}
		}
	}
	return "", "", "", false
}

func repoWorktree(base, dir, commit string) error {
	if exists(filepath.Join(dir, ".git")) {
		if head, _, _ := gitExec(dir, 30*time.Second, "rev-parse", "HEAD"); head == commit {
			return nil
		}
		out, code, err := gitExec(dir, 10*time.Minute, gitArgsNet("checkout", "--quiet", "--force", "--detach", commit)...)
		if err == nil && code != 0 {
			err = errors.New(gitShort(out, 3))
		}
		return err
	}
	if exists(dir) {
		repoRemoveAll(dir)
	}
	gitExec(base, time.Minute, "worktree", "prune")
	out, code, err := gitExec(base, 10*time.Minute, gitArgsNet("worktree", "add", "--quiet", "--force", "--detach", dir, commit)...)
	if err == nil && code != 0 {
		err = errors.New(gitShort(out, 3))
	}
	return err
}

// repoRemoveAll deletes one of TokenSaver's own copies; anything outside data\repos is refused.
func repoRemoveAll(p string) error {
	if !hasPathPrefix(p, reposDir()) || samePath(p, reposDir()) {
		return errors.New("refusing to delete outside the repos folder: " + p)
	}
	err := os.RemoveAll(p)
	if err != nil { // read-only git objects
		filepath.WalkDir(p, func(f string, d fs.DirEntry, e error) error {
			if e == nil {
				os.Chmod(f, 0o777)
			}
			return nil
		})
		err = os.RemoveAll(p)
	}
	return err
}

var (
	repoSweepMu sync.Mutex
	repoSweptAt time.Time
)

// repoSweep removes copies unused for repoKeep, at most once an hour.
func repoSweep() {
	repoSweepMu.Lock()
	if time.Since(repoSweptAt) < time.Hour {
		repoSweepMu.Unlock()
		return
	}
	repoSweptAt = time.Now()
	repoSweepMu.Unlock()
	repoSweepNow(time.Now().Add(-repoKeep))
}

// repoSweepNow deletes copies last used before cutoff: @ref worktrees first, then a
// repo's main copy once neither it nor any of its worktrees is in use. A repo being
// worked on right now (locked) is left alone.
func repoSweepNow(cutoff time.Time) (removed []string) {
	owners, _ := os.ReadDir(reposDir())
	for _, o := range owners {
		if !o.IsDir() || strings.HasPrefix(o.Name(), ".") {
			continue
		}
		odir := filepath.Join(reposDir(), o.Name())
		entries, _ := os.ReadDir(odir)
		groups := map[string][]string{}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			repo := strings.TrimSuffix(e.Name(), ".cloning")
			if i := strings.Index(repo, "@"); i > 0 {
				repo = repo[:i]
			}
			groups[repo] = append(groups[repo], e.Name())
		}
		for repo, dirs := range groups {
			sp := repoSpec{Owner: o.Name(), Name: repo}
			unlock, err := fileLock(repoStateFile(sp, repo+".lock"), 0)
			if err != nil {
				continue
			}
			inUse, prune := false, false
			for _, d := range dirs {
				if d == repo {
					continue
				}
				if repoLastUsed(sp, odir, d).After(cutoff) {
					inUse = true
					continue
				}
				if repoRemoveAll(filepath.Join(odir, d)) == nil {
					os.Remove(repoStateFile(sp, d+".used"))
					removed = append(removed, o.Name()+"/"+d)
					prune = true
				}
			}
			base := filepath.Join(odir, repo)
			if exists(base) && !inUse && !repoLastUsed(sp, odir, repo).After(cutoff) {
				if repoRemoveAll(base) == nil {
					os.Remove(repoStateFile(sp, repo+".used"))
					os.Remove(repoStateFile(sp, repo+".fetched"))
					removed = append(removed, o.Name()+"/"+repo)
				}
			} else if prune && exists(base) {
				gitExec(base, time.Minute, "worktree", "prune")
			}
			unlock()
		}
		os.Remove(odir) // only goes when empty
	}
	sort.Strings(removed)
	return removed
}

func repoLastUsed(sp repoSpec, odir, d string) time.Time {
	if t := stampTime(repoStateFile(sp, d+".used")); !t.IsZero() {
		return t
	}
	return stampTime(filepath.Join(odir, d))
}

// repoArgs resolves owner/name paths in the given arguments (a string or a list of
// strings) to TokenSaver's copies; the returned func shortens the result's paths again.
func repoArgs(raw json.RawMessage, keys ...string) (json.RawMessage, func(string) string, error) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return raw, nil, nil
	}
	var cs []*repoCopy
	resolve := func(p string) (string, error) {
		c, ok, err := repoResolve(p)
		if !ok || err != nil {
			return p, err
		}
		cs = append(cs, c)
		return c.Local, nil
	}
	for _, k := range keys {
		v, has := m[k]
		if !has {
			continue
		}
		var one string
		var many []string
		if json.Unmarshal(v, &one) == nil {
			p, err := resolve(one)
			if err != nil {
				return nil, nil, err
			}
			m[k], _ = json.Marshal(p)
		} else if json.Unmarshal(v, &many) == nil {
			for i := range many {
				p, err := resolve(many[i])
				if err != nil {
					return nil, nil, err
				}
				many[i] = p
			}
			m[k], _ = json.Marshal(many)
		}
	}
	if len(cs) == 0 {
		return raw, nil, nil
	}
	nb, err := json.Marshal(m)
	return nb, repoFixer(cs), err
}

// checkRepoArgs points check (and check build) at TokenSaver's copy for owner/name paths;
// fix:true keeps the separate clone of source repo, so the temporary copy stays as GitHub has it.
func checkRepoArgs(raw json.RawMessage) (json.RawMessage, func(string) string, error) {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return raw, nil, nil
	}
	src, _ := m["source"].(string)
	p, _ := m["path"].(string)
	fix, _ := m["fix"].(bool)
	ref, _ := m["ref"].(string)
	if src != "" && src != "pc" && src != "repo" {
		return raw, nil, nil
	}
	sp, ok := parseRepoSpec(p)
	if !ok {
		return raw, nil, nil
	}
	if fix {
		m["source"], m["path"] = "repo", sp.Owner+"/"+sp.Name
		if sp.HasRef && ref == "" {
			m["ref"] = firstPart(sp.Ref)
		}
		nb, err := json.Marshal(m)
		return nb, nil, err
	}
	if ref != "" && !sp.HasRef {
		sp.HasRef, sp.Ref, sp.Sub = true, strings.Trim(ref+"/"+sp.Sub, "/"), ""
	}
	c, err := repoGet(sp)
	if err != nil {
		return nil, nil, err
	}
	m["source"], m["path"] = "pc", c.Local
	delete(m, "ref")
	nb, err := json.Marshal(m)
	return nb, repoFixer([]*repoCopy{c}), err
}

func remoteKey(u string) string {
	u = strings.ToLower(strings.TrimSpace(filepath.ToSlash(u)))
	for _, pre := range []string{"https://", "http://", "ssh://", "git://", "file://"} {
		u = strings.TrimPrefix(u, pre)
	}
	if at := strings.Index(u, "@"); at >= 0 && at < strings.Index(u+"/", "/") {
		u = u[at+1:] // user@host
	}
	u = strings.Replace(u, "github.com:", "github.com/", 1)
	return strings.TrimSuffix(strings.TrimSuffix(u, "/"), ".git")
}

func hashName(s string) string {
	return fmt.Sprintf("%x", sha1.Sum([]byte(normCase(filepath.Clean(s)))))[:12]
}

// repoInfo: folder, branch (or what a detached copy is at) and last commit, plus extra.
func repoInfo(dir, label, extra string) string {
	br, _, _ := gitExec(dir, 20*time.Second, "rev-parse", "--abbrev-ref", "HEAD")
	if br == "HEAD" || br == "" {
		d, _, _ := gitExec(dir, 20*time.Second, "describe", "--tags", "--always")
		br = "detached at " + d
	} else {
		br = "branch " + br
	}
	last, _, _ := gitExec(dir, 20*time.Second, "log", "-1", "--date=short", "--format=%h %ad %s")
	var b strings.Builder
	if label != "" {
		b.WriteString(label + "\n")
	}
	fmt.Fprintf(&b, "%s\n%s · last commit %s", dir, br, last)
	if extra != "" {
		b.WriteString("\n" + extra)
	}
	return b.String()
}

// gitClone clones url (or owner/name) into to/path, or updates that folder when it is
// already a clone of the same repo; without a folder it gives TokenSaver's temporary copy.
func gitClone(a gitArgs) (string, error) {
	src, to := strings.TrimSpace(a.URL), strings.TrimSpace(a.To)
	if to == "" {
		to = strings.TrimSpace(a.Path)
	}
	if _, ok := parseRepoSpec(to); ok {
		if src == "" {
			src = to
		}
		to = ""
	}
	if src == "" {
		return "", errors.New("clone needs url (owner/name or a git URL)")
	}
	sp, gh := parseRepoSpec(src)
	if to == "" {
		if !gh {
			return "", errors.New("give to (a folder) to clone a URL that isn't on GitHub")
		}
		if a.Ref != "" && !sp.HasRef {
			sp.HasRef, sp.Ref, sp.Sub = true, a.Ref, ""
		}
		c, err := repoGet(sp)
		if err != nil {
			return "", err
		}
		return repoInfo(c.Root, c.Label+" (TokenSaver's temporary copy: read-only, removed after a day unused)", c.Note), nil
	}
	dest, err := expandPath(to)
	if err != nil {
		return "", err
	}
	url, ref := src, a.Ref
	if gh {
		url = repoURL(sp)
		if sp.HasRef && ref == "" {
			ref = firstPart(sp.Ref)
		}
	} else if !strings.Contains(url, "://") && !strings.HasPrefix(url, "git@") {
		return "", fmt.Errorf("url must be owner/name or a git URL: %s", src)
	}
	var res string
	err = within("cloning into "+dest, func() error {
		unlock, err := fileLock(filepath.Join(reposDir(), ".state", "clone-"+hashName(dest)+".lock"), 25*time.Minute)
		if err != nil {
			return err
		}
		defer unlock()
		res, err = gitCloneTo(url, dest, ref)
		return err
	})
	return res, err
}

func gitCloneTo(url, dest, ref string) (string, error) {
	if st, err := os.Stat(dest); err == nil {
		if !st.IsDir() {
			return "", fmt.Errorf("%s is a file", dest)
		}
		// asked of git, not by comparing paths: Windows short names (RUNNER~1) differ from git's
		prefix, code, _ := gitExec(dest, 20*time.Second, "rev-parse", "--show-prefix")
		if code == 0 && prefix == "" {
			origin, _, _ := gitExec(dest, 20*time.Second, "remote", "get-url", "origin")
			if remoteKey(origin) != remoteKey(url) {
				return "", fmt.Errorf("%s is already a clone of %s, not %s", dest, origin, url)
			}
			before, _, _ := gitExec(dest, 20*time.Second, "rev-parse", "--short", "HEAD")
			if out, code, err := gitExec(dest, 10*time.Minute, gitArgsNet("fetch", "--quiet", "--tags", "origin")...); err != nil || code != 0 {
				return "", fmt.Errorf("git fetch failed: %s", firstErr(out, err))
			}
			if ref != "" {
				if out, code, err := gitExec(dest, 2*time.Minute, "checkout", "--quiet", ref); err != nil || code != 0 {
					return "", fmt.Errorf("git checkout %s failed: %s", ref, firstErr(out, err))
				}
			}
			if _, code, _ := gitExec(dest, 20*time.Second, "rev-parse", "--abbrev-ref", "@{u}"); code == 0 {
				if out, code, err := gitExec(dest, 10*time.Minute, gitArgsNet("pull", "--ff-only", "--quiet")...); err != nil || code != 0 {
					return "", fmt.Errorf("%s is already a clone; git pull --ff-only failed:\n%s", dest, firstErr(out, err))
				}
			}
			after, _, _ := gitExec(dest, 20*time.Second, "rev-parse", "--short", "HEAD")
			state := "already a clone of it, up to date"
			if before != after {
				state = fmt.Sprintf("already a clone of it, updated %s..%s", before, after)
			}
			return repoInfo(dest, "", state), nil
		}
		if ents, _ := os.ReadDir(dest); len(ents) > 0 {
			return "", fmt.Errorf("%s already exists and is not a clone of %s", dest, url)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	args := gitArgsNet("clone", "--quiet")
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	out, code, err := gitExec(filepath.Dir(dest), 30*time.Minute, append(args, url, dest)...)
	if err != nil || code != 0 {
		return "", fmt.Errorf("git clone failed:\n%s", firstErr(out, err))
	}
	return repoInfo(dest, "", "cloned"), nil
}
