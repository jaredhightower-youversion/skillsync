package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Adapter installs or removes one skill in one tool's expected location
// (SPEC §3). projectRoot == "" means global scope. stateMap is the install
// record for the scope being synced.
//
// Only the claude-code adapter's returned verb and stateMap writes are used by
// the sync loop (sync.go): it is the canonical passthrough, and its target is
// the one a user can also have written by hand, so it owns the drift/adopt
// decision table. The generated adapters report nothing and are regenerated
// from the resolved skill set on every sync.
type Adapter interface {
	Install(sk Skill, projectRoot string, stateMap map[string]InstalledSkill) (verb string, err error)
	// Remove deletes what Install wrote for a skill that is no longer
	// subscribed or listed in a project manifest.
	Remove(name, projectRoot string) error
}

var adapters = map[string]Adapter{
	"claude-code": claudeAdapter{},
	"cursor":      cursorAdapter{},
	"codex":       codexAdapter{},
}

func claudeGlobalDir() string { return filepath.Join(homeDir(), ".claude", "skills") }

// claudeAdapter copies the skill directory verbatim — SKILL.md is Claude
// Code's native format. It owns the §5/§10 drift/adopt decision table because
// its output is a byte-for-byte copy the user may also have installed by hand.
type claudeAdapter struct{}

