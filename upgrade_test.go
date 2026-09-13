package main

import (
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"v0.4.0", "v0.4.0", 0},
		{"v0.4.0", "v0.3.0", 1},
		{"v0.3.0", "v0.4.0", -1},
		{"v0.10.0", "v0.9.0", 1},
		{"v1.0.0", "v1.0", 0},
		// A build from source: pseudo-version of the next patch, plus build
		// metadata. It is ahead of the tag it was built from, not behind it.
		{"v0.3.0", "v0.3.1-0.20260912020005-42e2a5eaf0fb+dirty", -1},
		{"v0.4.0", "v0.3.1-0.20260912020005-42e2a5eaf0fb+dirty", 1},
		{"v1.0.0", "v1.0.0-rc.1", 1},
		{"v1.0.0-rc.2", "v1.0.0-rc.10", -1},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},
		{"v1.0.0-1", "v1.0.0-alpha", -1},
		{"v1.0.0+meta", "v1.0.0", 0},
		{"not-a-version", "v0.1.0", -1},
	}
	for _, c := range cases {
		if got := compareVersions(c.a, c.b); got != c.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

// The notice must never point a build at an older release: acting on it
// replaces a binary that knows about newer tools and settings with one that
// does not, and the skills it was maintaining go stale silently.
func TestUpgradeNoticeNeverSuggestsADowngrade(t *testing.T) {
	t.Cleanup(func() { version = "dev" })
	state := &State{Update: UpdateInfo{Latest: "v0.3.0", CheckedAt: time.Now()}}

	version = "v0.3.1-0.20260912020005-42e2a5eaf0fb+dirty"
	if notice := upgradeNotice(state); notice != "" {
		t.Errorf("build ahead of the latest release should not be nagged, got %q", notice)
	}

	version = "v0.2.0"
	if notice := upgradeNotice(state); notice == "" {
		t.Error("an actually outdated build should be told about the newer release")
	}
}
