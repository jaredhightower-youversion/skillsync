package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"
)

// Self-upgrade. Skills update themselves, so the binary that updates them
// should too. Two pieces:
//
//	skillsync upgrade   downloads the latest GitHub release for this OS/arch,
//	                    verifies it against checksums.txt, swaps it in place
//	sync + check        sync (already on the network) records the latest tag
//	                    once a day; the offline session-start check then prints
//	                    a one-line "upgrade available" notice in the session
//
// version is stamped by release.yml via -ldflags "-X main.version=<tag>".
// `go install` builds fall back to the module version from build info.
var version = "dev"

const releaseRepo = "jaredhightower-youversion/skillsync"

// releaseAPI and upgradeClient are package vars so tests can point them at a
// local test server.
var (
	releaseAPI    = "https://api.github.com/repos/" + releaseRepo + "/releases/latest"
	upgradeClient = &http.Client{Timeout: 2 * time.Minute}
)

const updateCheckEvery = 24 * time.Hour

func currentVersion() string {
	if version != "dev" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return version
}

func cmdVersion() { fmt.Println("skillsync", currentVersion()) }

type release struct {
	Tag    string            // e.g. v0.2.0
	Assets map[string]string // asset name -> download URL
}

func latestRelease() (*release, error) {
	resp, err := upgradeClient.Get(releaseAPI)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("parse release: %w", err)
	}
	r := &release{Tag: body.TagName, Assets: map[string]string{}}
	for _, a := range body.Assets {
		r.Assets[a.Name] = a.URL
	}
	return r, nil
}

// assetName matches the naming in release.yml's cross-compile step.
func assetName() string {
	name := fmt.Sprintf("skillsync-%s-%s", runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

func cmdUpgrade() {
	rel, err := latestRelease()
	if err != nil {
		fatal("check latest release: %v", err)
	}
	cur := currentVersion()
	if rel.Tag == cur {
		fmt.Printf("skillsync %s is already the latest release\n", cur)
		return
	}
	if err := upgradeBinary(binaryPath(), rel); err != nil {
		fatal("upgrade to %s: %v", rel.Tag, err)
	}
	fmt.Printf("upgraded skillsync %s -> %s\n", cur, rel.Tag)
	// Clear the notice immediately; the next sync would do it anyway.
	state := loadState()
	state.Update = UpdateInfo{Latest: rel.Tag, CheckedAt: time.Now().UTC()}
	saveJSON(statePath(), state)
}

// upgradeBinary downloads the asset for this platform, verifies its sha256
// against the release's checksums.txt, and atomically replaces target with it.
func upgradeBinary(target string, rel *release) error {
	name := assetName()
	url, ok := rel.Assets[name]
	if !ok {
		return fmt.Errorf("release %s has no asset %s", rel.Tag, name)
	}
	sums, ok := rel.Assets["checksums.txt"]
	if !ok {
		return fmt.Errorf("release %s has no checksums.txt; refusing to install an unverified binary", rel.Tag)
	}
	want, err := expectedChecksum(sums, name)
	if err != nil {
		return err
	}

	// Download next to the target so the final rename stays on one filesystem.
	tmp := target + ".new"
	if err := download(url, tmp, 0o755); err != nil {
		return err
	}
	defer os.Remove(tmp)
	if got, err := fileSHA256(tmp); err != nil {
		return err
	} else if got != want {
		return fmt.Errorf("checksum mismatch for %s: got %s, want %s", name, got, want)
	}
	return replaceBinary(target, tmp)
}

// replaceBinary moves tmp over target. Windows cannot overwrite a running
// executable but can rename it, so the old file is moved aside first.
func replaceBinary(target, tmp string) error {
	if runtime.GOOS == "windows" {
		old := target + ".old"
		os.Remove(old)
		if err := os.Rename(target, old); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, target); err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("%w\n  %s is not writable by you; rerun with sudo", err, filepath.Dir(target))
		}
		return err
	}
	return nil
}

func download(url, dest string, mode os.FileMode) error {
	resp, err := upgradeClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("%w\n  %s is not writable by you; rerun with sudo", err, filepath.Dir(dest))
		}
		return err
	}
	_, copyErr := io.Copy(f, resp.Body)
	if closeErr := f.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return copyErr
}

// expectedChecksum finds name in a `shasum -a 256` listing (hex, spaces, name).
func expectedChecksum(url, name string) (string, error) {
	resp, err := upgradeClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == name {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt has no entry for %s", name)
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// recordLatestVersion asks GitHub for the newest tag at most once a day and
// stores it in state, so the offline `check` can mention it. Any failure is
// ignored: a sync must never fail because GitHub was unreachable.
func recordLatestVersion(state *State) {
	if time.Since(state.Update.CheckedAt) < updateCheckEvery {
		return
	}
	rel, err := latestRelease()
	if err != nil {
		return
	}
	state.Update = UpdateInfo{Latest: rel.Tag, CheckedAt: time.Now().UTC()}
}

// upgradeNotice returns a one-line hint when a newer release is known, or "".
// Dev builds are never nagged: they are ahead of, not behind, the releases.
func upgradeNotice(state *State) string {
	cur := currentVersion()
	if cur == "dev" || state.Update.Latest == "" || state.Update.Latest == cur {
		return ""
	}
	return fmt.Sprintf("skillsync %s is available (you have %s). Upgrade: skillsync upgrade", state.Update.Latest, cur)
}
