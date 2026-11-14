package main

// devices.json: other Android devices (tablet, TV, …) that hold this PC's link and report in,
// kept apart from phone.json so they never overwrite the paired phone's addresses (the PC then
// sent the phone's token to the TV, which refused it: "the phone rejected the token").

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type otherDev struct {
	LanIP       string    `json:"lan_ip,omitempty"`
	TSIP        string    `json:"tailscale_ip,omitempty"`
	AdbTCP      string    `json:"adb_tcp,omitempty"`
	LastReport  time.Time `json:"last_report"`
	LastNetwork string    `json:"last_network,omitempty"`
	LastBattery string    `json:"last_battery,omitempty"`
}

var devMu sync.Mutex

func devicesPath() string { return filepath.Join(appHome, "devices.json") }

// otherDevice tells whether a report comes from a device other than the paired phone: it names
// addresses and none of them is the phone's, or it arrived over Tailscale from another device.
// from is the address the connection came from directly (a request through the public tunnel or
// Funnel arrives from this PC itself, so it never counts).
func otherDevice(pc phoneConfig, lan, ts, from string) bool {
	if pc.LanIP == "" && pc.TSIP == "" {
		return false // nothing paired yet: the reporter is the phone
	}
	if pc.TSIP != "" && isTailnetIP(from) && from != pc.TSIP {
		return true // sent over Tailscale by another device, whatever it reports
	}
	if lan == "" && ts == "" {
		return false
	}
	if ts != "" && ts == pc.TSIP {
		return false
	}
	if lan != "" && lan == pc.LanIP && (ts == "" || pc.TSIP == "") {
		return false
	}
	return true
}

func loadOtherDevices() map[string]otherDev {
	m := map[string]otherDev{}
	if b, err := os.ReadFile(devicesPath()); err == nil {
		json.Unmarshal(b, &m)
	}
	return m
}

func saveOtherDevice(lan, ts, adbTCP, network, battery string) {
	devMu.Lock()
	defer devMu.Unlock()
	key := ts
	if key == "" {
		key = lan
	}
	m := loadOtherDevices()
	m[key] = otherDev{LanIP: lan, TSIP: ts, AdbTCP: adbTCP, LastReport: time.Now(), LastNetwork: network, LastBattery: battery}
	writeJSONAtomic(devicesPath(), m)
}

// otherDevicesStatus lists the other devices for phone status.
func otherDevicesStatus() string {
	m := loadOtherDevices()
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString("other devices with this PC's link (not the phone):\n")
	for _, k := range keys {
		d := m[k]
		fmt.Fprintf(&b, "  lan=%s tailscale=%s, last report %s\n", orDash(d.LanIP), orDash(d.TSIP), d.LastReport.Format("2006-01-02 15:04"))
	}
	return b.String()
}
