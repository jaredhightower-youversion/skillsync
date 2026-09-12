package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Feedback is the one upstream channel skillsync offers. Sync is one-way, repo
// to machines, so when a skill misfires the person (usually an agent) who saw
// it has nowhere to put that except a hand-written issue that loses the
// details. `skillsync feedback` opens the issue on the skill's source repo
// with a fixed shape: what happened, the proposed SKILL.md change, and an eval
// case in the repo's cases.json format so adopting the fix is a copy-paste
// plus a test that proves it.

// Feedback is everything the issue needs, gathered before any network call so
// the body can be rendered and tested without GitHub.
type Feedback struct {
	Skill      string
	Source     Source
	Repo       string // GitHub owner/name derived from Source.URL; "" if not GitHub
	Hash       string // installed hash, so the maintainer knows which version was reviewed
	Version    string
	Reporter   string
	Title      string
	Message    string
	Proposal   string
	Eval       *EvalCase
	ProjectDir string
}

// EvalCase matches an entry in the skill repo's evals/<skill>/cases.json, so
// the maintainer appends it verbatim. Field names and order are the repo's,
// not ours: if the eval runner changes shape, this changes with it.
type EvalCase struct {
	ID             string   `json:"id"`
	Category       string   `json:"category"`
	Prompt         string   `json:"prompt"`
	Files          []string `json:"files"`
	Rubric         []string `json:"rubric"`
	ExpectedOutput string   `json:"expected_output"`
	MaxTurns       int      `json:"max_turns"`
	ShouldTrigger  bool     `json:"should_trigger"`
	ExpectedChecks []string `json:"expected_checks"`
}

const feedbackUsage = `usage: skillsync feedback <skill> [flags]
  -m, --message <text>       what went wrong, or what should change (default: read from stdin)
  -t, --title <text>         issue title (default: first line of the message)
      --proposal <text>      proposed SKILL.md change; --proposal-file <path> reads it from a file
      --eval-prompt <text>   a user request that reproduces the problem; becomes a cases.json entry
      --eval-rubric <text>   what a correct response must do (repeat for several)
      --eval-negative        the eval prompt must NOT trigger the skill
      --dry-run              print the issue instead of opening it`

// cmdFeedback parses flags, builds the issue, and opens it with gh. It never
// mutates skill state: feedback is a message to the repo, not a local change.
func cmdFeedback(args []string) {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		fmt.Fprintln(os.Stderr, feedbackUsage)
		os.Exit(2)
	}
	fb, dryRun, err := parseFeedbackArgs(args)
	if err != nil {
		fatal("%v\n%s", err, feedbackUsage)
	}
	if fb.Message == "" {
		b, _ := io.ReadAll(os.Stdin)
		fb.Message = strings.TrimSpace(string(b))
	}
	if fb.Message == "" {
		fatal("feedback needs a message: pass -m or pipe it on stdin")
	}
	if fb.Title == "" {
		fb.Title = firstLine(fb.Message, 70)
	}

	cfg, err := loadConfig()
	if err != nil {
		fatal("not initialized: run `skillsync init <git-url>` first")
	}
	state := loadState()
	fb.Skill = args[0]
	fb.Source, fb.Hash, err = feedbackSource(cfg, state, fb.Skill)
	if err != nil {
		fatal("%v", err)
	}
	fb.Repo = githubRepo(fb.Source.URL)
	fb.Version = currentVersion()
	fb.Reporter, _ = gitCmd("", "config", "user.name")
	fb.ProjectDir = findProjectRoot(mustGetwd())
	if fb.Eval != nil {
		fb.Eval.ID = evalCaseID(fb.Title)
	}

	body := fb.Body()
	if dryRun {
		fmt.Printf("repo:  %s\ntitle: %s\n\n%s", fb.Repo, fb.IssueTitle(), body)
		return
	}
	if fb.Repo == "" {
		fatal("source %s is not a GitHub URL; use --dry-run and file the issue by hand", fb.Source.URL)
	}
	url, err := openIssue(fb.Repo, fb.IssueTitle(), body)
	if err != nil {
		fatal("open issue: %v\nre-run with --dry-run to get the body and file it by hand", err)
	}
	fmt.Printf("opened %s\n", url)
}

