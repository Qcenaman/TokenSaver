//go:build windows

package main

import (
	"os"
	"time"
)

// startAIWatcher starts "tokensaver ai-watch" through WMI: not a child of this process and
// outside Claude's job, so the idle shutdown still happens after Claude has quit.
func startAIWatcher() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	ps := "$s = New-CimInstance -ClassName Win32_ProcessStartup -ClientOnly -Property @{ShowWindow=[uint16]0}; " +
		"$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine=" +
		psSingle(`"`+exe+`" ai-watch`) + "; ProcessStartupInformation=$s}; " +
		"if ($r.ReturnValue -ne 0) { throw \"WMI returned $($r.ReturnValue)\" }"
	runQuiet(30*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
}
