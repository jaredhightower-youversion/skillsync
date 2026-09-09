package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"
)

// State lives at ~/.skillsync/state.json and records what sync did on this machine.

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

func mustSaveState(state *State) {
	if err := saveJSON(statePath(), state); err != nil {
		fatal("write state: %v", err)
	}
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
