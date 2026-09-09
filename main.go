// skillsync: team skill directory with auto-sync across agent tools.
// See SPEC.md for the decision record this implements.
//
// Layout, one concern per file:
//
//	main.go       command dispatch
//	config.go     ~/.skillsync/config.json, paths, fatal
//	state.go      ~/.skillsync/state.json: installs, health, exposure, update
//	source.go     git sources, skill discovery, precedence
//	manifest.go   skillsync.yaml, the per-project skill list
//	builtin.go    the embedded skillsync skill
//	skilldir.go   SKILL.md frontmatter, directory hashing and copying
//	adapters.go   per-tool install locations and the drift/adopt table
//	sync.go       the sync itself: global scope, projects, removal
//	exposure.go   what Claude sees: skillOverrides, usage rules, budget check
//	hub.go        generated router skills for large on-demand families
//	commands.go   init, add/remove, adopt, list, uninstall
//	health.go     auto on/off/status, check, failure surfacing
//	daemon.go     launchd / systemd / Task Scheduler registration
//	hook.go       Claude Code settings.json hooks
//	metrics.go    usage events and the optional HTTPS sink
//	upgrade.go    version, self-upgrade, update notice
//	lock_*.go     one sync at a time
package main

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  skillsync init <git-url>            set up this machine: sync now, then keep syncing on its own
      --tools claude-code,cursor,codex  which agent tools to install into (default: claude-code)
      --no-daemon / --no-hooks          escape hatches for CI or locked-down machines: skills then
                                        go stale until you run "skillsync sync" yourself
  skillsync sync                      sync now
  skillsync list                      skills, what Claude sees of each, when each changed, and who changed it
  skillsync check                     warn if syncs fail or Claude's skill listing is over budget
  skillsync add|remove <skill>        manage your global subscriptions
  skillsync adopt <skill>             replace a hand-installed skill with the managed version
  skillsync stats                     how often each skill gets used
  skillsync auto on|off|status        automatic updating: background job + Claude Code hooks
  skillsync upgrade                   replace this binary with the latest release
  skillsync version                   print the installed version
  skillsync uninstall [--keep-skills] remove skillsync and everything it installed
`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	arg := func(i int) string {
		if len(os.Args) <= i {
			usage()
		}
		return os.Args[i]
	}
	hasFlag := func(name string) bool { return slices.Contains(os.Args, name) }
	// flagValue accepts both `--tools cursor` and `--tools=cursor`.
	flagValue := func(name string) string {
		for i, a := range os.Args {
			if a == name && i+1 < len(os.Args) {
				return os.Args[i+1]
			}
			if v, ok := strings.CutPrefix(a, name+"="); ok {
				return v
			}
		}
		return ""
	}
	switch os.Args[1] {
	case "init":
		tools, err := parseTools(flagValue("--tools"))
		if err != nil {
			fatal("%v", err)
		}
		cmdInit(arg(2), tools, !hasFlag("--no-daemon"), !hasFlag("--no-hooks"))
	case "sync", "sync-all":
		// sync-all is kept as an alias: schedulers installed by earlier
		// versions invoke it by name.
		cmdSync()
	case "auto":
		cmdAuto(arg(2))
	case "check":
		cmdCheck() // fast, offline; the session hook calls it to surface breakage
	case "list":
		cmdList()
	case "add":
		cmdSubscribe(arg(2), true)
	case "remove":
		cmdSubscribe(arg(2), false)
	case "adopt":
		cmdAdopt(arg(2))
	case "uninstall":
		cmdUninstall(len(os.Args) > 2 && os.Args[2] == "--keep-skills")
	case "daemon":
		cmdDaemon(arg(2))
	case "hook":
		cmdHook(arg(2))
	case "track":
		cmdTrack(os.Args[2:])
	case "stats":
		cmdStats()
	case "upgrade":
		cmdUpgrade()
	case "version", "--version", "-v":
		cmdVersion()
	default:
		usage()
	}
}
