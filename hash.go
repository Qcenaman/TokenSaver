package main

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"strings"
)

// readHash (read mode:hash) returns sha256, sha1 and md5 of a file; with
// pattern set to an expected hash it also says MATCH or MISMATCH, so files
// can be verified without pulling them into the chat.
func readHash(a readArgs) (string, error) {
	f, err := os.Open(a.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%s is a folder; hash one file (use find for many)", a.Path)
	}
	h256, h1, h5 := sha256.New(), sha1.New(), md5.New()
	if _, err := io.Copy(io.MultiWriter(h256, h1, h5), f); err != nil {
		return "", err
	}
	sum := func(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }
	s256, s1, s5 := sum(h256), sum(h1), sum(h5)
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\nsha256 %s\nsha1   %s\nmd5    %s\n", a.Path, hc(st.Size()), s256, s1, s5)
	if want := strings.ToLower(strings.TrimSpace(a.Pattern)); want != "" {
		want = strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(want, "sha256:"), "sha1:"), "md5:")
		switch want {
		case s256, s1, s5:
			b.WriteString("MATCH: file hash equals the expected value")
		default:
			b.WriteString("MISMATCH: expected " + want)
		}
	}
	return b.String(), nil
}
