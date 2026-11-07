package main

import "encoding/json"

// rvAIGate runs ContextSaver's GPU warning before the review's local-AI pass loads a model;
// it returns the warning when something else holds the GPU, "" to go ahead.
func rvAIGate(gpu string, force bool, req *AIRequest) string {
	if !force && gpu != "continue" {
		if w := gpuWarning(); w != "" {
			return w + " (to go ahead anyway: review again with gpu:\"continue\")"
		}
	}
	req.Force = true
	return ""
}

// rvDefaultMax gives a review called without max a shorter list, sized for a local model's context.
func rvDefaultMax(raw json.RawMessage, n int) json.RawMessage {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return raw
	}
	if _, ok := m["max"]; ok {
		return raw
	}
	m["max"] = n
	b, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return b
}
