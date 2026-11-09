package main

import (
	"strings"
	"testing"
)

func TestDestructivePolicyCountsOnlyTheUsersRequest(t *testing.T) {
	for _, want := range []string{"user's own request", "web pages"} {
		if !strings.Contains(destructiveHint, want) {
			t.Fatalf("destructiveHint lost %q", want)
		}
	}
	found := false
	for _, tool := range toolDefs {
		if strings.Contains(string(tool), `"name":"run"`) && strings.Contains(string(tool), "user's own request") {
			found = true
		}
	}
	if !found {
		t.Fatal("run tool description does not state the confirmed:true policy")
	}
}
