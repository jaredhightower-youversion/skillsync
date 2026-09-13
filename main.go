// skillsync: team skill directory with auto-sync across agent tools.
// See SPEC.md for the decision record this implements.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Config lives at ~/.skillsync/config.json.
type Config struct {
	// Precedence-ordered: earlier sources win name collisions (SPEC §7).
	Sources []Source `json:"sources"`
	// Adapter targets. Default: claude-code only; users opt into others.
	Tools []string `json:"tools"`
	// Global subscriptions (SPEC §4). Unset (nil) = subscribe to everything,
	// the indie-friendly default. An explicitly emptied list means "none",
	// unsubscribing your last skill must not silently install every skill in
	// every source.
	GlobalSkills *[]string `json:"global_skills,omitempty"`
	Metrics      Metrics   `json:"metrics"`
	// Promote/demote thresholds for what Claude sees (SPEC §11).
	Exposure ExposureConfig `json:"exposure"`
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
	// What the last sync wrote to Claude Code's skillOverrides, per skill and
	// hub, and which hub directories it generated (SPEC §11).
	Exposure map[string]ExposureRecord `json:"exposure,omitempty"`
	Hubs     []string                  `json:"hubs,omitempty"`
	// Update is the newest release tag seen by the daily check in sync, so
	// the offline `check` hook can point at `skillsync upgrade`.
	Update UpdateInfo `json:"update,omitempty"`
}

type UpdateInfo struct {
	Latest    string    `json:"latest,omitempty"`
	CheckedAt time.Time `json:"checked_at,omitempty"`
}

type InstalledSkill struct {
	Source string `json:"source"`
	Hash   string `json:"hash"`
	// Outputs is the hash of what each tool's copy should contain, keyed by
	// tool name. It equals Hash for every adapter that copies byte for byte;
	// omp rewrites frontmatter, so its copy hashes differently and needs its
	// own record to tell "we wrote this" from "the user edited it".
	Outputs map[string]string `json:"outputs,omitempty"`
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		fatal("cannot determine home directory: %v", err)
	}
	return h
}

func stateDir() string   { return filepath.Join(homeDir(), ".skillsync") }
func configPath() string { return filepath.Join(stateDir(), "config.json") }
func statePath() string  { return filepath.Join(stateDir(), "state.json") }
func reposDir() string   { return filepath.Join(stateDir(), "repos") }
func eventsPath() string { return filepath.Join(stateDir(), "events.jsonl") }

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
	c.Exposure.applyDefaults()
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
	if s.Exposure == nil {
		s.Exposure = map[string]ExposureRecord{}
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
  skillsync init <git-url>            set up this machine: sync now, then keep syncing on its own
      --tools claude-code,cursor,codex,omp  which agent tools to install into (default: claude-code)
      --no-daemon / --no-hooks          escape hatches for CI or locked-down machines: skills then
                                        go stale until you run "skillsync sync" yourself
  skillsync sync                      sync now
  skillsync list                      skills, what Claude sees of each, when each changed, and who changed it
  skillsync check                     warn if syncs fail or Claude's skill listing is over budget
  skillsync add|remove <skill>        manage your global subscriptions
  skillsync adopt <skill>             replace a hand-installed skill with the managed version
  skillsync stats                     how often each skill gets used
  skillsync feedback <skill> -m ...   open an issue on the skill's repo with the problem, a proposed fix, and an eval case
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
	case "feedback":
		cmdFeedback(os.Args[2:])
	case "upgrade":
		cmdUpgrade()
	case "version", "--version", "-v":
		cmdVersion()
	default:
		usage()
	}
}

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
