//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// --- running as a service ---------------------------------------------------------------------

type linkService struct{}

// runServeService runs serve under the service control manager when Windows started it as a
// service; false means "not a service, run in the console".
func runServeService() bool {
	isSvc, err := svc.IsWindowsService()
	if err != nil || !isSvc {
		return false
	}
	if err := svc.Run(linkServiceName, linkService{}); err != nil {
		openServeLog(false)
		serveLog("service run failed: %v", err)
		os.Exit(1)
	}
	return true
}

func (linkService) Execute(_ []string, req <-chan svc.ChangeRequest, st chan<- svc.Status) (bool, uint32) {
	st <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- serveLinkCtx(ctx, true) }()
	st <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				st <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				st <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel()
				select {
				case <-errc:
				case <-time.After(20 * time.Second):
				}
				return false, 0
			}
		case err := <-errc:
			if errors.Is(err, errServeUpdated) {
				// A non-zero exit makes Windows apply the recovery action (restart in 2 s),
				// which starts the new program; cloudflared already ended with the job.
				return false, 1
			}
			return false, 0
		}
	}
}

// serveSingleInstance takes the machine-wide lock only one serve may hold.
func serveSingleInstance() (func(), error) {
	name, _ := windows.UTF16PtrFromString(`Global\TokenSaverServe`)
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if h != 0 {
			windows.CloseHandle(h)
		}
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, errors.New("lock already held")
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, errors.New("lock held by the service (another account)")
		}
		return nil, err
	}
	return func() { windows.CloseHandle(h) }, nil
}

// --- status -----------------------------------------------------------------------------------

// linkServiceState reads the service state with the rights any user has.
func linkServiceState() (state string, auto bool, account string, installed bool) {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "unknown (" + err.Error() + ")", false, "", false
	}
	defer windows.CloseServiceHandle(m)
	n, _ := windows.UTF16PtrFromString(linkServiceName)
	h, err := windows.OpenService(m, n, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return "not installed", false, "", false
	}
	s := &mgr.Service{Name: linkServiceName, Handle: h}
	defer s.Close()
	state = "unknown"
	if q, err := s.Query(); err == nil {
		state = map[svc.State]string{svc.Stopped: "stopped", svc.StartPending: "starting", svc.StopPending: "stopping",
			svc.Running: "running", svc.ContinuePending: "continuing", svc.PausePending: "pausing", svc.Paused: "paused"}[q.State]
	}
	if c, err := s.Config(); err == nil {
		auto, account = c.StartType == mgr.StartAutomatic, c.ServiceStartName
	}
	return state, auto, account, true
}

// linkListenerPid finds who listens on port. netstat leaves out listening sockets whose TCP
// state Windows reports as 0 (some network filter drivers do that to serve's socket, which is
// why 8766 looked "not listening" while it answered), so the table is read without a state filter.
func linkListenerPid(port int) int {
	if pid := listenerPid(port); pid > 0 {
		return pid
	}
	out, err := runQuiet(15*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("Get-NetTCPConnection -LocalPort %d -ErrorAction SilentlyContinue | Where-Object RemotePort -eq 0 | Select-Object -First 1 -ExpandProperty OwningProcess", port))
	if err != nil {
		return 0
	}
	var pid int
	fmt.Sscan(strings.TrimSpace(out), &pid)
	return pid
}

func oldLinkTaskPresent() bool {
	return quietCmd("schtasks", "/Query", "/TN", oldLinkTaskName).Run() == nil
}

func firewallRulePresent(name string) bool {
	return rawCmd(`netsh advfirewall firewall show rule name="`+name+`"`).Run() == nil
}

func cloudflaredPIDs() []int {
	var pids []int
	for pid, n := range procNames() {
		if strings.EqualFold(n, "cloudflared.exe") {
			pids = append(pids, pid)
		}
	}
	return pids
}

