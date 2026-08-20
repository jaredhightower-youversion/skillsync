package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"syscall"
)

// Skill is a directory in a source repo containing SKILL.md.
type Skill struct {
	Name   string
	Dir    string
	Source string
}

// resolveSkills fetches every source and merges their skills; earlier sources
// win name collisions (SPEC §7 precedence).
func resolveSkills(cfg *Config) ([]Skill, error) {
	byName := map[string]Skill{}
	var order []string
	for _, src := range cfg.Sources {
		repoPath, err := fetchSource(src)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", src.URL, err)
		}
		skills, err := discoverSkills(repoPath)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", repoPath, err)
		}
		for _, sk := range skills {
			if _, taken := byName[sk.Name]; taken {
				continue // earlier source wins
			}
			sk.Source = src.Name
			byName[sk.Name] = sk
			order = append(order, sk.Name)
		}
	}
	sort.Strings(order)
	resolved := make([]Skill, 0, len(order))
	for _, n := range order {
		resolved = append(resolved, byName[n])
	}
	return resolved, nil
}

// validateSource rejects source definitions that would turn a config file into
// code execution or a path escape. Config is hand-edited and team-distributed,
// so it is a trust boundary: a name becomes a directory under ~/.skillsync/repos
// and a URL becomes a git argument, where `ext::`/`ftp::` transports run shell
// commands and a leading `-` is parsed as an option.
func validateSource(src Source) error {
	if src.Name == "" || src.Name != filepath.Base(src.Name) || strings.HasPrefix(src.Name, ".") {
		return fmt.Errorf("invalid source name %q: must be a single path element", src.Name)
	}
	if src.URL == "" || strings.HasPrefix(src.URL, "-") {
		return fmt.Errorf("invalid source url %q", src.URL)
	}
	if strings.Contains(src.URL, "::") {
		return fmt.Errorf("source url %q uses a git transport helper; only https/ssh/git/file are allowed", src.URL)
	}
	switch {
	case strings.HasPrefix(src.URL, "https://"),
		strings.HasPrefix(src.URL, "ssh://"),
		strings.HasPrefix(src.URL, "git://"),
		strings.HasPrefix(src.URL, "file://"),
		scpLikeURL.MatchString(src.URL):
	default:
		return fmt.Errorf("source url %q must be https://, ssh://, git://, file:// or user@host:path", src.URL)
	}
	if strings.HasPrefix(src.Pin, "-") {
		return fmt.Errorf("invalid pin %q", src.Pin)
	}
	return nil
}

// scpLikeURL matches git's user@host:path form.
var scpLikeURL = regexp.MustCompile(`^[A-Za-z0-9._~-]+@[A-Za-z0-9._-]+:[^\s]+$`)

// fetchSource clones on first sync, pulls afterwards, then honors a pin
// (SPEC §6). Shells out to system git so existing credentials/SSO work.
func fetchSource(src Source) (string, error) {
	if err := validateSource(src); err != nil {
		return "", err
	}
	repoPath := filepath.Join(reposDir(), src.Name)
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err == nil {
		if out, err := gitCmd(repoPath, "fetch", "--tags", "origin"); err != nil {
			return "", fmt.Errorf("git fetch: %v: %s", err, out)
		}
	} else {
		if err := os.MkdirAll(reposDir(), 0o755); err != nil {
			return "", err
		}
		if out, err := gitCmd("", "clone", "--", src.URL, repoPath); err != nil {
			return "", fmt.Errorf("git clone: %v: %s", err, out)
		}
	}
	target := src.Pin
	if target == "" {
		// Track remote default-branch head. origin/HEAD may be unset on fresh
		// clones; let git resolve and record it, then re-ask.
		head, err := gitCmd(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
		if err != nil {
			if out, err := gitCmd(repoPath, "remote", "set-head", "origin", "--auto"); err != nil {
				return "", fmt.Errorf("git remote set-head: %v: %s", err, out)
			}
			head, err = gitCmd(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
			if err != nil {
				return "", fmt.Errorf("cannot resolve default branch for %s: %v", src.URL, err)
			}
		}
		target = strings.TrimPrefix(strings.TrimSpace(head), "refs/remotes/")
	}
	if out, err := gitCmd(repoPath, "checkout", "--detach", "--force", target, "--"); err != nil {
		return "", fmt.Errorf("git checkout %s: %v: %s", target, err, out)
	}
	return repoPath, nil
}

// gitCmd returns trimmed stdout; stderr only surfaces in the error so parsed
// values (refs, hashes) never get polluted by warnings on stderr.
func gitCmd(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// discoverSkills walks the repo; every directory holding SKILL.md is a skill
// named after the directory (skills.sh convention, SPEC §3).
func discoverSkills(repoPath string) ([]Skill, error) {
	var skills []Skill
	walkErr := filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
			skills = append(skills, Skill{Name: filepath.Base(path), Dir: path})
			if path != repoPath {
				return filepath.SkipDir // no nested skills inside a skill
			}
		}
		return nil
	})
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, walkErr
}

