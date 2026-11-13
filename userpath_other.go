//go:build !windows

package main

// addToUserPath is Windows-only; elsewhere the installer leaves PATH alone.
func addToUserPath(dir string) (bool, error) { return false, nil }