// linkStatusReport: service state, cloudflared count (must be 1), port, tunnel URL, firewall.
func linkStatusReport() string {
	lc := loadLinkCfg()
	var sb strings.Builder
	state, auto, account, installed := linkServiceState()
	if installed {
		start := "manual start"
		if auto {
			start = "starts at boot"
		}
		fmt.Fprintf(&sb, "service %s: %s (%s, %s)\n", linkServiceName, state, start, account)
	} else {
		fmt.Fprintf(&sb, "service %s: not installed (tokensaver install-link)\n", linkServiceName)
	}
	if oldLinkTaskPresent() {
		fmt.Fprintf(&sb, "old sign-in task %q: still present (install-link removes it)\n", oldLinkTaskName)
	}
	pids := cloudflaredPIDs()
	flag := ""
	if len(pids) != 1 && tunnelWanted(lc) {
		flag = " — should be 1"
	}
	fmt.Fprintf(&sb, "cloudflared processes: %d %v%s\n", len(pids), pids, flag)
	healthy := phoneHealth(fmt.Sprintf("http://127.0.0.1:%d", lc.Port), 2*time.Second)
	switch pid := linkListenerPid(lc.Port); {
	case healthy:
		fmt.Fprintf(&sb, "port %d: listening (pid %d), answers /health\n", lc.Port, pid)
	case pid > 0:
		fmt.Fprintf(&sb, "port %d: bound by pid %d but does NOT answer /health (see %s)\n", lc.Port, pid, serveLogPath())
	default:
		fmt.Fprintf(&sb, "port %d: NOT listening (see %s)\n", lc.Port, serveLogPath())
	}
	var fw []string
	for _, n := range firewallRuleNames(lc.CloudflaredExe != "" || cloudflaredExe() != "") {
		mark := "ok"
		if !firewallRulePresent(n) {
			mark = "MISSING"
		}
		fw = append(fw, n+" "+mark)
	}
	sb.WriteString("firewall: " + strings.Join(fw, ", "))
	return sb.String()
}

// rawCmd runs a command line exactly as written (netsh needs its own quoting), hidden.
func rawCmd(line string) *exec.Cmd {
	name := strings.Fields(line)[0]
	c := exec.Command(name)
	c.SysProcAttr = &syscall.SysProcAttr{CmdLine: line, HideWindow: true, CreationFlags: 0x08000000}
	return c
}

// --- install ----------------------------------------------------------------------------------

// consoleInstallLink installs serve as a boot-time LocalSystem service. Not elevated, it hands
// itself to the elevated helper (no prompt), or asks Windows once when the helper isn't there.
func consoleInstallLink() {
	exe := installPath()
	if !exists(exe) {
		exe, _ = os.Executable()
	}
	if !isElevated() {
		recordLinkPaths() // in the user's own context: their PATH and profile
		if pipeUp() {
			ep, err := startElevated(elevRequest{Command: `"` + exe + `" install-link`, Shell: "cmd", Confirmed: true}, os.Stdout)
			if err == nil {
				<-ep.done
				if ep.err != nil {
					fmt.Println("install-link through the elevated helper failed:", ep.err)
				}
				return
			}
			fmt.Println("The elevated helper couldn't run it:", err)
		}
		fmt.Println("Windows will ask once for permission to install the TokenSaver Link service...")
		if err := elevateSelf("install-link"); err != nil {
			fmt.Println("Not installed — the permission prompt was declined or failed:", err)
		}
		return
	}
	if err := installLinkService(exe); err != nil {
		fmt.Println("install-link failed:", err)
		audit("install-link FAILED: %v", err)
		return
	}
	audit("INSTALLED %s service for %s", linkServiceName, exe)
	fmt.Println(linkStatusReport())
}

// recordLinkPaths stores explicit cloudflared/adb/.android paths in link.json, so the service
// (LocalSystem: other PATH, other profile) finds the same programs and adb keys.
func recordLinkPaths() {
	lc := loadLinkCfg()
	changed := false
	if lc.CloudflaredExe == "" || !exists(lc.CloudflaredExe) {
		if p := cloudflaredExe(); p != "" && filepath.IsAbs(p) {
			lc.CloudflaredExe, changed = p, true
		}
	}
	if lc.AdbExe == "" || !exists(lc.AdbExe) {
		if p := adbExe(); filepath.IsAbs(p) && exists(p) {
			lc.AdbExe, changed = p, true
		}
	}
	if lc.AndroidUserHome == "" || !exists(lc.AndroidUserHome) {
		if h, err := os.UserHomeDir(); err == nil && !strings.Contains(strings.ToLower(h), "systemprofile") {
			if a := filepath.Join(h, ".android"); exists(a) {
				lc.AndroidUserHome, changed = a, true
			}
		}
	}
	if changed {
		writeJSONAtomic(linkCfgPath(), lc)
	}
}