func parseFeedbackArgs(args []string) (*Feedback, bool, error) {
	fb := &Feedback{}
	dryRun := false
	var evalPrompt string
	var rubric []string
	negative := false
	next := func(i *int, flag string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", flag)
		}
		*i++
		return args[*i], nil
	}
	var err error
	for i := 1; i < len(args); i++ {
		a := args[i]
		flag, inline, hasInline := strings.Cut(a, "=")
		value := func() (string, error) {
			if hasInline {
				return inline, nil
			}
			return next(&i, flag)
		}
		switch flag {
		case "-m", "--message":
			fb.Message, err = value()
		case "-t", "--title":
			fb.Title, err = value()
		case "--proposal":
			fb.Proposal, err = value()
		case "--proposal-file":
			var p string
			if p, err = value(); err == nil {
				var b []byte
				b, err = os.ReadFile(p)
				fb.Proposal = string(b)
			}
		case "--eval-prompt":
			evalPrompt, err = value()
		case "--eval-rubric":
			var r string
			if r, err = value(); err == nil {
				rubric = append(rubric, r)
			}
		case "--eval-negative":
			negative = true
		case "--dry-run":
			dryRun = true
		default:
			return nil, false, fmt.Errorf("unknown flag %s", a)
		}
		if err != nil {
			return nil, false, err
		}
	}
	if len(rubric) > 0 && evalPrompt == "" {
		return nil, false, fmt.Errorf("--eval-rubric needs --eval-prompt")
	}
	if negative && evalPrompt == "" {
		return nil, false, fmt.Errorf("--eval-negative needs --eval-prompt")
	}
	if evalPrompt != "" {
		fb.Eval = newEvalCase(evalPrompt, rubric, negative)
	}
	fb.Message = strings.TrimSpace(fb.Message)
	fb.Proposal = strings.TrimSpace(fb.Proposal)
	return fb, dryRun, nil
}

// newEvalCase fills the defaults the repo's runner expects. A negative case
// carries no rubric because there is nothing to judge when the skill must
// stay silent; a positive one is judge-graded against the rubric.
func newEvalCase(prompt string, rubric []string, negative bool) *EvalCase {
	c := &EvalCase{
		Category:       "guardrail",
		Prompt:         prompt,
		Files:          []string{},
		Rubric:         rubric,
		MaxTurns:       25,
		ShouldTrigger:  !negative,
		ExpectedChecks: []string{"judge"},
	}
	if c.Rubric == nil {
		c.Rubric = []string{}
	}
	if negative {
		c.Category = "negative"
		c.ExpectedChecks = []string{}
	}
	return c
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// evalCaseID derives a stable snake_case id from the title, prefixed so a
// maintainer can tell feedback-born cases from authored ones.
func evalCaseID(title string) string {
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(title), "_"), "_")
	if len(slug) > 40 {
		slug = slug[:40]
		if i := strings.LastIndex(slug, "_"); i > 0 {
			slug = slug[:i] // cut on a word boundary, not mid-word
		}
	}
	if slug == "" {
		slug = "case"
	}
	return "feedback_" + slug
}

