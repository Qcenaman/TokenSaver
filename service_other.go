//go:build !windows

package main

import (
	"fmt"
	"os"
	"time"
)

func runServeService() bool { return false }

func serveSingleInstance() (func(), error) { return func() {}, nil }

func consoleInstallLink() {
	exe, _ := os.Executable()
	fmt.Println("Start it with:", exe, "serve")
}

func linkStatusReport() string {
	lc := loadLinkCfg()
	if phoneHealth(fmt.Sprintf("http://127.0.0.1:%d", lc.Port), time.Second) {
		return fmt.Sprintf("port %d: listening", lc.Port)
	}
	return fmt.Sprintf("port %d: NOT listening (see %s)", lc.Port, serveLogPath())
}
