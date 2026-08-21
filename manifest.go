package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const manifestName = "skillsync.yaml"

// readManifest parses the project manifest (SPEC §4). Deliberately a YAML
// subset (comments plus one list) so we stay stdlib-only:
//
//	# team skills for this project
//	skills:
//	  - code-review
//	  - deploy-checklist
//
// ponytail: subset parser; swap for a YAML library if manifests grow fields.
func readManifest(path string) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var names []string
	inSkills := false
	for _, line := range strings.Split(string(b), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, "skills:") {
			inSkills = true
			continue
		}
		if inSkills && strings.HasPrefix(trimmed, "- ") {
			names = append(names, strings.TrimSpace(trimmed[2:]))
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "-") {
			inSkills = false // new top-level key ends the list
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no skills listed under `skills:` in %s", path)
	}
	return names, nil
}

// findProjectRoot walks up from dir looking for skillsync.yaml.
func findProjectRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, manifestName)); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}
