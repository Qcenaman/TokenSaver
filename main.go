package main

import (
	"fmt"
	"os"
)

const appVersion = "1.2.0"

func main() {
	initApp()
	args := os.Args[1:]
	if len(args) == 0 {
		consoleMenu()
		return
	}
	switch args[0] {
	case "mcp":
		serveMCP(os.Stdin, os.Stdout)
	case "run":
		if len(args) < 2 {
			fmt.Println(`usage: claudessistent run "<saved job name>"`)
			os.Exit(2)
		}
		os.Exit(consoleRunSaved(args[1]))
	case "install":
		consoleInstall()
	case "uninstall":
		consoleUninstall()
	case "version":
		fmt.Println("claudessistent", appVersion)
	default:
		fmt.Println("unknown command:", args[0])
		os.Exit(2)
	}
}
