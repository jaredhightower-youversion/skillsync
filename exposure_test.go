package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSkillFrontmatter(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][2]string{
		"---\nname: a\ndescription: plain text: with colon\n---\nbody":                                   {"plain text: with colon", ""},
		"---\ndescription: \"quoted\"\nmetadata:\n  exposure: auto\n---\n":                               {"quoted", "auto"},
		"---\ndescription: >-\n  folded line one\n  line two\nmetadata:\n  exposure: 'on-demand'\n---\n": {"folded line one line two", "on-demand"},
		"no frontmatter": {"", ""},
	}
	for body, want := range cases {
		p := filepath.Join(dir, "SKILL.md")
		os.WriteFile(p, []byte(body), 0o644)
		desc, exp := skillFrontmatter(p)
		if desc != want[0] || exp != want[1] {
			t.Errorf("%q: got (%q, %q), want (%q, %q)", body, desc, exp, want[0], want[1])
		}
	}
}

func TestEffectiveOverride(t *testing.T) {
	var cfg ExposureConfig
	cfg.applyDefaults()
	now := time.Now()
	day := 24 * time.Hour
	since := now.Add(-200 * day)
	recent := []time.Time{now.Add(-day), now.Add(-2 * day), now.Add(-3 * day)}
	cases := []struct {
		declared string
		since    time.Time
		uses     []time.Time
		want     string
	}{
		{exposureOnDemand, since, nil, overrideInvocable},
		{exposureOnDemand, since, recent[:2], overrideInvocable},
		{exposureOnDemand, since, recent, overrideOn},
		{exposureOnDemand, now, recent, overrideInvocable}, // tier changed after those uses: reset
		{exposureAuto, since, recent, overrideOn},
		{exposureAuto, since, nil, overrideNameOnly},
		{exposureAuto, now, nil, overrideOn}, // just declared auto: demotion clock starts now
	}
	for i, c := range cases {
		if got := effectiveOverride(cfg, c.declared, c.since, c.uses, now); got != c.want {
			t.Errorf("case %d: got %s, want %s", i, got, c.want)
		}
	}
}

func readOverrides(t *testing.T) map[string]any {
	t.Helper()
	settings, _ := readClaudeSettings()
	o, _ := settings["skillOverrides"].(map[string]any)
	return o
}

// Covers SPEC §11 end to end: overrides written and merged, hub generated for
// a large family, promotion from usage, reconciliation when skills leave, and
// uninstall leaving only the user's own entries.
func TestSyncExposure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(claudeSettingsPath(), []byte(`{"skillOverrides":{"mine":"off"},"model":"x"}`), 0o644)

	cfg := &Config{Tools: []string{"claude-code"}}
	cfg.Exposure.applyDefaults()
	state := loadState()
	repo := t.TempDir()
	var resolved []Skill
	add := func(name, body string) {
		dir := writeSkill(t, repo, name, body)
		resolved = append(resolved, discoverOne(t, dir))
		state.Installed[name] = InstalledSkill{Source: "default"}
	}
	add("unslop", "---\ndescription: cut AI tells\nmetadata:\n  exposure: auto\n---\n")
	for i := 0; i < 9; i++ {
		add("mkt-"+string(rune('a'+i)), "---\ndescription: marketing thing\n---\n")
	}
	mustSaveState(state) // track only records skills the saved state says are managed
	for i := 0; i < 3; i++ {
		cmdTrack([]string{"mkt-a"})
	}

	syncExposure(cfg, state, resolved, time.Now())
	o := readOverrides(t)
	for name, want := range map[string]string{"unslop": "on", "mkt-a": "on", "mkt-b": "user-invocable-only", "mkt": "on", "mine": "off"} {
		if o[name] != want {
			t.Errorf("%s: got %v, want %s", name, o[name], want)
		}
	}
	hub, err := os.ReadFile(filepath.Join(claudeGlobalDir(), "mkt", "SKILL.md"))
	if err != nil || !strings.Contains(string(hub), hubMarker) || !strings.Contains(string(hub), "`mkt-i`") {
		t.Fatalf("hub not generated: %v\n%s", err, hub)
	}
	if strings.Contains(string(hub), "`mkt-a`") {
		t.Error("promoted member should not be routed through the hub")
	}

	// Family shrinks below the threshold: hub and its override go away.
	for _, n := range []string{"mkt-g", "mkt-h", "mkt-i"} {
		delete(state.Installed, n)
	}
	syncExposure(cfg, state, resolved, time.Now())
	o = readOverrides(t)
	if _, ok := o["mkt"]; ok || len(state.Hubs) != 0 {
		t.Error("hub override should be dropped once the family is too small")
	}
	if _, ok := o["mkt-h"]; ok {
		t.Error("override for an uninstalled skill should be dropped")
	}
	if _, err := os.Stat(filepath.Join(claudeGlobalDir(), "mkt")); !os.IsNotExist(err) {
		t.Error("hub directory should be removed")
	}

	removeExposure(state)
	settings, _ := readClaudeSettings()
	o, _ = settings["skillOverrides"].(map[string]any)
	if len(o) != 1 || o["mine"] != "off" || settings["model"] != "x" {
		t.Errorf("uninstall should leave only the user's entries, got %v", settings)
	}
}

