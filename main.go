package main

import (
	"fmt"
	"os"
)

const appVersion = "1.18.2"

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
			fmt.Println(`usage: tokensaver run "<saved job name>"`)
			os.Exit(2)
		}
		os.Exit(consoleRunSaved(args[1]))
	case "install":
		consoleInstall()
	case "uninstall":
		consoleUninstall()
	case "helper":
		os.Exit(serveElevatedHelper())
	case "install-helper":
		consoleInstallHelper()
	case "uninstall-helper":
		consoleUninstallHelper()
	case "serve":
		serveLink()
	case "install-link":
		consoleInstallLink()
	case "phone":
		os.Exit(phoneCLI(args[1:]))
	case "version":
		fmt.Println("tokensaver", appVersion)
	case "terminal":
		terminal()
	case "macro":
		os.Exit(runMacroCLI(args[1:]))
	default:
		fmt.Println("unknown command:", args[0])
		os.Exit(2)
	}
}
