package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// The daemon is an OS-scheduled `skillsync sync-all` every 15 minutes
// (SPEC §2). macOS: launchd user agent. Linux: systemd user timer.
// Windows: not yet supported (SPEC open item).

const launchdLabel = "com.skillsync.sync"

func launchdPlistPath() string {
	return filepath.Join(homeDir(), "Library", "LaunchAgents", launchdLabel+".plist")
}

func systemdUnitDir() string {
	return filepath.Join(homeDir(), ".config", "systemd", "user")
}

func cmdDaemon(sub string) {
	switch runtime.GOOS {
	case "darwin":
		daemonDarwin(sub)
	case "linux":
		daemonLinux(sub)
	default:
		fatal("daemon not supported on %s yet — schedule `skillsync sync-all` with your OS scheduler", runtime.GOOS)
	}
}

func daemonDarwin(sub string) {
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key><array>
    <string>%s</string><string>sync-all</string>
  </array>
  <key>StartInterval</key><integer>900</integer>
  <key>RunAtLoad</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, launchdLabel, binaryPath(),
		filepath.Join(stateDir(), "daemon.log"), filepath.Join(stateDir(), "daemon.log"))

	switch sub {
	case "install":
		if err := os.MkdirAll(filepath.Dir(launchdPlistPath()), 0o755); err != nil {
			fatal("%v", err)
		}
		if err := os.WriteFile(launchdPlistPath(), []byte(plist), 0o644); err != nil {
			fatal("write plist: %v", err)
		}
		exec.Command("launchctl", "unload", launchdPlistPath()).Run() // idempotent reinstall
		if out, err := exec.Command("launchctl", "load", launchdPlistPath()).CombinedOutput(); err != nil {
			fatal("launchctl load: %v: %s", err, out)
		}
		fmt.Println("daemon installed: sync-all every 15m + at login (launchd)")
	case "uninstall":
		exec.Command("launchctl", "unload", launchdPlistPath()).Run()
		os.Remove(launchdPlistPath())
		fmt.Println("daemon uninstalled")
	case "status":
		out, err := exec.Command("launchctl", "list", launchdLabel).CombinedOutput()
		if err != nil {
			fmt.Println("daemon: not installed")
			return
		}
		fmt.Printf("daemon: installed\n%s", out)
	default:
		usage()
	}
}

func daemonLinux(sub string) {
	service := fmt.Sprintf(`[Unit]
Description=skillsync background sync
After=network-online.target

[Service]
Type=oneshot
ExecStart=%s sync-all
`, binaryPath())
	timer := `[Unit]
Description=skillsync sync every 15 minutes

[Timer]
OnBootSec=1min
OnUnitActiveSec=15min

[Install]
WantedBy=timers.target
`
	svcPath := filepath.Join(systemdUnitDir(), "skillsync.service")
	timerPath := filepath.Join(systemdUnitDir(), "skillsync.timer")

	switch sub {
	case "install":
		if err := os.MkdirAll(systemdUnitDir(), 0o755); err != nil {
			fatal("%v", err)
		}
		if err := os.WriteFile(svcPath, []byte(service), 0o644); err != nil {
			fatal("%v", err)
		}
		if err := os.WriteFile(timerPath, []byte(timer), 0o644); err != nil {
			fatal("%v", err)
		}
		for _, args := range [][]string{
			{"daemon-reload"}, {"enable", "--now", "skillsync.timer"},
		} {
			if out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput(); err != nil {
				fatal("systemctl --user %v: %v: %s", args, err, out)
			}
		}
		fmt.Println("daemon installed: sync-all every 15m (systemd user timer)")
	case "uninstall":
		exec.Command("systemctl", "--user", "disable", "--now", "skillsync.timer").Run()
		os.Remove(svcPath)
		os.Remove(timerPath)
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		fmt.Println("daemon uninstalled")
	case "status":
		out, _ := exec.Command("systemctl", "--user", "status", "skillsync.timer", "--no-pager").CombinedOutput()
		fmt.Printf("%s", out)
	default:
		usage()
	}
}
