package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPreset = `version = 1
[*]
jinja = true
[coder]
; Qwen with vision
model = D:\LLM MODELS\Qwen3.8-27B-UD-Q6_K.gguf
mmproj = D:\LLM MODELS\mmproj-F16.gguf
load-on-startup = true
[deepseek]
model = D:\LLM MODELS\DeepSeek.gguf
mmproj = D:\LLM MODELS\ds-mmproj.gguf
load-on-startup = false
[ripple]
model = D:\LLM MODELS\Ripple-65B.gguf
load-on-startup = false
`

// a hub launcher like D:\ELAI\start-hub.cmd pointing at a models.ini like the user's
func testHub(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	ini := filepath.Join(dir, "models.ini")
	os.WriteFile(ini, []byte(testPreset), 0o644)
	script := filepath.Join(dir, "start-hub.cmd")
	os.WriteFile(script, []byte("@echo off\r\n\"llama-server.exe\" --models-preset \""+ini+"\" --models-max 1 --host 0.0.0.0 --port 8080\r\n"), 0o644)
	return script
}

func TestHubPreset(t *testing.T) {
	script := testHub(t)
	ms := hubModels(script)
	if len(ms) != 3 || ms[0].Name != "coder" || !ms[0].Vision || ms[2].Vision || !ms[0].Startup {
		t.Fatalf("models.ini read wrong: %+v", ms)
	}
	if hubDefaultModel(ms) != "coder" || !hubVision(script, "deepseek") || hubVision(script, "ripple") {
		t.Error("default model or vision from mmproj wrong")
	}
	c := Config{AIScript: script}
	applyAIDefaults(&c)
	if c.AIModel != "coder" {
		t.Errorf("with the hub and no ai_model, the model must be coder, got %q", c.AIModel)
	}
}

// dead start scripts give way to the hub's models in the model list
func TestHubModelsReplaceDeadScripts(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	cfg.AIScript = testHub(t)
	scripts, order := withHubModels(map[string]string{"qwen": `D:\llama.cpp\start-qwen3.6-27b-nothink.bat`}, []string{"qwen"}, nil)
	if strings.Join(order, ",") != "coder,deepseek,ripple" || scripts["coder"] != cfg.AIScript {
		t.Errorf("model list = %v", order)
	}
	if hubScriptFor("ripple") != cfg.AIScript || hubScriptFor("qwen") != "" {
		t.Error("hub models must resolve to the hub launcher")
	}
}

// a reasoning model that spends the whole limit thinking gets one retry with more room
func TestAIEmptyAnswerRetry(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var req map[string]any
		json.Unmarshal(b, &req)
		calls = append(calls, req)
		msg := map[string]any{"content": "", "reasoning_content": "Let me think about the picture..."}
		if req["max_tokens"].(float64) >= aiRetryTokens {
			msg = map[string]any{"content": "A fox jumping on a platform.", "reasoning_content": "..."}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	}))
	defer srv.Close()
	c := &aiClient{kind: "openai", base: srv.URL, model: "coder", hc: srv.Client(), noThink: true}
	got, err := c.chatImg("sys", "Describe it", []byte{0xff, 0xd8}, 300)
	if err != nil || got != "A fox jumping on a platform." {
		t.Fatalf("answer %q, %v", got, err)
	}
	if len(calls) != 2 || calls[0]["max_tokens"].(float64) < aiMinTokens {
		t.Errorf("calls: %d, first max_tokens %v (want at least %d)", len(calls), calls[0]["max_tokens"], aiMinTokens)
	}
	if _, ok := calls[0]["chat_template_kwargs"]; !ok {
		t.Error("thinking should be switched off for these short jobs")
	}
}

func TestAIMissingScriptError(t *testing.T) {
	old := cfg
	t.Cleanup(func() { cfg = old })
	cfg = Config{AIScript: filepath.Join(t.TempDir(), "start-qwen.bat")}
	_, _, err := openAI(nil, "")
	if err == nil || !strings.Contains(err.Error(), "start script not found: "+cfg.AIScript) || !strings.Contains(err.Error(), "ai_script") {
		t.Errorf("the error must name the missing script and the setting: %v", err)
	}
}

