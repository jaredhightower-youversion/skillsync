package main

// Sources are git repos; every directory holding SKILL.md is a skill (SPEC §3, §6, §7).

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Skill is a directory in a source repo containing SKILL.md.
type Skill struct {
	Name        string
	Dir         string
	Source      string
	Description string // frontmatter description, what Claude sees when exposed
	Exposure    string // frontmatter metadata.exposure, "" = on-demand
	// Override is the exposure tier this sync decided for the skill, set
	// before install so an adapter that writes the tier into the installed
	// copy (omp) has it. Empty means undecided: install it listed.
	Override string
}

// resolveSkills fetches every source and merges their skills; earlier sources
// win name collisions (SPEC §7 precedence).
func resolveSkills(cfg *Config) ([]Skill, error) {
	byName := map[string]Skill{}
	var order []string
	for _, src := range cfg.Sources {
		repoPath, err := fetchSource(src)
		if err != nil {
			return nil, fmt.Errorf("fetch %s: %w", src.URL, err)
		}
		skills, err := discoverSkills(repoPath)
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", repoPath, err)
		}
		for _, sk := range skills {
			if _, taken := byName[sk.Name]; taken {
				continue // earlier source wins
			}
			sk.Source = src.Name
			byName[sk.Name] = sk
			order = append(order, sk.Name)
		}
	}
	sort.Strings(order)
	resolved := make([]Skill, 0, len(order))
	for _, n := range order {
		resolved = append(resolved, byName[n])
	}
	return withBuiltin(resolved)
}

func mustResolve() (*Config, *State, []Skill) {
	cfg, err := loadConfig()
	if err != nil {
		fatal("not initialized. Run: skillsync init <git-url>")
	}
	if len(cfg.Sources) == 0 {
		fatal("no sources configured in %s", configPath())
	}
	resolved, err := resolveSkills(cfg)
	if err != nil {
		fatal("%v", err)
	}
	return cfg, loadState(), resolved
}

// validateSource rejects source definitions that would turn a config file into
// code execution or a path escape. Config is hand-edited and team-distributed,
// so it is a trust boundary: a name becomes a directory under ~/.skillsync/repos
// and a URL becomes a git argument, where `ext::`/`ftp::` transports run shell
// commands and a leading `-` is parsed as an option.
func validateSource(src Source) error {
	if src.Name == "" || src.Name != filepath.Base(src.Name) || strings.HasPrefix(src.Name, ".") {
		return fmt.Errorf("invalid source name %q: must be a single path element", src.Name)
	}
	if src.URL == "" || strings.HasPrefix(src.URL, "-") {
		return fmt.Errorf("invalid source url %q", src.URL)
	}
	if strings.Contains(src.URL, "::") {
		return fmt.Errorf("source url %q uses a git transport helper; only https/ssh/git/file are allowed", src.URL)
	}
	switch {
	case strings.HasPrefix(src.URL, "https://"),
		strings.HasPrefix(src.URL, "ssh://"),
		strings.HasPrefix(src.URL, "git://"),
		strings.HasPrefix(src.URL, "file://"),
		scpLikeURL.MatchString(src.URL):
	default:
		return fmt.Errorf("source url %q must be https://, ssh://, git://, file:// or user@host:path", src.URL)
	}
	if strings.HasPrefix(src.Pin, "-") {
		return fmt.Errorf("invalid pin %q", src.Pin)
	}
	return nil
}

// scpLikeURL matches git's user@host:path form.
var scpLikeURL = regexp.MustCompile(`^[A-Za-z0-9._~-]+@[A-Za-z0-9._-]+:[^\s]+$`)

// fetchSource clones on first sync, pulls afterwards, then honors a pin
// (SPEC §6). Shells out to system git so existing credentials/SSO work.
func fetchSource(src Source) (string, error) {
	if err := validateSource(src); err != nil {
		return "", err
	}
	repoPath := filepath.Join(reposDir(), src.Name)
	if _, err := os.Stat(filepath.Join(repoPath, ".git")); err == nil {
		if out, err := gitCmd(repoPath, "fetch", "--tags", "origin"); err != nil {
			return "", fmt.Errorf("git fetch: %v: %s", err, out)
		}
	} else {
		if err := os.MkdirAll(reposDir(), 0o755); err != nil {
			return "", err
		}
		if out, err := gitCmd("", "clone", "--", src.URL, repoPath); err != nil {
			return "", fmt.Errorf("git clone: %v: %s", err, out)
		}
	}
	target := src.Pin
	if target == "" {
		// Track remote default-branch head. origin/HEAD may be unset on fresh
		// clones; let git resolve and record it, then re-ask.
		head, err := gitCmd(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
		if err != nil {
			if out, err := gitCmd(repoPath, "remote", "set-head", "origin", "--auto"); err != nil {
				return "", fmt.Errorf("git remote set-head: %v: %s", err, out)
			}
			head, err = gitCmd(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
			if err != nil {
				return "", fmt.Errorf("cannot resolve default branch for %s: %v", src.URL, err)
			}
		}
		target = strings.TrimPrefix(strings.TrimSpace(head), "refs/remotes/")
	}
	if out, err := gitCmd(repoPath, "checkout", "--detach", "--force", target, "--"); err != nil {
		return "", fmt.Errorf("git checkout %s: %v: %s", target, err, out)
	}
	return repoPath, nil
}

// gitCmd returns trimmed stdout; stderr only surfaces in the error so parsed
// values (refs, hashes) never get polluted by warnings on stderr.
func gitCmd(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return strings.TrimSpace(stdout.String()), fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// discoverSkills walks the repo; every directory holding SKILL.md is a skill
// named after the directory (skills.sh convention, SPEC §3).
func discoverSkills(repoPath string) ([]Skill, error) {
	var skills []Skill
	walkErr := filepath.WalkDir(repoPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() == ".git" {
			return filepath.SkipDir
		}
		if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err == nil {
			desc, exposure := skillFrontmatter(filepath.Join(path, "SKILL.md"))
			skills = append(skills, Skill{Name: filepath.Base(path), Dir: path, Description: desc, Exposure: exposure})
			if path != repoPath {
				return filepath.SkipDir // no nested skills inside a skill
			}
		}
		return nil
	})
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, walkErr
}

// lastChanged reports when a skill last changed upstream and who changed it,
// read from the source repo's history rather than file mtimes (which reflect
// when we cloned, not when the skill was edited).
func lastChanged(sk Skill) (time.Time, string) {
	if sk.Source == builtinSource {
		return time.Time{}, "built in"
	}
	repoPath := filepath.Join(reposDir(), sk.Source)
	rel, err := filepath.Rel(repoPath, sk.Dir)
	if err != nil {
		return time.Time{}, ""
	}
	out, err := gitCmd(repoPath, "log", "-1", "--format=%cI%x00%an", "--", rel)
	if err != nil || out == "" {
		return time.Time{}, ""
	}
	stamp, author, _ := strings.Cut(out, "\x00")
	when, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		return time.Time{}, author
	}
	return when, author
}
