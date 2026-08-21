package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// Adapter installs or removes one skill in one tool's expected location
// (SPEC §3). projectRoot == "" means global scope. stateMap is the install
// record for the scope being synced.
//
// Every supported tool now reads SKILL.md directories natively, so all three
// adapters are copies into different paths and all share the drift/adopt
// decision table.
type Adapter interface {
	Install(sk Skill, projectRoot string, stateMap map[string]InstalledSkill) (verb string, err error)
	// Remove deletes what Install wrote for a skill that is no longer
	// subscribed or listed in a project manifest.
	Remove(name, projectRoot string) error
}

// Claude Code and Cursor both read SKILL.md directories natively, so both are
// plain copies into different paths — no translation, and both get the §5/§10
// drift/adopt handling, since either target is somewhere a user may also have
// installed a skill by hand.
var adapters = map[string]Adapter{
	"claude-code": dirAdapter{
		global:  func() string { return claudeGlobalDir() },
		project: func(root string) string { return filepath.Join(root, ".claude", "skills") },
	},
	"cursor": dirAdapter{
		global:  func() string { return filepath.Join(homeDir(), ".cursor", "skills") },
		project: func(root string) string { return filepath.Join(root, ".cursor", "skills") },
	},
	// Codex reads `.agents/skills`, the cross-tool location, per OpenAI's
	// skills documentation (learn.chatgpt.com/docs/build-skills, checked
	// 2026-08-20). AGENTS.md is a separate mechanism for general instructions
	// and is deliberately left alone.
	"codex": dirAdapter{
		global:  func() string { return filepath.Join(homeDir(), ".agents", "skills") },
		project: func(root string) string { return filepath.Join(root, ".agents", "skills") },
	},
}

func claudeGlobalDir() string { return filepath.Join(homeDir(), ".claude", "skills") }

// dirAdapter installs a skill by copying its directory to a tool's skills
// path. It owns the §5/§10 drift/adopt decision table because its output is a
// byte-for-byte copy the user may also have installed by hand.
type dirAdapter struct {
	global  func() string
	project func(root string) string
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
	dest := filepath.Join(base, sk.Name)
	rec, managed := stateMap[sk.Name]

	destInfo, statErr := os.Stat(dest)
	destExists := statErr == nil && destInfo.IsDir()

	verb := "installed"
	switch {
	case destExists && !managed:
		destHash, err := hashDir(dest)
		if err != nil {
			return "", err
		}
		if destHash != srcHash {
			// SPEC §10: same name, different content, not ours — never touch.
			return "", fmt.Errorf("skipped: unmanaged local skill differs from source; run `skillsync adopt %s` or rename yours", sk.Name)
		}
		verb = "adopted" // §10: identical content — take ownership, no rewrite
	case destExists && managed:
		destHash, err := hashDir(dest)
		if err != nil {
			return "", err
		}
		if destHash == srcHash {
			return "", nil // up to date
		}
		if destHash != rec.Hash {
			// SPEC §5: managed = overwrite, but surface the drift.
			fmt.Fprintf(os.Stderr, "skillsync: %s: local edits overwritten — edit skills in the source repo (%s)\n", sk.Name, sk.Source)
		}
		verb = "updated"
	}

	if verb != "adopted" {
		if err := copyDir(sk.Dir, dest); err != nil {
			return "", err
		}
	}
	stateMap[sk.Name] = InstalledSkill{Source: sk.Source, Hash: srcHash}
	return verb, nil
}

func (a dirAdapter) Remove(name, projectRoot string) error {
	base := a.global()
	if projectRoot != "" {
		base = a.project(projectRoot)
	}
	return os.RemoveAll(filepath.Join(base, name))
}

func writeIfChanged(path string, content []byte) error {
	if existing, err := os.ReadFile(path); err == nil && string(existing) == string(content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o644)
}
