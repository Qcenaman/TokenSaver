package main

// rvAIGate runs TokenSaver's GPU reminder before the review's local-AI pass loads a model;
// it returns the reminder when something else holds the GPU, "" to go ahead.
func rvAIGate(gpu string, force bool, req *AIRequest) string {
	if rem, _ := gpuGate("The local AI", gpuMode(gpu, force), aiWouldLoad()); rem != "" {
		return rem
	}
	req.GPU = "continue"
	return ""
}
