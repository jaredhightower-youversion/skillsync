package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

// Metrics (SPEC §8): exact events from Claude Code's PostToolUse Skill hook,
// appended locally as JSONL, batched to a configurable HTTPS sink during sync.
// No endpoint configured = local-only stats. Cursor/Codex approximate
// log-parsing is a spec open item — not implemented.

type usageEvent struct {
	Skill string    `json:"skill"`
	Tool  string    `json:"tool"`
	Time  time.Time `json:"time"`
}

func cmdTrack(args []string) {
	var name string
	if len(args) == 1 && args[0] == "--stdin" {
		// Claude Code hook payload: {"tool_input": {"skill": "<name>"}, ...}
		var payload struct {
			ToolInput struct {
				Skill string `json:"skill"`
			} `json:"tool_input"`
		}
		if err := json.NewDecoder(io.LimitReader(os.Stdin, 1<<20)).Decode(&payload); err != nil || payload.ToolInput.Skill == "" {
			return // unparseable hook payload: drop silently, never break the user's session
		}
		name = payload.ToolInput.Skill
	} else if len(args) == 1 {
		name = args[0]
	} else {
		usage()
	}
	ev := usageEvent{Skill: name, Tool: "claude-code", Time: time.Now().UTC()}
	if err := os.MkdirAll(stateDir(), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(eventsPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	b, _ := json.Marshal(ev)
	f.Write(append(b, '\n'))
}

func readEvents() []usageEvent {
	f, err := os.Open(eventsPath())
	if err != nil {
		return nil
	}
	defer f.Close()
	var events []usageEvent
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev usageEvent
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			events = append(events, ev)
		}
	}
	return events
}

func cmdStats() {
	events := readEvents()
	if len(events) == 0 {
		fmt.Println("no usage events recorded yet (install hooks: skillsync hook install)")
		return
	}
	counts := map[string]int{}
	for _, ev := range events {
		counts[ev.Skill]++
	}
	type row struct {
		name  string
		count int
	}
	rows := make([]row, 0, len(counts))
	for n, c := range counts {
		rows = append(rows, row{n, c})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].count > rows[j].count })
	fmt.Printf("%-30s %s\n", "SKILL", "USES")
	for _, r := range rows {
		fmt.Printf("%-30s %d\n", r.name, r.count)
	}
}

// flushMetrics batches queued events to the configured sink; on success the
// local queue is cleared. Endpoint failures leave the queue intact for the
// next sync tick.
func flushMetrics(cfg *Config) {
	if !cfg.Metrics.Enabled || cfg.Metrics.Endpoint == "" {
		return
	}
	events := readEvents()
	if len(events) == 0 {
		return
	}
	body, err := json.Marshal(events)
	if err != nil {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(cfg.Metrics.Endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "skillsync: metrics flush failed (kept locally): %v\n", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		fmt.Fprintf(os.Stderr, "skillsync: metrics sink returned %s (kept locally)\n", resp.Status)
		return
	}
	os.Remove(eventsPath())
}
