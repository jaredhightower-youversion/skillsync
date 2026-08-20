package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Covers the SPEC §5/§10 decision table: install, up-to-date, managed drift
// overwrite, unmanaged identical adopt, unmanaged different skip.
func TestClaudeAdapterDecisionTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	stateMap := map[string]InstalledSkill{}
	a := claudeAdapter{}

	dir := writeSkill(t, repo, "alpha", "v1")
	sk := Skill{Name: "alpha", Dir: dir, Source: "default"}

	verb, err := a.Install(sk, "", stateMap)
	if err != nil || verb != "installed" {
		t.Fatalf("fresh install: verb=%q err=%v", verb, err)
	}
	if _, err := os.Stat(filepath.Join(claudeGlobalDir(), "alpha", "SKILL.md")); err != nil {
		t.Fatalf("skill not on disk: %v", err)
	}

	verb, err = a.Install(sk, "", stateMap)
	if err != nil || verb != "" {
		t.Fatalf("up-to-date: verb=%q err=%v", verb, err)
	}

	// Managed drift: local edit then upstream change — must overwrite (§5).
	installed := filepath.Join(claudeGlobalDir(), "alpha", "SKILL.md")
	os.WriteFile(installed, []byte("local hack"), 0o644)
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("v2"), 0o644)
	verb, err = a.Install(sk, "", stateMap)
	if err != nil || verb != "updated" {
		t.Fatalf("drift overwrite: verb=%q err=%v", verb, err)
	}
	got, _ := os.ReadFile(installed)
	if string(got) != "v2" {
		t.Fatalf("expected upstream content, got %q", got)
	}

	// Unmanaged identical → adopt (§10).
	betaDir := writeSkill(t, repo, "beta", "same")
	writeSkill(t, claudeGlobalDir(), "beta", "same")
	verb, err = a.Install(Skill{Name: "beta", Dir: betaDir, Source: "default"}, "", stateMap)
	if err != nil || verb != "adopted" {
		t.Fatalf("adopt: verb=%q err=%v", verb, err)
	}

	// Unmanaged different → skip with error, file untouched (§10).
	gammaDir := writeSkill(t, repo, "gamma", "upstream")
	writeSkill(t, claudeGlobalDir(), "gamma", "mine")
	_, err = a.Install(Skill{Name: "gamma", Dir: gammaDir, Source: "default"}, "", stateMap)
	if err == nil {
		t.Fatal("expected skip error for unmanaged differing skill")
	}
	got, _ = os.ReadFile(filepath.Join(claudeGlobalDir(), "gamma", "SKILL.md"))
	if string(got) != "mine" {
		t.Fatalf("unmanaged skill was modified: %q", got)
	}

	// Project scope lands under <root>/.claude/skills.
	proj := t.TempDir()
	verb, err = a.Install(sk, proj, map[string]InstalledSkill{})
	if err != nil || verb != "installed" {
		t.Fatalf("project install: verb=%q err=%v", verb, err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude", "skills", "alpha", "SKILL.md")); err != nil {
		t.Fatalf("project skill missing: %v", err)
	}
}

func TestDiscoverSkills(t *testing.T) {
	repo := t.TempDir()
	writeSkill(t, repo, "b-skill", "x")
	writeSkill(t, repo, "a-skill", "x")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	skills, err := discoverSkills(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 2 || skills[0].Name != "a-skill" || skills[1].Name != "b-skill" {
		t.Fatalf("unexpected: %+v", skills)
	}
}

func TestReadManifest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, manifestName)
	os.WriteFile(path, []byte("# team skills\nskills:\n  - code-review\n  - deploy\nother: ignored\n"), 0o644)
	names, err := readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "code-review" || names[1] != "deploy" {
		t.Fatalf("unexpected: %v", names)
	}
	os.WriteFile(path, []byte("nothing: here\n"), 0o644)
	if _, err := readManifest(path); err == nil {
		t.Fatal("expected error for manifest without skills")
	}
}

