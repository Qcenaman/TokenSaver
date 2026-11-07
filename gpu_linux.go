//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

var gpuVendors = map[string]string{"0x1002": "AMD", "0x10de": "NVIDIA", "0x8086": "Intel"}

func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func readInt(p string) (int64, bool) {
	v, err := strconv.ParseInt(readTrim(p), 10, 64)
	return v, err == nil
}

// otherGPUInfo describes AMD and Intel GPUs (and NVIDIA without nvidia-smi) from /sys:
// vendor, driver, VRAM (and GTT for integrated AMD), load and temperature.
func otherGPUInfo() string {
	cards, _ := filepath.Glob("/sys/class/drm/card[0-9]*")
	var out []string
	seen := map[string]bool{}
	for _, c := range cards {
		if strings.Contains(filepath.Base(c), "-") { // connectors like card1-eDP-1
			continue
		}
		dev, err := filepath.EvalSymlinks(filepath.Join(c, "device"))
		if err != nil || seen[dev] {
			continue
		}
		seen[dev] = true
		vendor := gpuVendors[readTrim(filepath.Join(dev, "vendor"))]
		if vendor == "" {
			continue
		}
		name := vendor + " GPU " + strings.TrimPrefix(readTrim(filepath.Join(dev, "device")), "0x")
		if pn := readTrim(filepath.Join(dev, "product_name")); pn != "" {
			name = pn
		}
		parts := []string{name}
		if drv, err := os.Readlink(filepath.Join(dev, "driver")); err == nil {
			parts = append(parts, "driver "+filepath.Base(drv))
		}
		if tot, ok := readInt(filepath.Join(dev, "mem_info_vram_total")); ok && tot > 0 {
			used, _ := readInt(filepath.Join(dev, "mem_info_vram_used"))
			s := fmt.Sprintf("%s VRAM (%s used)", hb(tot), hb(used))
			if gtt, ok := readInt(filepath.Join(dev, "mem_info_gtt_total")); ok && gtt > 0 {
				s += fmt.Sprintf(", %s shared", hb(gtt))
			}
			parts = append(parts, s)
		}
		if busy, ok := readInt(filepath.Join(dev, "gpu_busy_percent")); ok {
			parts = append(parts, fmt.Sprintf("%d%% busy", busy))
		}
		if temps, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*", "temp1_input")); len(temps) > 0 {
			if t, ok := readInt(temps[0]); ok {
				parts = append(parts, fmt.Sprintf("%d°C", t/1000))
			}
		}
		out = append(out, strings.Join(parts, ", "))
	}
	return strings.Join(out, " | ")
}
