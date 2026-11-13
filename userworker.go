package main

// The user worker: the boot service (SYSTEM, session 0) answers the connector and the phone, but
// every tool call runs in a TokenSaver process of the signed-in user's, so files, Git, builds,
// windows and sign-ins are the user's own. The service starts the worker when someone signs in
// and hands it each call over 127.0.0.1 with a key that only this service run knows. With nobody
// signed in, tool calls answer that Windows needs a sign-in first.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type workerInfo struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Session uint32 `json:"session"`
}

func workerInfoPath() string { return filepath.Join(appHome, "userworker.json") }

func loadWorkerInfo() (workerInfo, bool) {
	var wi workerInfo
	b, err := os.ReadFile(workerInfoPath())
	return wi, err == nil && json.Unmarshal(b, &wi) == nil && wi.Port > 0
}

// userWorkerMain is `tokensaver user-worker <key>`, started by the service in the user's session.
func userWorkerMain(args []string) int {
	if len(args) < 1 || len(args[0]) < 32 {
		return 2
	}
	key := args[0]
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 1
	}
	mux := http.NewServeMux()
	authed := func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer "+key }
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) {
		if !authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		io.WriteString(w, "ok")
	})
	mux.HandleFunc("/rpc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || !authed(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var req rpcReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		out, _ := json.Marshal(handleRPC(req))
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
	})
	me := workerInfo{Port: ln.Addr().(*net.TCPAddr).Port, PID: os.Getpid(), Session: currentSessionID()}
	if err := writeJSONAtomic(workerInfoPath(), me); err != nil {
		return 1
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go srv.Serve(ln)
	// End when TokenSaver is updated or another worker took over; signing out ends it anyway.
	watch := newExeWatch()
	for range time.Tick(10 * time.Second) {
		if wi, ok := loadWorkerInfo(); !ok || wi.PID != me.PID || watch.changed() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			srv.Shutdown(ctx)
			cancel()
			return 0
		}
	}
	return 0
}

var worker struct {
	sync.Mutex
	key string
}

// workerKey is this service run's key for its worker (new on every service start).
func workerKey() string {
	if worker.key == "" {
		worker.key = randHex(24)
	}
	return worker.key
}

func pingWorker(wi workerInfo, key string) bool {
	req, _ := http.NewRequest("GET", "http://127.0.0.1:"+strconv.Itoa(wi.Port)+"/ping", nil)
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

var errNobodySignedIn = errors.New("nobody is signed in to Windows on this PC yet, so TokenSaver has no user to work as. Sign in to Windows (the PC can stay locked afterwards), then try again")

// ensureWorker returns a running worker in the active user's session, starting one if needed.
func ensureWorker() (workerInfo, error) {
	worker.Lock()
	defer worker.Unlock()
	key := workerKey()
	session, ok := activeUserSession()
	if !ok {
		return workerInfo{}, errNobodySignedIn
	}
	if wi, ok := loadWorkerInfo(); ok && wi.Session == session && pingWorker(wi, key) {
		return wi, nil
	}
	os.Remove(workerInfoPath()) // an old worker sees this and ends
	exe, _ := os.Executable()
	if ip := installPath(); exists(ip) {
		exe = ip
	}
	if _, err := runInUserSession(exe, []string{"user-worker", key}, "", 0); err != nil {
		return workerInfo{}, fmt.Errorf("could not start TokenSaver as the signed-in user: %v", err)
	}
	for i := 0; i < 40; i++ {
		time.Sleep(250 * time.Millisecond)
		if wi, ok := loadWorkerInfo(); ok && wi.Session == session && pingWorker(wi, key) {
			serveLog("user worker started (pid %d, session %d)", wi.PID, wi.Session)
			return wi, nil
		}
	}
	return workerInfo{}, errors.New("TokenSaver started as the signed-in user but did not answer within 10 seconds")
}

// forwardToWorker runs a tools/call in the user's worker and returns its JSON-RPC reply.
func forwardToWorker(ctx context.Context, body []byte, id json.RawMessage) []byte {
	fail := func(err error) []byte {
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{
			"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}})
		return out
	}
	for attempt := 0; attempt < 2; attempt++ {
		wi, err := ensureWorker()
		if err != nil {
			return fail(err)
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:"+strconv.Itoa(wi.Port)+"/rpc", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+workerKey())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return fail(ctx.Err())
			}
			continue // the worker went away (sign-out, update): start a new one once
		}
		out, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err == nil && resp.StatusCode == 200 {
			return out
		}
	}
	return fail(errors.New("TokenSaver's user worker did not answer; try again"))
}
