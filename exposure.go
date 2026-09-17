package main

// Exposure (SPEC §11) separates "installed" from "listed to Claude". Every
// synced skill is installed and runs by slash command; only skills a repo marks
// `metadata.exposure: auto` get their description into Claude's context. The
// knob is Claude Code's own `skillOverrides` map in ~/.claude/settings.json,
// which nothing else writes. Claude Code truncates the skill listing at ~1% of
// the context window, so exposing everything makes the wrong skill fire or none.

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Values of `metadata.exposure` in SKILL.md frontmatter.
const (
	exposureAuto     = "auto"      // triggers from its description alone
	exposureOnDemand = "on-demand" // slash command or hub only; the default
)

// Values Claude Code accepts in skillOverrides.
const (
	overrideOn        = "on"                  // name + description listed
	overrideNameOnly  = "name-only"           // name listed, description hidden
	overrideInvocable = "user-invocable-only" // hidden from Claude, `/` menu only
)

// ExposureConfig holds the promote/demote thresholds, config.json "exposure".
type ExposureConfig struct {
	PromoteUses   int  `json:"promote_uses"`    // on-demand skill invoked this often...
	PromoteDays   int  `json:"promote_days"`    // ...within this many days → "on" here
	DemoteDays    int  `json:"demote_days"`     // auto skill unused this long → "name-only" here
	HubMinMembers *int `json:"hub_min_members"` // family size that earns a hub; 0 disables hubs
}

func (e *ExposureConfig) applyDefaults() {
	if e.PromoteUses == 0 {
		e.PromoteUses = 3
	}
	if e.PromoteDays == 0 {
		e.PromoteDays = 30
	}
	if e.DemoteDays == 0 {
		e.DemoteDays = 90
	}
	if e.HubMinMembers == nil {
		n := 8
		e.HubMinMembers = &n
	}
}

// ExposureRecord is what the last sync decided for one skill, kept so a repo
// change can reset the learned state and uninstall can remove what we wrote.
type ExposureRecord struct {
	Declared string    `json:"declared"` // tier from the repo, or "hub"
	Since    time.Time `json:"since"`    // when Declared last changed; usage before it is ignored
	Written  string    `json:"written"`  // skillOverrides value we wrote
}

// declaredExposure normalizes a frontmatter value; unknown values are reported
// once and treated as the default, the repo lint is where they get rejected.
func declaredExposure(sk Skill) string {
	switch sk.Exposure {
	case exposureAuto, exposureOnDemand:
		return sk.Exposure
	case "":
		return exposureOnDemand
	}
	fmt.Fprintf(os.Stderr, "skillsync: %s: unknown metadata.exposure %q, treating as %s\n", sk.Name, sk.Exposure, exposureOnDemand)
	return exposureOnDemand
}

// effectiveOverride applies the per-machine promotion rules to a declared tier.
// Only invocations after `since` count, so a tier change in the repo resets
// what this machine learned.
func effectiveOverride(cfg ExposureConfig, declared string, since time.Time, uses []time.Time, now time.Time) string {
	recent := 0
	last := since
	for _, t := range uses {
		if t.Before(since) {
			continue
		}
		if t.After(last) {
			last = t
		}
		if now.Sub(t) <= time.Duration(cfg.PromoteDays)*24*time.Hour {
			recent++
		}
	}
	if declared == exposureAuto {
		if now.Sub(last) > time.Duration(cfg.DemoteDays)*24*time.Hour {
			return overrideNameOnly
		}
		return overrideOn
	}
	if recent >= cfg.PromoteUses {
		return overrideOn
	}
	return overrideInvocable
}

// usageBySkill groups the local event log by skill name.
func usageBySkill() map[string][]time.Time {
	uses := map[string][]time.Time{}
	for _, ev := range readEvents() {
		uses[ev.Skill] = append(uses[ev.Skill], ev.Time)
	}
	return uses
}