// A hidden skill needs a listed router in whichever directory the tool reads,
// with paths into that same directory — omp never looks in ~/.claude/skills.
func TestHubsWrittenForEveryTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &Config{Tools: []string{"claude-code", "omp"}}
	cfg.Exposure.applyDefaults()
	state := loadState()
	repo := t.TempDir()
	var resolved []Skill
	for i := range 9 {
		name := "mkt-" + string(rune('a'+i))
		dir := writeSkill(t, repo, name, "---\ndescription: marketing thing\n---\n")
		resolved = append(resolved, discoverOne(t, dir))
		state.Installed[name] = InstalledSkill{Source: "default"}
	}

	syncExposure(cfg, state, resolved, time.Now())

	for _, dir := range []string{claudeGlobalDir(), ompGlobalDir()} {
		hub, err := os.ReadFile(filepath.Join(dir, "mkt", "SKILL.md"))
		if err != nil {
			t.Fatalf("hub missing in %s: %v", dir, err)
		}
		if !strings.Contains(string(hub), filepath.Join(dir, "mkt-a", "SKILL.md")) {
			t.Errorf("hub in %s should route to its own copies:\n%s", dir, hub)
		}
	}

	state.Installed = map[string]InstalledSkill{}
	syncExposure(cfg, state, resolved, time.Now())
	for _, dir := range []string{claudeGlobalDir(), ompGlobalDir()} {
		if _, err := os.Stat(filepath.Join(dir, "mkt")); !os.IsNotExist(err) {
			t.Errorf("hub in %s should be removed once the family is gone", dir)
		}
	}
}

func discoverOne(t *testing.T, dir string) Skill {
	t.Helper()
	skills, err := discoverSkills(dir)
	if err != nil || len(skills) != 1 {
		t.Fatalf("discover %s: %v", dir, err)
	}
	skills[0].Source = "default"
	return skills[0]
}

func TestListingBudgetProblem(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SLASH_COMMAND_TOOL_CHAR_BUDGET", "100")
	writeSkill(t, claudeGlobalDir(), "big", "---\ndescription: "+strings.Repeat("x", 200)+"\n---\n")
	if p := listingBudgetProblem(); !strings.Contains(p, "big") {
		t.Fatalf("expected an over-budget warning naming the skill, got %q", p)
	}
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	b, _ := json.Marshal(map[string]any{"skillOverrides": map[string]string{"big": overrideInvocable}})
	os.WriteFile(claudeSettingsPath(), b, 0o644)
	if p := listingBudgetProblem(); p != "" {
		t.Fatalf("hidden skill should not count: %q", p)
	}
}
