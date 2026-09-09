package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// The sync: install global subscriptions and project manifests through every
// enabled adapter, remove what is no longer wanted, then reconcile exposure.

// cmdSync syncs everything: global subscriptions, every project synced before,
// and the project you are standing in. One command rather than the old
// sync/sync-all split, which asked users to know which scope they wanted for
// an operation that is idempotent and cheap either way.
func cmdSync() {
	unlock := lockSync()
	defer unlock()

	cfg, err := loadConfig()
	if err != nil {
		fatal("not initialized. Run: skillsync init <git-url>")
	}
	if len(cfg.Sources) == 0 {
		fatal("no sources configured in %s", configPath())
	}
	state := loadState()

	resolved, resolveErr := resolveSkills(cfg)
	recordSync(state, resolveErr)
	if resolveErr != nil {
		mustSaveState(state) // remember the failure so `auto status` can report it
		fatal("%v", resolveErr)
	}

	syncGlobal(cfg, state, resolved)
	for _, root := range state.Projects {
		if _, err := os.Stat(filepath.Join(root, manifestName)); err != nil {
			continue // project gone or manifest removed; keep registration cheaply
		}
		syncProject(cfg, state, resolved, root)
	}
	if root := findProjectRoot(mustGetwd()); root != "" && !slices.Contains(state.Projects, root) {
		syncProject(cfg, state, resolved, root)
		state.Projects = append(state.Projects, root)
	}
	syncExposure(cfg, state, resolved, time.Now().UTC())
	recordLatestVersion(state)
	mustSaveState(state)
	flushMetrics(cfg)
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		fatal("getwd: %v", err)
	}
	return wd
}

// syncGlobal installs the user's global subscriptions (SPEC §4) through every
// enabled adapter's global target.
func syncGlobal(cfg *Config, state *State, resolved []Skill) {
	changed := 0
	wanted := map[string]bool{}
	for _, sk := range resolved {
		if !cfg.subscribed(sk.Name) {
			continue
		}
		wanted[sk.Name] = true
		verb := applyAdapters(cfg, sk, "", state.Installed)
		if verb != "" {
			fmt.Printf("%-10s %s\n", verb, sk.Name)
			changed++
		}
	}
	changed += uninstallUnwanted(cfg, state.Installed, wanted, "")
	fmt.Printf("global sync: %d changed\n", changed)
}

// syncProject installs the project manifest's skills project-locally (SPEC §4).
// Project-local shadows global by the agent tools' own precedence; we report
// the shadowing instead of touching the global copy.
func syncProject(cfg *Config, state *State, resolved []Skill, root string) {
	names, err := readManifest(filepath.Join(root, manifestName))
	if err != nil {
		fmt.Fprintf(os.Stderr, "skillsync: %s: %v\n", root, err)
		return
	}
	if state.ProjectInstalled[root] == nil {
		state.ProjectInstalled[root] = map[string]InstalledSkill{}
	}
	changed := 0
	wanted := map[string]bool{}
	for _, name := range names {
		i := slices.IndexFunc(resolved, func(s Skill) bool { return s.Name == name })
		if i < 0 {
			fmt.Fprintf(os.Stderr, "skillsync: %s: skill %q not found in any source\n", root, name)
			continue
		}
		sk := resolved[i]
		wanted[sk.Name] = true
		verb := applyAdapters(cfg, sk, root, state.ProjectInstalled[root])
		if verb != "" {
			fmt.Printf("%-10s %s (project %s)\n", verb, sk.Name, filepath.Base(root))
			changed++
		}
		if _, global := state.Installed[sk.Name]; global && verb != "" {
			fmt.Printf("note: project-local %s shadows your global copy in this project\n", sk.Name)
		}
	}
	changed += uninstallUnwanted(cfg, state.ProjectInstalled[root], wanted, root)
	fmt.Printf("project sync (%s): %d changed\n", filepath.Base(root), changed)
}

// applyAdapters runs every enabled adapter for one skill+scope and returns a
// verb describing what changed, or "" when everything was already up to date.
// The copying adapters maintain the install record; AGENTS.md-style adapters
// report nothing, so when only those are enabled we record the install here to
// keep status reporting and uninstall working.
func applyAdapters(cfg *Config, sk Skill, projectRoot string, stateMap map[string]InstalledSkill) string {
	verb, reported, installed := "", false, false
	for _, tool := range cfg.Tools {
		a, ok := adapters[tool]
		if !ok {
			fmt.Fprintf(os.Stderr, "skillsync: unknown tool %q in config\n", tool)
			continue
		}
		v, err := a.Install(sk, projectRoot, stateMap)
		if err != nil {
			fmt.Fprintf(os.Stderr, "skillsync: %s [%s]: %v\n", sk.Name, tool, err)
			continue
		}
		installed = true
		if _, copies := a.(dirAdapter); copies {
			reported = true
			if verb == "" {
				verb = v
			}
		}
	}
	if !reported && installed {
		hash, err := hashDir(sk.Dir)
		if err != nil {
			return ""
		}
		if rec, ok := stateMap[sk.Name]; ok && rec.Hash == hash {
			return ""
		}
		stateMap[sk.Name] = InstalledSkill{Source: sk.Source, Hash: hash}
		verb = "generated"
	}
	return verb
}

// uninstallUnwanted removes skills we installed that are no longer subscribed
// or listed in the project manifest, so unsubscribing actually takes effect.
func uninstallUnwanted(cfg *Config, stateMap map[string]InstalledSkill, wanted map[string]bool, projectRoot string) int {
	removed := 0
	for name := range stateMap {
		if wanted[name] {
			continue
		}
		for _, tool := range cfg.Tools {
			a, ok := adapters[tool]
			if !ok {
				continue
			}
			if err := a.Remove(name, projectRoot); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(os.Stderr, "skillsync: remove %s [%s]: %v\n", name, tool, err)
			}
		}
		delete(stateMap, name)
		fmt.Printf("%-10s %s\n", "removed", name)
		removed++
	}
	return removed
}