func firstUse(times []time.Time) time.Time {
	var first time.Time
	for _, t := range times {
		if first.IsZero() || t.Before(first) {
			first = t
		}
	}
	return first
}

func lastUse(times []time.Time) time.Time {
	var last time.Time
	for _, t := range times {
		if t.After(last) {
			last = t
		}
	}
	return last
}

// syncExposure decides a tier for every managed skill and applies it. Kept as
// one call for callers that already have the files in place; cmdSync splits
// it, because the omp adapter needs the tier while it installs.
func syncExposure(cfg *Config, state *State, resolved []Skill, now time.Time) {
	managed := map[string]bool{}
	for name := range state.Installed {
		managed[name] = true
	}
	for _, m := range state.ProjectInstalled {
		for name := range m {
			managed[name] = true
		}
	}
	applyExposure(cfg, state, resolved, decideExposure(cfg, state, resolved, managed, now), now)
}

// writeOverrides merges `set` into skillOverrides and deletes `drop`, touching
// no other entry and not rewriting the file when nothing changed. Both
// arguments name only skills skillsync manages.
func writeOverrides(set map[string]string, drop []string) error {
	settings, _ := readClaudeSettings()
	overrides, _ := settings["skillOverrides"].(map[string]any)
	if overrides == nil {
		overrides = map[string]any{}
	}
	changed := false
	for name, v := range set {
		if cur, _ := overrides[name].(string); cur != v {
			overrides[name] = v
			changed = true
		}
	}
	for _, name := range drop {
		if _, ok := overrides[name]; ok {
			delete(overrides, name)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(overrides) == 0 {
		delete(settings, "skillOverrides")
	} else {
		settings["skillOverrides"] = overrides
	}
	if err := saveJSON(claudeSettingsPath(), settings); err != nil {
		return fmt.Errorf("write %s: %w", claudeSettingsPath(), err)
	}
	return nil
}

// removeExposure drops every override skillsync wrote, for uninstall. Hub
// directories are removed separately because --keep-skills keeps them.
func removeExposure(state *State) {
	var drop []string
	for name := range state.Exposure {
		drop = append(drop, name)
	}
	sort.Strings(drop)
	if err := writeOverrides(nil, drop); err != nil {
		fmt.Fprintf(os.Stderr, "skillsync: %v\n", err)
	}
}

// listingBudgetProblem reports when the descriptions Claude Code will list
// exceed its skill-listing budget, naming the biggest contributors. Reads what
// Claude Code reads: every skill directory in ~/.claude/skills plus the current
// project's, the skillOverrides map, and the budget knobs. Silence means fine.
// Token counts are chars/4 estimates.
func listingBudgetProblem() string {
	settings, _ := readClaudeSettings()
	overrides, _ := settings["skillOverrides"].(map[string]any)
	type entry struct {
		name  string
		chars int
	}
	var entries []entry
	total := 0
	for _, dir := range listingDirs() {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			// Stat, not e.IsDir(): symlinked skill directories count too.
			if info, err := os.Stat(filepath.Join(dir, e.Name())); err != nil || !info.IsDir() {
				continue
			}
			desc, _ := skillFrontmatter(filepath.Join(dir, e.Name(), "SKILL.md"))
			if desc == "" {
				continue
			}
			chars := len(e.Name())
			switch overrides[e.Name()] {
			case overrideInvocable, "off":
				continue
			case overrideNameOnly:
			default:
				chars += len(desc)
			}
			entries = append(entries, entry{e.Name(), chars})
			total += chars
		}
	}
	budget := listingBudgetChars(settings)
	if total <= budget {
		return ""
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].chars > entries[j].chars })
	var top []string
	for _, e := range entries[:min(5, len(entries))] {
		top = append(top, fmt.Sprintf("%s (~%d)", e.name, e.chars/4))
	}
	return fmt.Sprintf("Skill descriptions listed to Claude use ~%d tokens, budget is ~%d; Claude Code truncates the rest and triggers get lost.\n"+
		"  Largest: %s\n"+
		"  Fix: mark fewer skills `metadata.exposure: auto` in the skill repo, or set them to \"user-invocable-only\" in skillOverrides.",
		total/4, budget/4, strings.Join(top, ", "))
}

