package main

import (
	"strings"
	"testing"
)

func TestUIXMLToTree(t *testing.T) {
	xml := `<hierarchy><node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="x" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,0][1440,3120]">` +
		`<node index="0" text="Wireless debugging" resource-id="android:id/title" class="android.widget.TextView" package="x" content-desc="" checkable="false" checked="false" clickable="false" enabled="true" focusable="false" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[126,1200][900,1280]" />` +
		`<node index="1" text="" resource-id="" class="android.widget.Button" package="x" content-desc="Navigate up" checkable="false" checked="false" clickable="true" enabled="true" focusable="true" focused="false" scrollable="false" long-clickable="false" password="false" selected="false" bounds="[0,165][270,453]" />` +
		`</node></hierarchy>`
	got := uiXMLToTree(xml, 0)
	want := "  Wireless debugging [126,1200,900,1280]\n* Navigate up [0,165,270,453]"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !strings.Contains(uiXMLToTree("<hierarchy/>", 0), "no readable elements") {
		t.Fatal("empty dump should say so")
	}
	if n := strings.Count(uiXMLToTree(xml, 1), "\n"); n != 0 {
		t.Fatalf("max=1 should give one line, got %d newlines", n)
	}
}

func TestTailscaleAndroidLine(t *testing.T) {
	out := "100.109.168.97  pc   user@  windows  -\n100.84.75.31    s24  user@  android  active; direct 1.2.3.4:5\n"
	if m := tsLineRe.FindStringSubmatch(out); m == nil || m[1] != "100.84.75.31" {
		t.Fatalf("android peer not found: %v", m)
	}
}
