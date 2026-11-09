package main

import (
	"encoding/json"
	"strings"
	"sync"
)

// The tool texts are written for Windows; on Linux these phrases are swapped so
// Claude gets the right shell, trash and folder names.
var linuxTextPairs = []string{
	"this Windows PC", "this Linux PC (paths like /home/user/..., commands run in bash)",
	"(Recycle Bin only)", "(Trash only)",
	"(cmd or powershell)", "(bash; shell pwsh if installed)",
	"elevated:true runs as admin without UAC; destructive admin commands (deleting, registry, services, disks, firewall...)",
	"elevated:true runs as root through the admin helper without a password prompt; destructive admin commands (deleting, packages, services, disks, partitions, firewall, reboot...)",
	"info: Windows, CPU, RAM, free space per drive", "info: Linux distro, CPU, RAM, free space per disk",
	`D:\\Claude Chats`, "~/Claude Chats",
}

var linuxText = strings.NewReplacer(linuxTextPairs...)

func platformText(s string) string {
	if isWin {
		return s
	}
	return linuxText.Replace(s)
}

var platformDefs = sync.OnceValue(func() []json.RawMessage {
	if isWin {
		return annotateDefs(toolDefs)
	}
	out := make([]json.RawMessage, len(toolDefs))
	for i, d := range toolDefs {
		out[i] = annotateDef(json.RawMessage(platformText(string(d))))
	}
	return out
})

func platformToolDefs() []json.RawMessage { return platformDefs() }

func binName() string {
	if isWin {
		return "Recycle Bin"
	}
	return "Trash"
}
