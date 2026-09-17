package main

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
)

// Adapter installs or removes one skill in one tool's expected location
// (SPEC §3). projectRoot == "" means global scope. stateMap is the install
// record for the scope being synced.
//
// Every supported tool reads SKILL.md directories natively, so every adapter
// is a copy into a different path and all share the drift/adopt decision
// table. One of them, omp, rewrites frontmatter on the way in (SPEC §11),
// which is why the install record keeps a per-tool hash of what was written.
type Adapter interface {
	Install(sk Skill, projectRoot string, stateMap map[string]InstalledSkill) (verb string, err error)
	// Remove deletes what Install wrote for a skill that is no longer
	// subscribed or listed in a project manifest.
	Remove(name, projectRoot string) error
}

var adapters = map[string]Adapter{
	"claude-code": dirAdapter{
		tool:    "claude-code",
		global:  func() string { return claudeGlobalDir() },
		project: func(root string) string { return filepath.Join(root, ".claude", "skills") },
	},
	"cursor": dirAdapter{
		tool:    "cursor",
		global:  func() string { return filepath.Join(homeDir(), ".cursor", "skills") },
		project: func(root string) string { return filepath.Join(root, ".cursor", "skills") },
	},
	// Codex reads `.agents/skills`, the cross-tool location, per OpenAI's
	// skills documentation (learn.chatgpt.com/docs/build-skills, checked
	// 2026-08-20). AGENTS.md is a separate mechanism for general instructions
	// and is deliberately left alone.
	"codex": dirAdapter{
		tool:    "codex",
		global:  func() string { return filepath.Join(homeDir(), ".agents", "skills") },
		project: func(root string) string { return filepath.Join(root, ".agents", "skills") },
	},
	// omp (Oh My Pi) discovers `~/.omp/agent/skills` and `<root>/.omp/skills`
	// natively at priority 100, above the copies it would otherwise pick up
	// from ~/.claude/skills. It has no skillOverrides equivalent: exposure is
	// per-skill frontmatter, so this adapter writes it (see ompExposure).
	"omp": dirAdapter{
		tool:      "omp",
		global:    func() string { return ompGlobalDir() },
		project:   func(root string) string { return filepath.Join(root, ".omp", "skills") },
		transform: ompExposure,
	},
}

func claudeGlobalDir() string { return filepath.Join(homeDir(), ".claude", "skills") }

// dirAdapter installs a skill by copying its directory to a tool's skills
// path. It owns the §5/§10 drift/adopt decision table because its output is
// something the user may also have installed by hand.
type dirAdapter struct {
	tool      string
	global    func() string
	project   func(root string) string
	transform transform // nil = byte-for-byte copy
}

func (a dirAdapter) Install(sk Skill, projectRoot string, stateMap map[string]InstalledSkill) (string, error) {
	base := a.global()
	if projectRoot != "" {
		base = a.project(projectRoot)
	}
	srcHash, err := hashDir(sk.Dir)
	if err != nil {
		return "", err
	}
	// What this tool's copy should contain, which is the source only when the
	// adapter does not transform.
	wantHash := srcHash
	if a.transform != nil {
		if wantHash, err = hashDirWith(sk, a.transform); err != nil {
			return "", err
		}
	}
	dest := filepath.Join(base, sk.Name)
	rec, managed := stateMap[sk.Name]
	// The hash of what we last wrote here. Records written before per-tool
	// hashes existed only have the source hash, which is the same value for
	// every non-transforming adapter.
	lastWritten := rec.Hash
	if h, ok := rec.Outputs[a.tool]; ok {
		lastWritten = h
	}

	destInfo, statErr := os.Stat(dest)
	destExists := statErr == nil && destInfo.IsDir()

	verb := "installed"
	upToDate := false
	switch {
	case destExists && !managed:
		destHash, err := hashDir(dest)
		if err != nil {
			return "", err
		}
		if destHash != wantHash {
			// SPEC §10: same name, different content, not ours, never touch.
			return "", fmt.Errorf("skipped: unmanaged local skill differs from source; run `skillsync adopt %s` or rename yours", sk.Name)
		}
		verb = "adopted" // §10: identical content, take ownership, no rewrite
	case destExists && managed:
		destHash, err := hashDir(dest)
		if err != nil {
			return "", err
		}
		if destHash == wantHash {
			upToDate = true
			break
		}
		if destHash != lastWritten {
			// SPEC §5: managed = overwrite, but surface the drift.
			fmt.Fprintf(os.Stderr, "skillsync: %s: local edits overwritten, edit skills in the source repo (%s)\n", sk.Name, sk.Source)
		}
		verb = "updated"
	}

	if !upToDate && verb != "adopted" {
		if err := copyDir(sk, dest, a.transform); err != nil {
			return "", err
		}
	}
	outputs := maps.Clone(rec.Outputs)
	if outputs == nil {
		outputs = map[string]string{}
	}
	outputs[a.tool] = wantHash
	stateMap[sk.Name] = InstalledSkill{Source: sk.Source, Hash: srcHash, Outputs: outputs}
	if upToDate {
		return "", nil
	}
	return verb, nil
}

func (a dirAdapter) Remove(name, projectRoot string) error {
	base := a.global()
	if projectRoot != "" {
		base = a.project(projectRoot)
	}
	return os.RemoveAll(filepath.Join(base, name))
}

func ompGlobalDir() string { return filepath.Join(homeDir(), ".omp", "agent", "skills") }

// transform rewrites one file on its way from the source repo into a tool's
// skills directory. rel is the path relative to the skill directory.
type transform func(sk Skill, rel string, content []byte) []byte

// ompExposure is omp's half of SPEC §11. Claude Code takes the tier out of
// band through skillOverrides; omp reads `hide: true` from the skill's own
// frontmatter, where it means exactly what user-invocable-only means in Claude
// Code: not listed to the model, still loaded, still reachable through
// `/skill:<name>` and `skill://<name>`. name-only has no omp equivalent — the
// skill was declared auto, so it stays listed rather than disappearing.
func ompExposure(sk Skill, rel string, content []byte) []byte {
	if rel != "SKILL.md" || sk.Override != overrideInvocable {
		return content
	}
	return hideFrontmatter(content)
}

// hideFrontmatter adds `hide: true` to a SKILL.md's frontmatter block. A file
// without frontmatter is left alone: omp's native provider requires a
// description, so such a file was never going to load, and synthesizing a
// block would change what the repo said. A `hide` the repo set itself wins.
func hideFrontmatter(content []byte) []byte {
	lines := strings.Split(string(content), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return content
	}
	for i, line := range lines[1:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" {
			out := make([]string, 0, len(lines)+1)
			out = append(out, lines[:i+1]...)
			out = append(out, "hide: true")
			out = append(out, lines[i+1:]...)
			return []byte(strings.Join(out, "\n"))
		}
		if key, _, ok := strings.Cut(line, ":"); ok && key == "hide" {
			return content
		}
	}
	return content // unterminated frontmatter; not ours to repair
}
