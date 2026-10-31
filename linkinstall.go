package main

// install-link: "tokensaver serve" as a Windows service that starts at boot as LocalSystem,
// before anyone signs in, plus the firewall rules that keep Windows from ever asking.

import (
	"fmt"
	"strings"
)

const (
	linkServiceName    = "TokenSaverLink"
	linkServiceDisplay = "TokenSaver Link"
	linkServiceDesc    = "TokenSaver's phone link (port 8766) and its cloudflared tunnel; starts at boot, before sign-in."
	oldLinkTaskName    = "TokenSaver Link" // the old sign-in scheduled task this replaces

	fwRulePort        = "TokenSaver Link"
	fwRuleApp         = "TokenSaver app"
	fwRuleCloudflared = "TokenSaver cloudflared"
)

// linkInstallPlan is everything install-link sets up, worked out without touching the PC.
type linkInstallPlan struct {
	ServiceName string
	DisplayName string
	Description string
	Exe         string
	Args        []string
	Account     string // LocalSystem
	StartType   string // auto: at boot, no sign-in needed
	RemoveTasks []string
	// Firewall holds netsh command lines, run in order: first every rule for these programs and
	// our old rule names is deleted (duplicates included), then exactly one rule per need is added.
	Firewall []string
}

func planLinkInstall(exe, cloudflared string, port int) linkInstallPlan {
	p := linkInstallPlan{
		ServiceName: linkServiceName,
		DisplayName: linkServiceDisplay,
		Description: linkServiceDesc,
		Exe:         exe,
		Args:        []string{"serve"},
		Account:     "LocalSystem",
		StartType:   "auto",
		RemoveTasks: []string{oldLinkTaskName},
	}
	del := func(sel string) {
		p.Firewall = append(p.Firewall, "netsh advfirewall firewall delete rule "+sel)
	}
	add := func(name, dir, what string) {
		p.Firewall = append(p.Firewall, fmt.Sprintf(`netsh advfirewall firewall add rule name="%s" dir=%s action=allow %s enable=yes profile=any`, name, dir, what))
	}
	for _, n := range []string{fwRulePort, fwRulePort + " (Tailscale)", fwRuleApp, fwRuleCloudflared} {
		del(fmt.Sprintf(`name="%s"`, n))
	}
	del(fmt.Sprintf(`name=all program="%s"`, exe))
	if cloudflared != "" {
		del(fmt.Sprintf(`name=all program="%s"`, cloudflared))
	}
	add(fwRulePort, "in", fmt.Sprintf("protocol=TCP localport=%d", port))
	for _, dir := range []string{"in", "out"} {
		add(fwRuleApp, dir, fmt.Sprintf(`program="%s"`, exe))
	}
	if cloudflared != "" {
		for _, dir := range []string{"in", "out"} {
			add(fwRuleCloudflared, dir, fmt.Sprintf(`program="%s"`, cloudflared))
		}
	}
	return p
}

// firewallRuleNames are the rules status checks for.
func firewallRuleNames(withCloudflared bool) []string {
	n := []string{fwRulePort, fwRuleApp}
	if withCloudflared {
		n = append(n, fwRuleCloudflared)
	}
	return n
}

// isServeCommandLine says whether a process command line is a "tokensaver serve".
func isServeCommandLine(cmd string) bool {
	c := strings.ToLower(strings.TrimSpace(cmd))
	i := strings.LastIndex(c, " serve")
	return i > 0 && i+len(" serve") == len(c) && strings.Contains(c[:i], "tokensaver")
}
