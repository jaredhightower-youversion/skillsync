package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGithubRepo(t *testing.T) {
	cases := map[string]string{
		"git@github.com:org/repo.git":         "org/repo",
		"git@github.com:org/repo":             "org/repo",
		"https://github.com/org/repo.git":     "org/repo",
		"https://github.com/org/repo":         "org/repo",
		"https://github.com/org/repo/":        "org/repo",
		"ssh://git@github.com/org/repo.git":   "org/repo",
		"https://gitlab.com/org/repo.git":     "",
		"git@github.com:org":                  "",
		"https://github.com/org/repo/tree/mn": "",
	}
	for url, want := range cases {
		if got := githubRepo(url); got != want {
			t.Errorf("githubRepo(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestParseFeedbackArgs(t *testing.T) {
	fb, dry, err := parseFeedbackArgs([]string{"ops-handoff",
		"-m", "edited before summary", "--title=Resume gate",
		"--proposal", "Print summary first.",
		"--eval-prompt", "Resume the handoff", "--eval-rubric", "summary first", "--eval-rubric", "no edits",
		"--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if !dry || fb.Message != "edited before summary" || fb.Title != "Resume gate" || fb.Proposal != "Print summary first." {
		t.Errorf("parsed wrong: %+v dry=%v", fb, dry)
	}
	if fb.Eval == nil || fb.Eval.Prompt != "Resume the handoff" || len(fb.Eval.Rubric) != 2 ||
		!fb.Eval.ShouldTrigger || fb.Eval.Category != "guardrail" || fb.Eval.ExpectedChecks[0] != "judge" {
		t.Errorf("eval case wrong: %+v", fb.Eval)
	}

	fb, _, err = parseFeedbackArgs([]string{"x", "-m", "m", "--eval-prompt", "p", "--eval-negative"})
	if err != nil {
		t.Fatal(err)
	}
	if fb.Eval.ShouldTrigger || fb.Eval.Category != "negative" || len(fb.Eval.ExpectedChecks) != 0 {
		t.Errorf("negative case wrong: %+v", fb.Eval)
	}

	for _, bad := range [][]string{
		{"x", "--eval-rubric", "r"}, // rubric without prompt
		{"x", "--eval-negative"},    // negative without prompt
		{"x", "-m"},                 // missing value
		{"x", "--bogus"},            // unknown flag
	} {
		if _, _, err := parseFeedbackArgs(bad); err == nil {
			t.Errorf("expected error for %v", bad)
		}
	}
}

func TestEvalCaseID(t *testing.T) {
	if got := evalCaseID("Resume: edits before summary!"); got != "feedback_resume_edits_before_summary" {
		t.Errorf("got %q", got)
	}
	if got := evalCaseID("!!!"); got != "feedback_case" {
		t.Errorf("empty slug got %q", got)
	}
	long := evalCaseID("Resume step 4 edited files before printing the summary first")
	if long != "feedback_resume_step_4_edited_files_before" {
		t.Errorf("long id should cut on a word boundary, got %q", long)
	}
}

// The body is what a maintainer reads and what a script would parse; the
// eval block must be valid JSON in the repo's cases.json shape.
func TestFeedbackBody(t *testing.T) {
	fb := &Feedback{
		Skill:    "ops-handoff",
		Source:   Source{Name: "default", URL: "git@github.com:org/skills.git"},
		Hash:     "abc",
		Version:  "v9.9.9",
		Reporter: "Ada",
		Title:    "Resume gate",
		Message:  "edited before summary",
		Proposal: "Print summary first.",
		Eval:     newEvalCase("Resume the handoff", []string{"summary first"}, false),
	}
	fb.Eval.ID = evalCaseID(fb.Title)
	body := fb.Body()
	for _, want := range []string{
		"## What happened", "edited before summary",
		"## Proposed change to SKILL.md", "Print summary first.",
		"## Eval case", "`evals/ops-handoff/cases.json`",
		"## Context", "installed hash: `abc`", "reported by: Ada", "skillsync v9.9.9",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q:\n%s", want, body)
		}
	}
	_, after, _ := strings.Cut(body, "```json\n")
	js, _, ok := strings.Cut(after, "\n```")
	if !ok {
		t.Fatalf("no json block:\n%s", body)
	}
	var c EvalCase
	if err := json.Unmarshal([]byte(js), &c); err != nil {
		t.Fatalf("eval block is not valid JSON: %v\n%s", err, js)
	}
	if c.ID != "feedback_resume_gate" || c.Prompt != "Resume the handoff" || c.MaxTurns != 25 || c.Files == nil {
		t.Errorf("round-tripped case wrong: %+v", c)
	}
	if fb.IssueTitle() != "[skill-feedback] ops-handoff: Resume gate" {
		t.Errorf("title %q", fb.IssueTitle())
	}

	empty := &Feedback{Skill: "s", Message: "m"}
	if b := empty.Body(); !strings.Contains(b, "None proposed") || !strings.Contains(b, "No eval case") {
		t.Errorf("placeholders missing:\n%s", b)
	}
}

func TestFeedbackSourceFromState(t *testing.T) {
	cfg := &Config{Sources: []Source{{Name: "team", URL: "git@github.com:org/skills.git"}}}
	state := &State{
		Installed:        map[string]InstalledSkill{"alpha": {Source: "team", Hash: "h1"}},
		ProjectInstalled: map[string]map[string]InstalledSkill{"/p": {"beta": {Source: "team", Hash: "h2"}}},
	}
	src, hash, err := feedbackSource(cfg, state, "alpha")
	if err != nil || src.URL != cfg.Sources[0].URL || hash != "h1" {
		t.Errorf("global: %v %v %v", src, hash, err)
	}
	src, hash, err = feedbackSource(cfg, state, "beta")
	if err != nil || src.Name != "team" || hash != "h2" {
		t.Errorf("project: %v %v %v", src, hash, err)
	}
	state.Installed["skillsync"] = InstalledSkill{Source: builtinSource, Hash: "h3"}
	src, _, err = feedbackSource(cfg, state, "skillsync")
	if err != nil || githubRepo(src.URL) != releaseRepo {
		t.Errorf("builtin should point at %s: %v %v", releaseRepo, src, err)
	}
}