func cmdSync() {
	unlock := lockSync()
	defer unlock()
	cfg, state, resolved := mustResolve()
	syncGlobal(cfg, state, resolved)
	if root := findProjectRoot(mustGetwd()); root != "" {
		syncProject(cfg, state, resolved, root)
		if !slices.Contains(state.Projects, root) {
			state.Projects = append(state.Projects, root)
		}
	}
	mustSaveState(state)
	flushMetrics(cfg)
}

func cmdSyncAll() {
	unlock := lockSync()
	defer unlock()
	cfg, state, resolved := mustResolve()
	syncGlobal(cfg, state, resolved)
	for _, root := range state.Projects {
		if _, err := os.Stat(filepath.Join(root, manifestName)); err != nil {
			continue // project gone or manifest removed; keep registration cheaply
		}
		syncProject(cfg, state, resolved, root)
	}
	mustSaveState(state)
	flushMetrics(cfg)
}

// lockSync serializes syncs. The daemon timer and the session-start hook both
// fire sync-all, and two runs sharing one clone can hash a half-checked-out
// tree or lose a state.json write. A second run exits rather than queueing:
// the next tick is at most 15 minutes away.
func lockSync() func() {
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		fatal("create state dir: %v", err)
	}
	path := filepath.Join(stateDir(), "sync.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fatal("open lock: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		fmt.Println("another sync is already running; skipping")
		f.Close()
		os.Exit(0)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}
}

func mustResolve() (*Config, *State, []Skill) {
	cfg, err := loadConfig()
	if err != nil {
		fatal("not initialized — run: skillsync init <git-url>")
	}
	if len(cfg.Sources) == 0 {
		fatal("no sources configured in %s", configPath())
	}
	resolved, err := resolveSkills(cfg)
	if err != nil {
		fatal("%v", err)
	}
	return cfg, loadState(), resolved
}

func mustGetwd() string {
	wd, err := os.Getwd()
	if err != nil {
		fatal("getwd: %v", err)
	}
	return wd
}

func mustSaveState(state *State) {
	if err := saveJSON(statePath(), state); err != nil {
		fatal("write state: %v", err)
	}
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

// applyAdapters runs every enabled adapter for one skill+scope and returns the
// change verb. The Claude Code adapter reports it (it owns the install record);
// when that adapter is not enabled, the generated adapters carry the state
// entry themselves so status reporting and uninstall still work.
func applyAdapters(cfg *Config, sk Skill, projectRoot string, stateMap map[string]InstalledSkill) string {
	verb, sawClaude, installed := "", false, false
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
		if tool == "claude-code" {
			sawClaude, verb = true, v
		}
	}
	if !sawClaude && installed {
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

// cmdAdopt resolves a §10 conflict by replacing the unmanaged copy with the
// managed version. Scope follows the caller: inside a project with a manifest
// it adopts the project copy, otherwise the global one — the skip warning is
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
		fatal("adopt %s: nothing installed — check the tools list in %s", name, configPath())
	}
	mustSaveState(state)
	fmt.Printf("adopted    %s (now managed, %s)\n", name, scope)
}

func cmdList() {
	_, state, resolved := mustResolve()
	for _, sk := range resolved {
		status := "available"
		if _, ok := state.Installed[sk.Name]; ok {
			status = "installed"
		}
		fmt.Printf("%-10s %-30s source=%s\n", status, sk.Name, sk.Source)
	}
}

// hashDir is a content hash over sorted relative paths + file bytes.
func hashDir(dir string) (string, error) {
	h := sha256.New()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		file, err := os.Open(f)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, file)
		file.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyDir replaces dest with an exact copy of src (atomic replace per skill:
// stage next to dest, then rename over).
func copyDir(src, dest string) error {
	staging := dest + ".skillsync-tmp"
	os.RemoveAll(staging)
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(staging, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		os.RemoveAll(staging)
		return err
	}
	return os.Rename(staging, dest)
}
