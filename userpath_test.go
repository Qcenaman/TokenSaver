//go:build windows

package main

import "testing"

func TestPathListHas(t *testing.T) {
	list := `C:\Windows;"D:\Claude Apps\TokenSaver\app\";%USERPROFILE%\bin`
	if !pathListHas(list, `d:\claude apps\tokensaver\app`) {
		t.Error("existing entry (quoted, trailing backslash, other case) not found")
	}
	if pathListHas(list, `D:\Claude Apps\TokenSaver`) || pathListHas("", `C:\x`) {
		t.Error("found an entry that is not there")
	}
}
