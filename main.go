// skillsync: team skill directory with auto-sync across agent tools.
// See SPEC.md for the decision record this implements.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// Config lives at ~/.skillsync/config.json.
type Config struct {
	// Precedence-ordered: earlier sources win name collisions (SPEC §7).
	Sources []Source `json:"sources"`
	// Adapter targets. Default: claude-code only; users opt into others.
	Tools []string `json:"tools"`
	// Global subscriptions (SPEC §4). Unset (nil) = subscribe to everything,
	// the indie-friendly default. An explicitly emptied list means "none" —
	// unsubscribing your last skill must not silently install every skill in
	// every source.
	GlobalSkills *[]string `json:"global_skills,omitempty"`
	Metrics      Metrics   `json:"metrics"`
}

// subscribed reports whether a skill belongs in the user's global scope.
func (c *Config) subscribed(name string) bool {
	if c.GlobalSkills == nil {
		return true
	}
	return slices.Contains(*c.GlobalSkills, name)
}

type Source struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	// Pin holds a tag or commit; empty = track default-branch head (SPEC §6).
	Pin string `json:"pin,omitempty"`
}

type Metrics struct {
	Enabled  bool   `json:"enabled"`
	Endpoint string `json:"endpoint,omitempty"` // empty = local-only stats (SPEC §8)
}

// Health records the outcome of the last sync so failures surface where people
// look, instead of dying silently in the daemon log. A background job that
// stopped working is the worst failure this tool has: skills quietly stop
// updating and nobody notices for weeks.
type Health struct {
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

// stale is how long without a successful sync before we start warning. The
// background job runs every 15 minutes, so a few missed ticks are noise; four
// hours means something is actually wrong.
const staleAfter = 4 * time.Hour

// State tracks installs per scope. Hash mismatch on disk = local drift
// (SPEC §5); unknown dir with matching hash = adoptable (SPEC §10).
type State struct {
	Health    Health                    `json:"health"`
	Installed map[string]InstalledSkill `json:"installed"` // global scope
	// Projects registered by running `skillsync sync` inside them; the daemon
	// re-syncs each (SPEC §4).
	Projects         []string                             `json:"projects,omitempty"`
	ProjectInstalled map[string]map[string]InstalledSkill `json:"project_installed,omitempty"`
}

type InstalledSkill struct {
	Source string `json:"source"`
	Hash   string `json:"hash"`
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		fatal("cannot determine home directory: %v", err)
	}
	return h
}

func stateDir() string    { return filepath.Join(homeDir(), ".skillsync") }
func configPath() string  { return filepath.Join(stateDir(), "config.json") }
func statePath() string   { return filepath.Join(stateDir(), "state.json") }
func reposDir() string    { return filepath.Join(stateDir(), "repos") }
func eventsPath() string  { return filepath.Join(stateDir(), "events.jsonl") }

// binaryPath is the absolute path to this binary, used when writing scheduler
// and hook entries that must invoke it later.
func binaryPath() string {
	p, err := os.Executable()
	if err != nil || p == "" {
		fatal("cannot determine the skillsync binary path: %v", err)
	}
	return p
}

func loadConfig() (*Config, error) {
	b, err := os.ReadFile(configPath())
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", configPath(), err)
	}
	if len(c.Tools) == 0 {
		c.Tools = []string{"claude-code"}
	}
	return &c, nil
}

func loadState() *State {
	s := &State{}
	b, err := os.ReadFile(statePath())
	if err == nil {
		_ = json.Unmarshal(b, s) // corrupt state = fresh; sync re-adopts by hash
	}
	if s.Installed == nil {
		s.Installed = map[string]InstalledSkill{}
	}
	if s.ProjectInstalled == nil {
		s.ProjectInstalled = map[string]map[string]InstalledSkill{}
	}
	return s
}

func saveJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// recoverableFatal makes fatal panic instead of exiting, so an optional setup
// step can fail without killing the run. Set only by runStep.
var recoverableFatal bool

func fatal(format string, args ...any) {
	if recoverableFatal {
		panic(fmt.Sprintf(format, args...))
	}
	fmt.Fprintf(os.Stderr, "skillsync: "+format+"\n", args...)
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  skillsync init <git-url>            set up this machine (--no-daemon / --no-hooks to skip a part)
  skillsync sync                      sync now
  skillsync list                      skills, when each changed, and who changed it
  skillsync add|remove <skill>        manage your global subscriptions
  skillsync adopt <skill>             replace a hand-installed skill with the managed version
  skillsync stats                     how often each skill gets used
  skillsync auto on|off|status        automatic updating: background job + Claude Code hooks
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
	switch os.Args[1] {
	case "init":
		cmdInit(arg(2), !hasFlag("--no-daemon"), !hasFlag("--no-hooks"))
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
	default:
		usage()
	}
}

// cmdInit is the whole onboarding: point at a repo and the machine is set up.
// The background job and hooks are what make skills update without anyone
// running a command, so they are on by default — a tool that silently never
// auto-updates is the failure this project exists to prevent. Either can be
// declined, and neither failing is fatal: the config and skills are still
// good, so we warn and carry on rather than leaving a half-initialized state.
func cmdInit(url string, withDaemon, withHooks bool) {
	if _, err := loadConfig(); err == nil {
		fatal("already initialized (%s); edit it to change sources", configPath())
	}
	src := Source{Name: "default", URL: url}
	// Prove the URL works before persisting it — otherwise a typo leaves a
	// config that makes every retry of `init` refuse to run.
	if _, err := fetchSource(src); err != nil {
		fatal("cannot use %s: %v", url, err)
	}
	cfg := &Config{
		Sources: []Source{src},
		Tools:   []string{"claude-code"},
		Metrics: Metrics{Enabled: true},
	}
	if err := saveJSON(configPath(), cfg); err != nil {
		fatal("write config: %v", err)
	}
	fmt.Printf("initialized with source %s\n", url)
	cmdSync()

	if withDaemon {
		runStep("background sync", func() { cmdDaemon("install") })
	}
	if withHooks {
		runStep("Claude Code hooks", func() { cmdHook("install") })
	}
	fmt.Println("\nready — skills stay up to date on their own. `skillsync list` to see them.")
}

// runStep runs an optional setup step, turning a fatal into a warning so one
// unavailable scheduler or unwritable settings file cannot abort onboarding.
func runStep(label string, step func()) {
	recoverableFatal = true
	defer func() {
		recoverableFatal = false
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "skillsync: could not set up %s: %v\n", label, r)
			fmt.Fprintf(os.Stderr, "  skills still sync when you run `skillsync sync`.\n")
		}
	}()
	step()
}

func cmdSubscribe(name string, add bool) {
	cfg, err := loadConfig()
	if err != nil {
		fatal("not initialized — run: skillsync init <git-url>")
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
