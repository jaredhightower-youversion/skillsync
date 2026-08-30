package main

import (
	_ "embed"
	"path/filepath"
)

// The skillsync skill teaches an agent how to work with this tool: set a
// machine up without disabling auto-update, edit skills in the repo rather
// than the installed copy, and verify delivery. It ships inside the binary so
// every machine has it from the first sync, with nothing extra to install.

//go:embed skill/SKILL.md
var builtinSkillMD []byte

const builtinSkillName = "skillsync"
const builtinSource = "builtin"

// withBuiltin appends the embedded skill to a resolved list unless a source
// repo already provides one under the same name, giving the built-in the
// lowest precedence so a team can override it. The skill directory is
// materialized under the state dir because adapters copy directories.
func withBuiltin(resolved []Skill) ([]Skill, error) {
	for _, sk := range resolved {
		if sk.Name == builtinSkillName {
			return resolved, nil
		}
	}
	dir := filepath.Join(stateDir(), builtinSource, builtinSkillName)
	if err := writeIfChanged(filepath.Join(dir, "SKILL.md"), builtinSkillMD); err != nil {
		return nil, err
	}
	return append(resolved, Skill{Name: builtinSkillName, Dir: dir, Source: builtinSource}), nil
}