func TestAIServerProcNamed(t *testing.T) {
	if s := aiServerProc("llama-server.exe", `"C:\Perplexity\llama-server.exe" -m "C:\Models\nemotron-3.5-bolt.gguf" --port 39281`); !strings.Contains(s, "nemotron-3.5-bolt.gguf") {
		t.Errorf("a foreign model server must be named with its model: %q", s)
	}
	if aiServerProc("llama-server.exe", `llama-server.exe -hf Qwen/Qwen3-Embedding-0.6B-GGUF:Q8_0 --embedding --port 8091`) != "" {
		t.Error("the embedding server is TokenSaver's own and small: leave it out")
	}
	if aiServerProc("chrome.exe", "chrome.exe --type=gpu-process") != "" {
		t.Error("only model servers count")
	}
}

func TestConfigReloadAndStray(t *testing.T) {
	oldHome, oldCfg, oldStamp := appHome, cfg, cfgStamp
	t.Cleanup(func() { appHome, cfg, cfgStamp = oldHome, oldCfg, oldStamp })
	root := t.TempDir()
	appHome = filepath.Join(root, "data")
	os.MkdirAll(appHome, 0o755)
	cfgStamp = "before"
	os.WriteFile(filepath.Join(appHome, "config.json"), []byte(`{"ai_model":"one"}`), 0o644)
	refreshConfig()
	if cfg.AIModel != "one" {
		t.Fatal("config not read")
	}
	os.WriteFile(filepath.Join(appHome, "config.json"), []byte(`{"ai_model":"coder","workers":2}`), 0o644)
	refreshConfig()
	if cfg.AIModel != "coder" || cfg.Workers != 2 {
		t.Error("a changed config.json must be picked up without a restart")
	}
	if strayConfigNote() != "" {
		t.Error("no stray file yet")
	}
	os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"ai_model":"coder"}`), 0o644)
	if n := strayConfigNote(); !strings.Contains(n, "is not read") {
		t.Errorf("a config.json next to data must be reported: %q", n)
	}
}

func TestAutoYesStickyOff(t *testing.T) {
	old := appHome
	appHome = t.TempDir()
	t.Cleanup(func() { appHome = old })
	autoYesUserOff()
	if s := autoYesAutoOn(); !strings.Contains(s, "stays off") {
		t.Errorf("project / full auto mode must not switch a user's off back on: %q", s)
	}
	if autoYesRun() != 0 || loadAutoYes().On {
		t.Error("a watcher started anyway must end at once")
	}
	if s := autoYesRestart(); strings.Contains(s, "on since") {
		t.Error("a restart after an update must leave it off")
	}
	if !loadAutoYes().UserOff {
		t.Error("the user's off must stay recorded")
	}
}

// full auto mode answers overwrite questions inside the task, like "overwrites the 3 GLBs only"
func TestFullAutoAllowsOverwrite(t *testing.T) {
	q := "Run stage 3 (exports the characters; overwrites the 3 GLBs only)?"
	if !projectAllows(q, fullMode) {
		t.Error("full auto mode must answer an overwrite of the task's own files")
	}
	if projectAllows("Format drive D: and reinstall?", fullMode) && ayDriveWipe.MatchString("Format drive D:") {
		t.Error("wiping a drive still waits for the user")
	}
}

func TestResyncNoticeAndHelper(t *testing.T) {
	old := appHome
	appHome = t.TempDir()
	t.Cleanup(func() { appHome = old })
	if resyncNotice("Perplexity") == "" {
		t.Error("a client that never fetched this tool list must be told to re-sync")
	}
	noteToolsListed("Perplexity", fullOutputToolDefs(platformToolDefs()))
	if n := resyncNotice("Perplexity"); n != "" {
		t.Errorf("no notice once the list is current: %q", n)
	}
	out := string(appendResultNote([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"done"}],"isError":false}}`), "re-sync please"))
	if !strings.Contains(out, `done\n\nre-sync please`) {
		t.Errorf("note not added: %s", out)
	}
	helper := map[string]bool{}
	for _, d := range fullOutputToolDefs(platformToolDefs()) {
		var m struct{ Name, Description string }
		json.Unmarshal(d, &m)
		helper[m.Name] = strings.Contains(m.Description, "local AI helper")
	}
	if !helper["local_ai"] || !helper["browser"] || !helper["window"] || helper["run"] {
		t.Errorf("helper sentence on local_ai, window and browser only: %v", helper)
	}
	for _, d := range platformToolDefs() {
		if strings.Contains(string(d), "local AI helper") {
			t.Error("Claude's tool list must not change")
		}
	}
}