// feedbackSource finds which source a skill came from without a network
// round trip: the install record is enough, and feedback is often filed right
// after a failure when the network may be the problem. Skills the machine has
// never installed fall back to a full resolve.
func feedbackSource(cfg *Config, state *State, name string) (Source, string, error) {
	srcName, hash := "", ""
	if inst, ok := state.Installed[name]; ok {
		srcName, hash = inst.Source, inst.Hash
	} else {
		for _, m := range state.ProjectInstalled {
			if inst, ok := m[name]; ok {
				srcName, hash = inst.Source, inst.Hash
				break
			}
		}
	}
	if srcName == "" {
		resolved, err := resolveSkills(cfg)
		if err != nil {
			return Source{}, "", fmt.Errorf("skill %q is not installed and sources could not be fetched: %v", name, err)
		}
		for _, sk := range resolved {
			if sk.Name == name {
				srcName = sk.Source
				break
			}
		}
	}
	if srcName == builtinSource {
		return Source{Name: builtinSource, URL: "https://github.com/" + releaseRepo}, hash, nil
	}
	for _, s := range cfg.Sources {
		if s.Name == srcName {
			return s, hash, nil
		}
	}
	return Source{}, "", fmt.Errorf("skill %q not found in any source", name)
}

var githubURL = regexp.MustCompile(`^(?:https?://|git@|ssh://git@)github\.com[:/]([^/]+/[^/]+?)(?:\.git)?/?$`)

// githubRepo extracts owner/name from the SSH and HTTPS forms GitHub hands
// out. Anything else returns "" and the caller degrades to dry-run.
func githubRepo(url string) string {
	m := githubURL.FindStringSubmatch(strings.TrimSpace(url))
	if m == nil {
		return ""
	}
	return m[1]
}

func (fb *Feedback) IssueTitle() string {
	return fmt.Sprintf("[skill-feedback] %s: %s", fb.Skill, fb.Title)
}

// Body renders the issue. The sections are fixed so a maintainer, or a script,
// can find the proposal and the eval case in the same place every time.
func (fb *Feedback) Body() string {
	var b strings.Builder
	fmt.Fprintf(&b, "## What happened\n\n%s\n\n", fb.Message)

	b.WriteString("## Proposed change to SKILL.md\n\n")
	if fb.Proposal == "" {
		b.WriteString("_None proposed. The maintainer decides the wording._\n\n")
	} else {
		fmt.Fprintf(&b, "%s\n\n", fb.Proposal)
	}

	b.WriteString("## Eval case\n\n")
	if fb.Eval == nil {
		b.WriteString("_No eval case supplied. Add one before adopting so the fix is testable._\n\n")
	} else {
		fmt.Fprintf(&b, "Append to `evals/%s/cases.json` when adopting. It should fail on the current skill and pass after the change.\n\n", fb.Skill)
		js, _ := json.MarshalIndent(fb.Eval, "", "  ")
		fmt.Fprintf(&b, "```json\n%s\n```\n\n", js)
	}

	b.WriteString("## Context\n\n")
	fmt.Fprintf(&b, "- skill: `%s`\n", fb.Skill)
	fmt.Fprintf(&b, "- source: %s (%s)\n", fb.Source.Name, fb.Source.URL)
	if fb.Hash != "" {
		fmt.Fprintf(&b, "- installed hash: `%s`\n", fb.Hash)
	}
	if fb.ProjectDir != "" {
		fmt.Fprintf(&b, "- reported from project: `%s`\n", fb.ProjectDir)
	}
	if fb.Reporter != "" {
		fmt.Fprintf(&b, "- reported by: %s\n", fb.Reporter)
	}
	fmt.Fprintf(&b, "- skillsync %s\n", fb.Version)
	return b.String()
}

// openIssue shells out to gh rather than calling the API: gh already holds
// the user's token and handles SSO, which is the whole reason this command
// can be one step instead of a login flow.
func openIssue(repo, title, body string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("gh CLI not found; install it from https://cli.github.com and run `gh auth login`")
	}
	tmp, err := os.CreateTemp("", "skillsync-feedback-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.WriteString(body); err != nil {
		return "", err
	}
	tmp.Close()
	cmd := exec.Command("gh", "issue", "create", "--repo", repo, "--title", title, "--body-file", tmp.Name())
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func firstLine(s string, max int) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return truncate(strings.TrimSpace(line), max)
}