func TestFrontmatterAndCursor(t *testing.T) {
	repo := t.TempDir()
	dir := writeSkill(t, repo, "fm", "---\nname: fm\ndescription: does things\n---\n\nBody here.")
	fm, body, err := readSkillMD(dir)
	if err != nil || fm.Description != "does things" || !strings.Contains(body, "Body here.") {
		t.Fatalf("frontmatter: %+v body=%q err=%v", fm, body, err)
	}

	proj := t.TempDir()
	if _, err := (cursorAdapter{}).Install(Skill{Name: "fm", Dir: dir}, proj, nil); err != nil {
		t.Fatal(err)
	}
	mdc, _ := os.ReadFile(filepath.Join(proj, ".cursor", "rules", "fm.mdc"))
	if !strings.Contains(string(mdc), "description: does things") || !strings.Contains(string(mdc), "Body here.") {
		t.Fatalf("bad mdc: %s", mdc)
	}
	// Global scope is a no-op for cursor.
	if _, err := (cursorAdapter{}).Install(Skill{Name: "fm", Dir: dir}, "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestCodexManagedSection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	dir := writeSkill(t, repo, "cx", "---\nname: cx\ndescription: codex skill\n---\ninstructions v1")
	proj := t.TempDir()
	os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("# My project notes\n"), 0o644)

	a := codexAdapter{}
	if _, err := a.Install(Skill{Name: "cx", Dir: dir}, proj, nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(proj, "AGENTS.md"))
	if !strings.Contains(string(got), "# My project notes") || !strings.Contains(string(got), "instructions v1") {
		t.Fatalf("user content lost or skill missing:\n%s", got)
	}

	// Update replaces the block, no duplication; user content preserved.
	os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: cx\ndescription: codex skill\n---\ninstructions v2"), 0o644)
	if _, err := a.Install(Skill{Name: "cx", Dir: dir}, proj, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(proj, "AGENTS.md"))
	s := string(got)
	if strings.Contains(s, "instructions v1") || !strings.Contains(s, "instructions v2") {
		t.Fatalf("stale block not replaced:\n%s", s)
	}
	if strings.Count(s, "### cx") != 1 || strings.Count(s, codexStart) != 1 {
		t.Fatalf("duplicated section:\n%s", s)
	}

	// Second skill appends inside the same managed region.
	dir2 := writeSkill(t, repo, "cy", "---\nname: cy\ndescription: second\n---\nsecond body")
	if _, err := a.Install(Skill{Name: "cy", Dir: dir2}, proj, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(filepath.Join(proj, "AGENTS.md"))
	if !strings.Contains(string(got), "### cy") || strings.Count(string(got), codexStart) != 1 {
		t.Fatalf("second skill not merged into region:\n%s", got)
	}
}

func TestTrackAndStats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmdTrack([]string{"my-skill"})
	cmdTrack([]string{"my-skill"})
	events := readEvents()
	if len(events) != 2 || events[0].Skill != "my-skill" || events[0].Tool != "claude-code" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestHookIdempotency(t *testing.T) {
	ss, _ := hookEntries()
	if hasSkillsyncHook(nil) {
		t.Fatal("empty list should not match")
	}
	if !hasSkillsyncHook([]any{ss}) {
		t.Fatal("skillsync entry should match")
	}
}

func TestFlushMetrics(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmdTrack([]string{"a"})
	var got []usageEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	flushMetrics(&Config{Metrics: Metrics{Enabled: true, Endpoint: srv.URL}})
	if len(got) != 1 || got[0].Skill != "a" {
		t.Fatalf("sink got %+v", got)
	}
	if _, err := os.Stat(eventsPath()); !os.IsNotExist(err) {
		t.Fatal("queue not cleared after successful flush")
	}
	// Failed flush keeps the queue.
	cmdTrack([]string{"b"})
	flushMetrics(&Config{Metrics: Metrics{Enabled: true, Endpoint: "http://127.0.0.1:1/nope"}})
	if len(readEvents()) != 1 {
		t.Fatal("queue lost on failed flush")
	}
}