func (claudeAdapter) Install(sk Skill, projectRoot string, stateMap map[string]InstalledSkill) (string, error) {
	base := claudeGlobalDir()
	if projectRoot != "" {
		base = filepath.Join(projectRoot, ".claude", "skills")
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

func (claudeAdapter) Remove(name, projectRoot string) error {
	base := claudeGlobalDir()
	if projectRoot != "" {
		base = filepath.Join(projectRoot, ".claude", "skills")
	}
	return os.RemoveAll(filepath.Join(base, name))
}

// cursorAdapter generates .cursor/rules/<name>.mdc from SKILL.md. Cursor rules
// are project-scoped only; global scope is a no-op. Generated files are
// regenerated atomically every sync (Ruler-style) — local edits to generated
// artifacts are not preserved by design.
type cursorAdapter struct{}

func (cursorAdapter) Install(sk Skill, projectRoot string, _ map[string]InstalledSkill) (string, error) {
	if projectRoot == "" {
		return "", nil
	}
	fm, body, err := readSkillMD(sk.Dir)
	if err != nil {
		return "", err
	}
	content := fmt.Sprintf("---\ndescription: %s\nalwaysApply: false\n---\n\n%s", yamlScalar(fm.Description), body)
	dest := filepath.Join(projectRoot, ".cursor", "rules", sk.Name+".mdc")
	return "", writeIfChanged(dest, []byte(content))
}

// yamlScalar quotes a value so descriptions containing ": ", "#", or a leading
// YAML indicator can't produce an unparseable frontmatter block.
func yamlScalar(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", " ").Replace(s) + `"`
}

func (cursorAdapter) Remove(name, projectRoot string) error {
	if projectRoot == "" {
		return nil
	}
	return os.Remove(filepath.Join(projectRoot, ".cursor", "rules", name+".mdc"))
}

// codexAdapter maintains a managed section in AGENTS.md (global: ~/.codex/,
// project: repo root). Content outside the markers is preserved.
type codexAdapter struct{}

const (
	codexStart = "<!-- skillsync:start — managed by skillsync, do not edit -->"
	codexEnd   = "<!-- skillsync:end -->"
)

// Per-skill markers delimit each block. Slicing on markdown headings would
// break on any h3 inside a skill body and orphan the remainder on every sync.
func codexSkillStart(name string) string { return "<!-- skillsync:skill=" + name + " -->" }

const codexSkillEnd = "<!-- skillsync:/skill -->"

func (codexAdapter) Install(sk Skill, projectRoot string, _ map[string]InstalledSkill) (string, error) {
	base := filepath.Join(homeDir(), ".codex")
	if projectRoot != "" {
		base = projectRoot
	}
	fm, body, err := readSkillMD(sk.Dir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(base, "AGENTS.md")
	existing, _ := os.ReadFile(path)
	section := fmt.Sprintf("%s\n### %s\n%s\n\n%s\n%s\n",
		codexSkillStart(sk.Name), sk.Name, fm.Description, strings.TrimSpace(body), codexSkillEnd)
	updated := upsertManagedSection(string(existing), sk.Name, section)
	return "", writeIfChanged(path, []byte(updated))
}

func (codexAdapter) Remove(name, projectRoot string) error {
	base := filepath.Join(homeDir(), ".codex")
	if projectRoot != "" {
		base = projectRoot
	}
	path := filepath.Join(base, "AGENTS.md")
	doc, err := os.ReadFile(path)
	if err != nil {
		return nil // nothing written for this tool
	}
	s := string(doc)
	startIdx := strings.Index(s, codexStart)
	endIdx := strings.Index(s, codexEnd)
	if startIdx < 0 || endIdx < startIdx {
		return nil
	}
	inner := removeSkillBlock(s[startIdx+len(codexStart):endIdx], name)
	return writeIfChanged(path, []byte(s[:startIdx]+codexStart+inner+codexEnd+s[endIdx+len(codexEnd):]))
}

// upsertManagedSection inserts or replaces one skill's block inside the
// skillsync-managed region of an AGENTS.md, creating the region if absent.
// Content outside the region markers is never touched.
func upsertManagedSection(doc, skillName, section string) string {
	startIdx := strings.Index(doc, codexStart)
	endIdx := strings.Index(doc, codexEnd)
	if startIdx < 0 || endIdx < startIdx {
		// No managed region yet: append one.
		region := codexStart + "\n" + section + codexEnd + "\n"
		if doc != "" && !strings.HasSuffix(doc, "\n") {
			doc += "\n"
		}
		return doc + region
	}
	inner := removeSkillBlock(doc[startIdx+len(codexStart):endIdx], skillName)
	if inner != "" && !strings.HasSuffix(inner, "\n") {
		inner += "\n"
	}
	inner += section
	return doc[:startIdx] + codexStart + "\n" + strings.TrimPrefix(inner, "\n") + codexEnd + doc[endIdx+len(codexEnd):]
}

// removeSkillBlock strips one skill's marker-delimited block from a managed
// region, leaving other skills' blocks untouched.
func removeSkillBlock(inner, skillName string) string {
	marker := codexSkillStart(skillName)
	i := strings.Index(inner, marker)
	if i < 0 {
		return inner
	}
	rest := inner[i+len(marker):]
	end := strings.Index(rest, codexSkillEnd)
	if end < 0 {
		return inner[:i] // truncated block: drop the remainder
	}
	return inner[:i] + strings.TrimPrefix(rest[end+len(codexSkillEnd):], "\n")
}

type frontmatter struct {
	Name        string
	Description string
}

// readSkillMD parses SKILL.md's YAML frontmatter (name/description only —
// the two fields the skills.sh convention guarantees) and returns the body.
func readSkillMD(dir string) (frontmatter, string, error) {
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return frontmatter{}, "", err
	}
	text := string(b)
	var fm frontmatter
	body := text
	if strings.HasPrefix(text, "---\n") {
		if end := strings.Index(text[4:], "\n---"); end >= 0 {
			for _, line := range strings.Split(text[4:4+end], "\n") {
				if v, ok := strings.CutPrefix(line, "name:"); ok {
					fm.Name = strings.TrimSpace(v)
				}
				if v, ok := strings.CutPrefix(line, "description:"); ok {
					fm.Description = strings.TrimSpace(v)
				}
			}
			body = strings.TrimPrefix(text[4+end+4:], "\n")
		}
	}
	return fm, body, nil
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
