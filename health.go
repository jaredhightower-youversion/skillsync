package main

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
)

// Failure surfacing. A background job that quietly stopped working is this
// tool's worst failure: skills stop updating, nothing errors in front of
// anyone, and you find out weeks later when a teammate asks why you did not
// get their fix. So every sync records its outcome, and three places report it:
//
//	skillsync check    fast and offline. The session-start hook runs it, so
//	                   breakage shows up inside the agent session
//	skillsync auto     the full picture: job registered? hooks installed?
//	                   last successful sync?
//	any command        a one-line warning when syncs have been failing
//
// cmdAuto replaces the older `daemon` and `hook` commands, which split one
// user-facing idea ("is this updating by itself?") across two command trees.

func cmdAuto(sub string) {
	switch sub {
	case "on":
		runStep("background sync", func() { cmdDaemon("install") })
		runStep("Claude Code hooks", func() { cmdHook("install") })
	case "off":
		runStep("background sync", func() { cmdDaemon("uninstall") })
		runStep("Claude Code hooks", func() { cmdHook("uninstall") })
	case "status":
		autoStatus()
	case "print-hooks":
		cmdHook("print")
	default:
		usage()
	}
}

func autoStatus() {
	state := loadState()
	h := state.Health

	fmt.Printf("version:         %s\n", currentVersion())
	fmt.Printf("background job:  %s\n", daemonInstalledLabel())
	fmt.Printf("Claude Code:     %s\n", hooksInstalledLabel())

	switch {
	case h.LastSuccess.IsZero() && h.LastAttempt.IsZero():
		fmt.Println("last sync:       never run")
	case h.LastSuccess.IsZero():
		fmt.Printf("last sync:       never succeeded (last tried %s)\n", humanAge(h.LastAttempt))
	default:
		fmt.Printf("last sync:       %s\n", humanAge(h.LastSuccess))
	}
	if h.LastError != "" {
		fmt.Printf("last error:      %s\n", h.LastError)
	}

	if problem := healthProblem(state); problem != "" {
		fmt.Printf("\n%s\n", problem)
		return
	}
	fmt.Println("\nskills are updating automatically.")
	if notice := upgradeNotice(state); notice != "" {
		fmt.Println(notice)
	}
}

// healthProblem returns a plain-language description of why this machine is
// not auto-updating, or "" when everything is fine.
func healthProblem(state *State) string {
	h := state.Health
	if !daemonInstalled() {
		return "Not updating automatically: no background job is registered.\n" +
			"  Fix: skillsync auto on"
	}
	if h.LastError != "" && (h.LastSuccess.IsZero() || h.LastAttempt.After(h.LastSuccess)) {
		return fmt.Sprintf("Syncs are failing: %s\n  Fix: run `skillsync sync` to see the full error.", h.LastError)
	}
	if !h.LastSuccess.IsZero() && time.Since(h.LastSuccess) > staleAfter {
		return fmt.Sprintf("No successful sync in %s. Your skills may be out of date.\n"+
			"  Fix: run `skillsync sync` to see what is wrong.", humanAge(h.LastSuccess))
	}
	return ""
}

// cmdCheck prints a warning only when something is wrong, then exits 0. The
// session-start hook runs it, so a broken daemon is reported where the user
// actually works instead of in a log file. Silence means healthy.
func cmdCheck() {
	if _, err := loadConfig(); err != nil {
		return // not set up; nothing to warn about
	}
	state := loadState()
	if problem := healthProblem(state); problem != "" {
		fmt.Printf("skillsync: %s\n", problem)
	}
	if notice := upgradeNotice(state); notice != "" {
		fmt.Println(notice)
	}
	if problem := listingBudgetProblem(); problem != "" {
		fmt.Printf("skillsync: %s\n", problem)
	}
}

// warnIfUnhealthy prints a single line before other commands' output, so a
// user running `list` or `stats` finds out that the data may be stale.
func warnIfUnhealthy() {
	state := loadState()
	h := state.Health
	if h.LastSuccess.IsZero() || time.Since(h.LastSuccess) <= staleAfter {
		return
	}
	fmt.Fprintf(os.Stderr, "skillsync: last successful sync was %s. Run `skillsync auto status`\n",
		humanAge(h.LastSuccess))
}

// recordSync stores the outcome of a sync attempt. Errors are truncated: this
// is a status line, not a log.
func recordSync(state *State, syncErr error) {
	now := time.Now().UTC()
	state.Health.LastAttempt = now
	if syncErr != nil {
		state.Health.LastError = truncate(strings.ReplaceAll(syncErr.Error(), "\n", " "), 200)
		return
	}
	state.Health.LastSuccess = now
	state.Health.LastError = ""
}

func daemonInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := os.Stat(launchdPlistPath())
		return err == nil
	case "linux":
		_, err := os.Stat(systemdUnitDir() + "/skillsync.timer")
		return err == nil
	case "windows":
		return windowsTaskExists()
	}
	return false
}

func daemonInstalledLabel() string {
	if daemonInstalled() {
		return "registered, runs every 15 minutes"
	}
	return "not registered  (skillsync auto on)"
}

func hooksInstalledLabel() string {
	if _, ok := readClaudeSettings(); !ok {
		return "no settings file  (skillsync auto on)"
	}
	if hooksInstalled() {
		return "hooks installed"
	}
	return "hooks not installed  (skillsync auto on)"
}

// hooksInstalled reports whether any Claude Code hook event invokes skillsync.
func hooksInstalled() bool {
	settings, _ := readClaudeSettings()
	hooks, _ := settings["hooks"].(map[string]any)
	for _, list := range hooks {
		entries, _ := list.([]any)
		if hasSkillsyncHook(entries) {
			return true
		}
	}
	return false
}