// listingDirs is where Claude Code discovers personal and project skills.
// ponytail: plugin skills also count against the budget and are not scanned;
// add ~/.claude/plugins if the warning ever looks too low.
func listingDirs() []string {
	dirs := []string{claudeGlobalDir()}
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, filepath.Join(wd, ".claude", "skills"))
	}
	return dirs
}

// contextWindowTokens is the budget base when Claude Code does not say.
// ponytail: the real budget depends on the model in use; 200k is the common case.
const contextWindowTokens = 200_000

// listingBudgetChars is Claude Code's skill-listing budget, in characters.
// SLASH_COMMAND_TOOL_CHAR_BUDGET (env or settings "env") wins outright;
// otherwise skillListingBudgetFraction of the context window, default 1%.
func listingBudgetChars(settings map[string]any) int {
	v := os.Getenv("SLASH_COMMAND_TOOL_CHAR_BUDGET")
	if v == "" {
		env, _ := settings["env"].(map[string]any)
		v, _ = env["SLASH_COMMAND_TOOL_CHAR_BUDGET"].(string)
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return n
	}
	fraction := 0.01
	if f, ok := settings["skillListingBudgetFraction"].(float64); ok && f > 0 {
		fraction = f
	}
	return int(fraction * contextWindowTokens * 4)
}

// decideExposure resolves the tier for every skill in `managed` and records it
// in state.Exposure. It touches no tool's files, so a caller can decide first
// and hand the tiers to the adapters.
func decideExposure(cfg *Config, state *State, resolved []Skill, managed map[string]bool, now time.Time) map[string]string {
	byName := skillsByName(resolved)
	uses := usageBySkill()
	desired := map[string]string{}
	for name := range managed {
		sk, ok := byName[name]
		if !ok {
			continue // installed earlier, gone from every source; uninstallUnwanted handles it
		}
		declared := declaredExposure(sk)
		rec, seen := state.Exposure[name]
		if rec.Declared != declared {
			rec = ExposureRecord{Declared: declared, Since: now}
			if !seen {
				// First time we see this skill: keep the usage it already has.
				if first := firstUse(uses[name]); !first.IsZero() && first.Before(now) {
					rec.Since = first
				}
			}
		}
		rec.Written = effectiveOverride(cfg.Exposure, declared, rec.Since, uses[name], now)
		desired[name] = rec.Written
		state.Exposure[name] = rec
	}
	return desired
}

// applyExposure regenerates the hubs and writes Claude Code's skillOverrides.
// Hubs are generated for every tool that copies skills, because a hidden skill
// needs a listed router whatever is reading it; skillOverrides is Claude
// Code's own file and is written only when Claude Code is a target (omp reads
// its tier from the frontmatter the adapter wrote).
func applyExposure(cfg *Config, state *State, resolved []Skill, desired map[string]string, now time.Time) {
	syncHubs(cfg, state, skillsByName(resolved), desired, now)
	var drop []string
	for name := range state.Exposure {
		if _, keep := desired[name]; !keep {
			drop = append(drop, name)
			delete(state.Exposure, name)
		}
	}
	if !slices.Contains(cfg.Tools, "claude-code") {
		return
	}
	if err := writeOverrides(desired, drop); err != nil {
		fmt.Fprintf(os.Stderr, "skillsync: %v\n", err)
	}
}

func skillsByName(resolved []Skill) map[string]Skill {
	byName := make(map[string]Skill, len(resolved))
	for _, sk := range resolved {
		byName[sk.Name] = sk
	}
	return byName
}
