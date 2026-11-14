package main

// The ELAI hub: one llama-server router (start-hub.cmd → llama-server --models-preset models.ini)
// that serves every model in models.ini by name on one port and loads them on demand. TokenSaver
// uses it as the local AI by default when it exists, picks a model by name instead of a script,
// and reads from models.ini which models can see pictures (an mmproj line).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// elaiHubScript is where the hub's launcher lives on the user's PC (tests blank it out, so
// they never depend on what is installed on the machine running them).
var elaiHubScript = `D:\ELAI\start-hub.cmd`

type hubModel struct {
	Name, Model string
	Vision      bool // has an mmproj: it can look at pictures
	Startup     bool // load-on-startup
}

var presetRe = regexp.MustCompile(`(?i)--models-preset[ =]+(?:"([^"]+)"|(\S+))`)

// hubPreset is the models.ini a hub launcher passes to llama-server ("" when script is not a hub).
func hubPreset(script string) string {
	b, err := os.ReadFile(script)
	if err != nil {
		return ""
	}
	m := presetRe.FindStringSubmatch(string(b))
	if m == nil {
		return ""
	}
	p := m[1]
	if p == "" {
		p = m[2]
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(filepath.Dir(script), p)
	}
	return p
}

func isHubScript(script string) bool { return script != "" && hubPreset(script) != "" }

// parseHubPreset reads the [sections] of a llama-server models preset; [*] holds the shared defaults.
func parseHubPreset(b []byte) []hubModel {
	var out []hubModel
	var cur *hubModel
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == ';' || l[0] == '#' {
			continue
		}
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			name := strings.TrimSpace(l[1 : len(l)-1])
			if name == "*" || name == "" {
				cur = nil
				continue
			}
			out = append(out, hubModel{Name: name})
			cur = &out[len(out)-1]
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if !ok || cur == nil {
			continue
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		switch k {
		case "model", "m":
			cur.Model = v
		case "mmproj":
			cur.Vision = v != ""
		case "load-on-startup":
			cur.Startup = strings.EqualFold(v, "true") || v == "1"
		}
	}
	return out
}

// hubModels lists the models a hub launcher serves.
func hubModels(script string) []hubModel {
	p := hubPreset(script)
	if p == "" {
		return nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	return parseHubPreset(b)
}

// hubDefaultModel is the model to use when none is set: the first loaded on startup, else the first.
func hubDefaultModel(ms []hubModel) string {
	for _, m := range ms {
		if m.Startup {
			return m.Name
		}
	}
	if len(ms) > 0 {
		return ms[0].Name
	}
	return ""
}

// hubVision tells whether a hub model can look at pictures (from models.ini).
func hubVision(script, model string) bool {
	for _, m := range hubModels(script) {
		if strings.EqualFold(m.Name, model) {
			return m.Vision
		}
	}
	return false
}

// autoAIScript is the default local AI launcher: the ELAI hub when it is there, else the old script.
func autoAIScript() string {
	if isWin && elaiHubScript != "" && exists(elaiHubScript) {
		return elaiHubScript
	}
	return defaultAIScript
}

// applyAIDefaults fills ai_script / ai_model with what is really on the PC when config leaves them empty.
func applyAIDefaults(c *Config) {
	if c.AIScript == "" && isWin {
		c.AIScript = autoAIScript()
	}
	if c.AIModel == "" && c.AIEndpoint == "" && isHubScript(c.AIScript) {
		c.AIModel = hubDefaultModel(hubModels(c.AIScript))
	}
}

// withHubModels: when a hub exists, its models stand in for entries whose start script is gone.
func withHubModels(scripts map[string]string, order []string, haveGGUF func(string) bool) (map[string]string, []string) {
	hub := ""
	switch {
	case isHubScript(cfg.AIScript):
		hub = cfg.AIScript
	case isWin && elaiHubScript != "" && exists(elaiHubScript):
		hub = elaiHubScript
	}
	if hub == "" {
		return scripts, order
	}
	out := map[string]string{}
	var names []string
	for _, n := range order {
		if s := scripts[n]; exists(s) || (haveGGUF != nil && haveGGUF(n)) {
			out[n] = s
			names = append(names, n)
		}
	}
	for _, m := range hubModels(hub) {
		if _, ok := out[m.Name]; !ok {
			out[m.Name] = hub
			names = append(names, m.Name)
		}
	}
	return out, names
}

// hubUse makes a hub model the current one: starts the hub when it is down, then asks the router
// to load that model (it unloads another one by itself when --models-max is reached).
func hubUse(name, script string, waitSecs int) (string, error) {
	found := false
	for _, m := range hubModels(script) {
		if strings.EqualFold(m.Name, name) {
			found = true
		}
	}
	if !found {
		return "", fmt.Errorf("%s is not in %s", name, hubPreset(script))
	}
	host, port := scriptAddr(script)
	base := fmt.Sprintf("http://%s:%d", host, port)
	c := &aiClient{kind: "openai", base: base}
	note := ""
	if !c.healthy() {
		if _, err := guardStartScript(script); err != nil {
			return "", fmt.Errorf("couldn't start the hub %s: %v", script, err)
		}
		note = " (started the hub)"
		for i := 0; i < 60 && !c.healthy(); i++ {
			time.Sleep(time.Second)
		}
	}
	setAIScript(script)
	setAIModel(name)
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("the hub's port %d is not valid", port)
	}
	b, err := json.Marshal(map[string]string{"model": name})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(max(waitSecs, 10))*time.Second)
	defer cancel()
	// always this PC's own loopback address: the hub listens on all addresses, port from its launcher
	loadURL := (&url.URL{Scheme: "http", Host: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), Path: "/models/load"}).String()
	if req, err := http.NewRequestWithContext(ctx, "POST", loadURL, bytes.NewReader(b)); err == nil { // #nosec G704 -- loopback only
		req.Header.Set("Content-Type", "application/json")
		if resp, err := http.DefaultClient.Do(req); err == nil { // #nosec G704 -- loopback only // the router loads it; a plain error just means it loads on first use
			resp.Body.Close()
		}
	}
	return fmt.Sprintf("%s is the local AI now, served by the hub on port %d%s; it loads on first use if it isn't loaded yet", name, port, note), nil
}

// hubScriptFor is the hub launcher when name is one of its models ("" otherwise).
func hubScriptFor(name string) string {
	for _, hub := range []string{cfg.AIScript, elaiHubScript} {
		if hub == "" || (hub == elaiHubScript && !isWin) || !isHubScript(hub) {
			continue
		}
		for _, m := range hubModels(hub) {
			if strings.EqualFold(m.Name, name) {
				return hub
			}
		}
	}
	return ""
}
