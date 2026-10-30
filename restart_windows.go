//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

func psSingle(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// restartClaude quits Claude desktop and opens it again, so it loads the installed
// TokenSaver. Claude ends its MCP servers (this process) when it quits, so the work goes
// to a new process created through WMI: not a child of this one, outside Claude's job,
// so it survives the quit. This returns at once; the restart follows a few seconds later.
func restartClaude() (string, error) {
	if !claudeRunning() {
		return "", errors.New("claude desktop isn't running")
	}
	exe := installPath()
	if !exists(exe) {
		exe, _ = os.Executable()
	}
	ps := "$s = New-CimInstance -ClassName Win32_ProcessStartup -ClientOnly -Property @{ShowWindow=[uint16]0}; " +
		"$r = Invoke-CimMethod -ClassName Win32_Process -MethodName Create -Arguments @{CommandLine=" +
		psSingle(`"`+exe+`" restart-claude`) + "; ProcessStartupInformation=$s}; " +
		"if ($r.ReturnValue -ne 0) { throw \"WMI returned $($r.ReturnValue)\" }"
	if out, err := runQuiet(30*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps); err != nil {
		return "", fmt.Errorf("couldn't start the restart: %v %s", err, out)
	}
	v := installedVersion()
	if v == "" {
		v = appVersion
	}
	return "Claude desktop quits and reopens in a few seconds with TokenSaver " + v + ". This chat's tools end with it: carry on in a new chat once it is open.", nil
}

// restartClaudeNow is the detached side: it waits a moment for the tool result to reach
// Claude, quits it, waits until it is gone and starts it again the way the Start menu
// does (Store app) or from its program path.
func restartClaudeNow() int {
	procFreeConsole.Call()
	ps := `
$p = Get-CimInstance Win32_Process -Filter "name='claude.exe'" | Select-Object -First 1 -ExpandProperty ExecutablePath
Start-Sleep -Seconds 3
Stop-Process -Name claude -Force -ErrorAction SilentlyContinue
$i = 0
while ((Get-Process claude -ErrorAction SilentlyContinue) -and $i -lt 40) { Start-Sleep -Milliseconds 500; $i++ }
Start-Sleep -Seconds 2
$app = (Get-StartApps | Where-Object { $_.Name -eq 'Claude' } | Select-Object -First 1).AppID
if ($app) { Start-Process explorer.exe "shell:AppsFolder\$app" } elseif ($p) { Start-Process $p } else { exit 3 }
`
	if _, err := runQuiet(3*time.Minute, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps); err != nil {
		return 1
	}
	return 0
}