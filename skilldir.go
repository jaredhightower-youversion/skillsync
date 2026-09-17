package main

// Helpers for skill directories on disk: frontmatter, content hashing, atomic copy.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// skillFrontmatter reads description and metadata.exposure from a SKILL.md.
// A YAML subset: `key: value`, `>`/`|` block scalars, and one nested map.
// ponytail: subset parser, same trade as readManifest; swap for a YAML library
// if more frontmatter fields start to matter.
func skillFrontmatter(path string) (description, exposure string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ""
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", ""
	}
	var fm []string
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "---" {
			break
		}
		fm = append(fm, l)
	}
	top := ""
	for i := 0; i < len(fm); i++ {
		line := fm[i]
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !indented {
			top = key
		}
		switch {
		case !indented && key == "description":
			description, i = yamlScalar(fm, i, val)
		case indented && top == "metadata" && key == "exposure":
			exposure = unquote(val)
		}
	}
	return description, exposure
}

// yamlScalar returns the value starting at line i: inline, or a `>`/`|` block
// gathered from the indented lines that follow. Returns the last line consumed.
func yamlScalar(lines []string, i int, inline string) (string, int) {
	if !strings.HasPrefix(inline, ">") && !strings.HasPrefix(inline, "|") {
		return unquote(inline), i
	}
	sep := " "
	if inline[0] == '|' {
		sep = "\n"
	}
	var parts []string
	for i+1 < len(lines) {
		next := lines[i+1]
		if strings.TrimSpace(next) != "" && next[0] != ' ' && next[0] != '\t' {
			break
		}
		i++
		parts = append(parts, strings.TrimSpace(next))
	}
	return strings.TrimSpace(strings.Join(parts, sep)), i
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// hashDir is a content hash over sorted relative paths + file bytes.
func hashDir(dir string) (string, error) {
	h := sha256.New()
	var files []string
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		rel, _ := filepath.Rel(dir, f)
		fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		file, err := os.Open(f)
		if err != nil {
			return "", err
		}
		_, err = io.Copy(h, file)
		file.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyDir replaces dest with the skill's directory, transform applied (atomic
// replace per skill: stage next to dest, then rename over).
func copyDir(sk Skill, dest string, tf transform) error {
	staging := dest + ".skillsync-tmp"
	os.RemoveAll(staging)
	// Directories first, so empty ones survive the copy too.
	err := filepath.WalkDir(sk.Dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(sk.Dir, path)
		return os.MkdirAll(filepath.Join(staging, rel), 0o755)
	})
	if err == nil {
		err = walkSkill(sk, tf, func(rel string, content []byte) error {
			return os.WriteFile(filepath.Join(staging, filepath.FromSlash(rel)), content, 0o644)
		})
	}
	if err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		os.RemoveAll(staging)
		return err
	}
	return os.Rename(staging, dest)
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

// hashDirWith hashes what an adapter's transform would write for a skill, in
// the same format as hashDir, so the two are comparable: a transforming
// adapter compares its expected output against the hash of what is on disk.
func hashDirWith(sk Skill, tf transform) (string, error) {
	h := sha256.New()
	err := walkSkill(sk, tf, func(rel string, content []byte) error {
		fmt.Fprintf(h, "%s\x00", rel)
		h.Write(content)
		h.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
