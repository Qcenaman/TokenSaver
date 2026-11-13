package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const appVersion = "2.9.0"

func main() {
	initApp()
	args := commandArgs(os.Args[1:])
	if len(args) == 0 {
		// TokenSaver-Setup.exe, double-clicked: the Perplexity setup for a new PC
		if exe, err := os.Executable(); err == nil && strings.Contains(strings.ToLower(filepath.Base(exe)), "setup") {
			os.Exit(setupPerplexityMain(nil))
		}
		consoleMenu()
		return
	}
	switch args[0] {
	case "setup-perplexity":
		os.Exit(setupPerplexityMain(args[1:]))
	case "mcp":
		for i := 1; i+1 < len(args); i++ {
			if args[i] == "--for" {
				setProfile(args[i+1])
			}
		}
		serveMCP(os.Stdin, os.Stdout)
	case "start":
		if len(args) > 1 {
			os.Exit(consoleStartModel(args[1]))
		}
		os.Exit(consoleStartLocalAI())
	case "stop":
		os.Exit(consoleStopLocalAI())
	case "model":
		os.Exit(consoleModel(args[1:]))
	case "approvals":
		os.Exit(consoleApprovals(args[1:]))
	case "chat":
		os.Exit(consoleChat(args[1:]))
	case "local-setup":
		consoleLocalSetup()
		waitEnter("Press Enter to close.", 0)
	case "run":
		if len(args) < 2 {
			fmt.Println("usage: " + appName() + ` run "<saved job name>"`)
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
	case "ai-watch":
		os.Exit(aiWatchMain())
	case "serve":
		serveLink()
	case "install-link":
		consoleInstallLink()
	case "phone":
		os.Exit(phoneCLI(args[1:]))
	case "connector":
		os.Exit(connectorCLI(args[1:]))
	case "chats":
		os.Exit(chatsCLI(args[1:]))
	case "setup":
		if localMode {
			consoleLocalSetup()
			waitEnter("Press Enter to close.", 0)
			return
		}
		consoleSetup()
	case "refresh-helper":
		refreshHelper()
	case "version":
		fmt.Println(appName(), appVersion, "("+runtime.GOOS+")")
	case "restart-claude":
		os.Exit(restartClaudeNow())
	case "autoyes":
		os.Exit(autoYesCLI(args[1:]))
	case "project":
		os.Exit(projectCLI(args[1:]))
	case "user-worker":
		os.Exit(userWorkerMain(args[1:]))
	case "terminal":
		terminal()
	case "macro":
		os.Exit(runMacroCLI(args[1:]))
	default:
		fmt.Println("unknown command:", args[0])
		os.Exit(2)
	}
}
