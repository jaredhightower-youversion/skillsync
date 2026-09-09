package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// User-facing commands that are not the sync itself.

// cmdInit is the whole onboarding: point at a repo and the machine is set up.
// The background job and hooks are what make skills update without anyone
// running a command, so they are on by default, a tool that silently never
// auto-updates is the failure this project exists to prevent. Either can be
// declined, and neither failing is fatal: the config and skills are still
// good, so we warn and carry on rather than leaving a half-initialized state.
//
// It ends by printing `auto status` rather than a fixed success line, so a
// skipped or failed step is visible to whoever ran it, human or agent, instead
// of being reported as "ready".
func cmdInit(url string, tools []string, withDaemon, withHooks bool) {
	if _, err := loadConfig(); err == nil {
		fatal("already initialized (%s); edit it to change sources", configPath())
	}
	src := Source{Name: "default", URL: url}
	// Prove the URL works before persisting it, otherwise a typo leaves a
	// config that makes every retry of `init` refuse to run.
	if _, err := fetchSource(src); err != nil {
		fatal("cannot use %s: %v", url, err)
	}
	cfg := &Config{
		Sources: []Source{src},
		Tools:   tools,
		Metrics: Metrics{Enabled: true},
	}
	cfg.Exposure.applyDefaults() // written out so the thresholds are visible and editable
	if err := saveJSON(configPath(), cfg); err != nil {
		fatal("write config: %v", err)
	}
	fmt.Printf("initialized with source %s\n", url)
	fmt.Printf("installing into: %s\n", strings.Join(tools, ", "))
	cmdSync()

	if withDaemon {
		runStep("background sync", func() { cmdDaemon("install") })
	} else {
		fmt.Println("skipped background job (--no-daemon): skills will NOT update on their own")
	}
	if withHooks {
		runStep("Claude Code hooks", func() { cmdHook("install") })
	} else {
		fmt.Println("skipped Claude Code hooks (--no-hooks): no session-start sync, no usage stats")
	}
	fmt.Println()
	autoStatus()
}

// parseTools turns the `--tools` flag into the config's tool list. Empty means
// the default. Unknown names are rejected here because sync silently ignores
// tools it has no adapter for, which would otherwise look like a successful
// install into nothing.
func parseTools(flag string) ([]string, error) {
	if strings.TrimSpace(flag) == "" {
		return []string{"claude-code"}, nil
	}
	var tools []string
	for _, t := range strings.Split(flag, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, ok := adapters[t]; !ok {
			return nil, fmt.Errorf("unknown tool %q in --tools; choose from: %s", t, strings.Join(adapterNames(), ", "))
		}
		if !slices.Contains(tools, t) {
			tools = append(tools, t)
		}
	}
	if len(tools) == 0 {
		return nil, fmt.Errorf("--tools given but empty; choose from: %s", strings.Join(adapterNames(), ", "))
	}
	return tools, nil
}

