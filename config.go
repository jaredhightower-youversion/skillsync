package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// Config lives at ~/.skillsync/config.json. It is hand-edited and team-distributed.

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

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		fatal("cannot determine home directory: %v", err)
	}
	return h
}

func stateDir() string { return filepath.Join(homeDir(), ".skillsync") }

func configPath() string { return filepath.Join(stateDir(), "config.json") }

func statePath() string { return filepath.Join(stateDir(), "state.json") }

func reposDir() string { return filepath.Join(stateDir(), "repos") }

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