func installLinkService(exe string) error {
	recordLinkPaths()
	lc := loadLinkCfg()
	plan := planLinkInstall(exe, lc.CloudflaredExe, lc.Port)

	// 1. The old sign-in task goes, with the serve it started.
	for _, t := range plan.RemoveTasks {
		if quietCmd("schtasks", "/Query", "/TN", t).Run() == nil {
			quietCmd("schtasks", "/End", "/TN", t).Run()
			if out, err := quietCmd("schtasks", "/Delete", "/TN", t, "/F").CombinedOutput(); err != nil {
				return fmt.Errorf("couldn't remove the old task %q: %s", t, strings.TrimSpace(string(out)))
			}
			fmt.Printf("Removed the old sign-in task %q.\n", t)
		}
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("service manager: %v", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(plan.ServiceName)
	have := err == nil
	if have {
		stopServiceWait(s, 30*time.Second)
	}

	// 2. No other serve and no leftover cloudflared may survive.
	if ps, err := procs(); err == nil {
		for _, p := range ps {
			if p.pid != os.Getpid() && isServeCommandLine(p.cmd) {
				killTree(p.pid)
				fmt.Println("Stopped an older tokensaver serve, pid", p.pid)
			}
		}
	}
	if n := killTunnelOrphans(lc.Port); n > 0 {
		fmt.Printf("Stopped %d leftover cloudflared process(es).\n", n)
	}

	// 3. Firewall: delete every rule for these programs and our old names, then add one each.
	for _, line := range plan.Firewall {
		out, err := rawCmd(line).CombinedOutput()
		if err != nil && strings.Contains(line, " add rule ") {
			return fmt.Errorf("firewall: %s: %s", line, strings.TrimSpace(string(out)))
		}
	}
	fmt.Println("Firewall rules set for", exe, "and", orDash(lc.CloudflaredExe))

	// 4. The service: boot start, LocalSystem, restart on failure (and on update exits).
	conf := mgr.Config{
		ServiceType:      windows.SERVICE_WIN32_OWN_PROCESS,
		StartType:        mgr.StartAutomatic,
		ErrorControl:     mgr.ErrorNormal,
		DisplayName:      plan.DisplayName,
		Description:      plan.Description,
		ServiceStartName: plan.Account,
	}
	if have {
		conf.BinaryPathName = syscall.EscapeArg(plan.Exe) + " " + strings.Join(plan.Args, " ")
		if err := s.UpdateConfig(conf); err != nil {
			s.Close()
			return fmt.Errorf("update service: %v", err)
		}
	} else {
		s, err = m.CreateService(plan.ServiceName, plan.Exe, conf, plan.Args...)
		if err != nil {
			return fmt.Errorf("create service: %v", err)
		}
	}
	defer s.Close()
	acts := []mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 2 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}
	if err := s.SetRecoveryActions(acts, 3600); err != nil {
		return fmt.Errorf("recovery actions: %v", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("recovery on exit codes: %v", err)
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start service: %v", err)
	}
	fmt.Printf("Service %q installed: starts at boot as LocalSystem, no sign-in needed.\n", plan.ServiceName)

	// 5. Wait for the port, then (when wanted) the tunnel.
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) && !phoneHealth(fmt.Sprintf("http://127.0.0.1:%d", lc.Port), time.Second) {
		time.Sleep(time.Second)
	}
	for time.Now().Before(deadline) && tunnelWanted(lc) {
		if st := loadTunnelState(); st.State == "up" || st.State == "failed" || st.State == "missing" {
			break
		}
		time.Sleep(time.Second)
	}
	return nil
}

func stopServiceWait(s *mgr.Service, max time.Duration) {
	if q, err := s.Query(); err != nil || q.State == svc.Stopped {
		return
	}
	s.Control(svc.Stop)
	for end := time.Now().Add(max); time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
		if q, err := s.Query(); err != nil || q.State == svc.Stopped {
			return
		}
	}
}