func adapterNames() []string {
	names := make([]string, 0, len(adapters))
	for name := range adapters {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func cmdSubscribe(name string, add bool) {
	cfg, err := loadConfig()
	if err != nil {
		fatal("not initialized. Run: skillsync init <git-url>")
	}
	// First explicit `add` narrows an unset (= everything) list to a chosen set;
	// first explicit `remove` needs the resolved set to subtract from.
	var list []string
	if cfg.GlobalSkills != nil {
		list = *cfg.GlobalSkills
	} else if add {
		list = nil
	} else {
		_, _, resolved := mustResolve()
		for _, sk := range resolved {
			list = append(list, sk.Name)
		}
	}
	has := slices.Contains(list, name)
	switch {
	case add && has:
		fmt.Printf("already subscribed to %s\n", name)
		return
	case add:
		list = append(list, name)
	case !has:
		fmt.Printf("not subscribed to %s\n", name)
		return
	default:
		list = slices.DeleteFunc(list, func(s string) bool { return s == name })
	}
	if list == nil {
		list = []string{} // explicitly empty, not "unset"
	}
	cfg.GlobalSkills = &list
	if err := saveJSON(configPath(), cfg); err != nil {
		fatal("write config: %v", err)
	}
	fmt.Println("updated subscriptions; run `skillsync sync` to apply")
}

// cmdAdopt resolves a §10 conflict by replacing the unmanaged copy with the
// managed version. Scope follows the caller: inside a project with a manifest
// it adopts the project copy, otherwise the global one, the skip warning is
// raised per scope, so it must be resolvable per scope.
func cmdAdopt(name string) {
	cfg, state, resolved := mustResolve()
	i := slices.IndexFunc(resolved, func(s Skill) bool { return s.Name == name })
	if i < 0 {
		fatal("skill %q not found in any source", name)
	}
	sk := resolved[i]

	root := findProjectRoot(mustGetwd())
	scopeMap := state.Installed
	scope := "global"
	if root != "" {
		if state.ProjectInstalled[root] == nil {
			state.ProjectInstalled[root] = map[string]InstalledSkill{}
		}
		scopeMap = state.ProjectInstalled[root]
		scope = "project " + filepath.Base(root)
	}
	// Clear the unmanaged copy so Install takes the plain install path.
	for _, tool := range cfg.Tools {
		if a, ok := adapters[tool]; ok {
			if err := a.Remove(sk.Name, root); err != nil && !os.IsNotExist(err) {
				fatal("adopt %s: %v", name, err)
			}
		}
	}
	if verb := applyAdapters(cfg, sk, root, scopeMap); verb == "" {
		fatal("adopt %s: nothing installed, check the tools list in %s", name, configPath())
	}
	mustSaveState(state)
	fmt.Printf("adopted    %s (now managed, %s)\n", name, scope)
}

// cmdList shows every resolved skill, newest change first, so "what changed
// lately" is answerable at a glance, plus what Claude sees of each (SPEC §11).
func cmdList() {
	warnIfUnhealthy()
	_, state, resolved := mustResolve()
	uses := usageBySkill()

	type row struct {
		skill   Skill
		updated time.Time
		author  string
	}
	rows := make([]row, 0, len(resolved))
	for _, sk := range resolved {
		when, who := lastChanged(sk)
		rows = append(rows, row{sk, when, who})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].updated.After(rows[j].updated) })

	fmt.Printf("%-10s %-28s %-10s %-20s %-12s %-12s %-16s %s\n",
		"STATUS", "SKILL", "TIER", "CLAUDE SEES", "LAST USED", "UPDATED", "BY", "SOURCE")
	for _, r := range rows {
		status, sees := "available", "-"
		if _, ok := state.Installed[r.skill.Name]; ok {
			status = "installed"
			if rec, ok := state.Exposure[r.skill.Name]; ok {
				sees = rec.Written
			}
		}
		fmt.Printf("%-10s %-28s %-10s %-20s %-12s %-12s %-16s %s\n",
			status, r.skill.Name, declaredExposure(r.skill), sees, humanAge(lastUse(uses[r.skill.Name])),
			humanAge(r.updated), truncate(r.author, 16), r.skill.Source)
	}
}

func humanAge(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

// cmdUninstall removes everything skillsync put on this machine: managed
// skills in every scope it installed into, the background job, the Claude Code
// hooks, and its own state directory. Skills the user installed by hand are
// untouched, they were never in the install record.
func cmdUninstall(keepSkills bool) {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Println("not initialized; nothing to remove")
		return
	}
	state := loadState()
	if !keepSkills {
		removed := uninstallUnwanted(cfg, state.Installed, nil, "")
		for root, installed := range state.ProjectInstalled {
			removed += uninstallUnwanted(cfg, installed, nil, root)
		}
		fmt.Printf("removed %d managed skill(s)\n", removed)
	} else {
		fmt.Println("left installed skills in place (--keep-skills)")
	}
	// Overrides always go: leaving them would hide skills we no longer manage.
	// Hubs stay with --keep-skills, they still point at the kept members.
	removeExposure(state)
	if !keepSkills {
		for _, hub := range state.Hubs {
			removeHub(hub)
		}
	}
	cmdDaemon("uninstall")
	cmdHook("uninstall")
	if err := os.RemoveAll(stateDir()); err != nil {
		fatal("remove %s: %v", stateDir(), err)
	}
	fmt.Printf("removed %s\n", stateDir())
	fmt.Println("done, delete the skillsync binary itself to finish")
}
