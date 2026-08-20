package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Claude Code hooks (SPEC §2 session-start freshness, §8 exact usage events):
//   SessionStart          → skillsync sync-all   (backgrounded, non-blocking)
//   PostToolUse (Skill)   → skillsync track --stdin
//
// `hook install` merges into ~/.claude/settings.json idempotently;
// `hook print` emits the JSON for users who manage settings by hand.

func claudeSettingsPath() string { return filepath.Join(homeDir(), ".claude", "settings.json") }

// readClaudeSettings returns the parsed settings file, or an empty map when it
// does not exist. A malformed file is fatal rather than silently replaced —
// it holds the user's own configuration.
func readClaudeSettings() (map[string]any, bool) {
	settings := map[string]any{}
	b, err := os.ReadFile(claudeSettingsPath())
	if err != nil {
		return settings, false
	}
	if err := json.Unmarshal(b, &settings); err != nil {
		fatal("parse %s: %v — fix it or use `skillsync hook print` and edit by hand", claudeSettingsPath(), err)
	}
	return settings, true
}

func hookEntries() (sessionStart, postToolUse map[string]any) {
	bin := binaryPath()
	// Quote the path: install locations like ~/Library/Application Support
	// contain spaces, and an unquoted path runs the wrong command silently.
	sessionStart = map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": fmt.Sprintf("(%q sync-all >/dev/null 2>&1 &)", bin),
		}},
	}
	postToolUse = map[string]any{
		"matcher": "Skill",
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": fmt.Sprintf("%q track --stdin", bin),
		}},
	}
	return
}

func cmdHook(sub string) {
	ss, ptu := hookEntries()
	switch sub {
	case "print":
		out, _ := json.MarshalIndent(map[string]any{
			"hooks": map[string]any{
				"SessionStart": []any{ss},
				"PostToolUse":  []any{ptu},
			},
		}, "", "  ")
		fmt.Println(string(out))
	case "uninstall":
		settings, ok := readClaudeSettings()
		if !ok {
			fmt.Println("no Claude Code settings file; nothing to remove")
			return
		}
		hooks, _ := settings["hooks"].(map[string]any)
		removed := 0
		for event, list := range hooks {
			entries, _ := list.([]any)
			kept := make([]any, 0, len(entries))
			for _, e := range entries {
				if hasSkillsyncHook([]any{e}) {
					removed++
					continue
				}
				kept = append(kept, e)
			}
			if len(kept) == 0 {
				delete(hooks, event)
			} else {
				hooks[event] = kept
			}
		}
		if removed == 0 {
			fmt.Println("no skillsync hooks found")
			return
		}
		if len(hooks) == 0 {
			delete(settings, "hooks")
		}
		if err := saveJSON(claudeSettingsPath(), settings); err != nil {
			fatal("write %s: %v", claudeSettingsPath(), err)
		}
		fmt.Printf("removed %d hook(s) from %s\n", removed, claudeSettingsPath())
	case "install":
		settings, _ := readClaudeSettings()
		hooks, _ := settings["hooks"].(map[string]any)
		if hooks == nil {
			hooks = map[string]any{}
		}
		added := 0
		for event, entry := range map[string]map[string]any{"SessionStart": ss, "PostToolUse": ptu} {
			list, _ := hooks[event].([]any)
			if hasSkillsyncHook(list) {
				continue
			}
			hooks[event] = append(list, entry)
			added++
		}
		settings["hooks"] = hooks
		if added == 0 {
			fmt.Println("hooks already installed")
			return
		}
		if err := saveJSON(claudeSettingsPath(), settings); err != nil {
			fatal("write %s: %v", claudeSettingsPath(), err)
		}
		fmt.Printf("installed %d hook(s) into %s\n", added, claudeSettingsPath())
	default:
		usage()
	}
}

// hasSkillsyncHook reports whether any hook command in the list mentions
// skillsync — the idempotency check for `hook install`.
func hasSkillsyncHook(list []any) bool {
	b, _ := json.Marshal(list)
	return strings.Contains(string(b), "skillsync")
}
