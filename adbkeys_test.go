package main

import (
	"os"
	"strings"
	"testing"
)

func TestMergeKeyListKeepsExistingKeys(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := mergeKeyList("a"+sep[:0]+"key", "other"+sep+"AKEY"+sep+"akey"+sep)
	parts := strings.Split(got, sep)
	if parts[0] != "akey" {
		t.Fatalf("own key must come first, got %q", got)
	}
	if len(parts) != 2 || parts[1] != "other" {
		t.Fatalf("want own key plus 'other' once, got %q", got)
	}
}

func TestMergeKeyListEmptyExisting(t *testing.T) {
	if got := mergeKeyList("k", ""); got != "k" {
		t.Fatalf("got %q", got)
	}
}
