//go:build linux

package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func TestKeyMapsCoverKeyNames(t *testing.T) {
	for name, vk := range keyNames {
		if _, ok := vkKeysym[vk]; !ok {
			t.Errorf("no X keysym for %q (0x%02X)", name, vk)
		}
		if _, ok := vkEvdev[vk]; !ok && vk < 0x7C { // F13+ have codes too, media keys checked above
			t.Errorf("no evdev code for %q (0x%02X)", name, vk)
		}
	}
}

func grayPNG(w, h int) []byte {
	img := image.NewGray(image.Rect(0, 0, w, h))
	img.Set(1, 1, color.Gray{Y: 200})
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes()
}

func TestRGBAPNGAndCrop(t *testing.T) {
	out, err := rgbaPNG(grayPNG(4, 3))
	if err != nil {
		t.Fatal(err)
	}
	img, _ := png.Decode(bytes.NewReader(out))
	if _, ok := img.(*image.RGBA); !ok {
		t.Fatalf("got %T, want *image.RGBA", img)
	}
	c, err := cropPNG(out, 1, 1, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	ci, _ := png.Decode(bytes.NewReader(c))
	if ci.Bounds().Dx() != 2 || ci.Bounds().Dy() != 2 {
		t.Fatal("crop size wrong")
	}
	px, err := pngToBGRA(out, 4, 3)
	if err != nil || len(px) != 4*3*4 || px[(1*4+1)*4] != 200 {
		t.Fatalf("BGRA pixels wrong: %v", err)
	}
}

func TestAtspiHandles(t *testing.T) {
	h := atspiHandle(4242, 7)
	if !isAtspiHandle(h) || isAtspiHandle(uintptr(0x0080000c)) {
		t.Fatal("handle kinds mixed up")
	}
}

func TestAtspiScriptHasModes(t *testing.T) {
	for _, m := range []string{"'list'", "'text'", "'do'", "RECT", "OK set", "OK invoked"} {
		if !strings.Contains(atspiScript, m) {
			t.Errorf("script lacks %s", m)
		}
	}
}
