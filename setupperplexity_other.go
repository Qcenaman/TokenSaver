//go:build !windows

package main

import "fmt"

// setupPerplexityMain: the Perplexity setup installer is Windows-only.
func setupPerplexityMain(args []string) int {
	fmt.Println("The Perplexity setup is for Windows.")
	return 2
}
