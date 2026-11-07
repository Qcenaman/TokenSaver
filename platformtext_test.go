package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every Windows phrase that gets swapped on Linux must still be in the texts,
// or the Linux build would silently describe Windows.
func TestLinuxTextPairsStillMatch(t *testing.T) {
	var all strings.Builder
	all.WriteString(serverInstructions)
	for _, d := range toolDefs {
		all.Write(d)
	}
	for i := 0; i < len(linuxTextPairs); i += 2 {
		if !strings.Contains(all.String(), linuxTextPairs[i]) {
			t.Errorf("phrase %q is no longer in the tool texts; update linuxTextPairs", linuxTextPairs[i])
		}
	}
	for i, d := range platformToolDefs() {
		if !json.Valid(d) {
			t.Errorf("platform tool %d is not valid JSON", i)
		}
	}
}
