# SkillSync — Team Skill Directory with Auto-Sync

## Problem
Teams share agent skills (SKILL.md-style instructions) across members and tools. Today distribution is manual copy-paste: skills drift, nobody knows who has which version, and there is no visibility into which skills earn their keep. Goal: edit a skill once, every team member's machine updates automatically in the background — no update command, ever.

## Audience
Independent contributors, startups, enterprise. Not everyone uses Claude Code — must work across agent tools.

## Decisions (settled via grilling session, 2026-08-20)

### 1. Source of truth: git repo
One or more git repos hold all skills. Versioning, PR review, rollback, and auth (existing git credentials / SSO) come free. No hosted registry.

### 2. Distribution: custom sync CLI + daemon (agent-agnostic)
Not built on Claude Code's plugin marketplace — team members use other tools too.

- Installed CLI (`skillsync`) registers an OS background job (launchd / systemd / Windows Task Scheduler) that polls sources every N minutes.
- Additionally a fast just-in-time check at agent session start, via hook where the tool supports it (Claude Code SessionStart hook; wrappers elsewhere).
- No user-facing update command required; one exists anyway for debugging (`skillsync sync`).

### 3. Cross-tool: one canonical format + adapters
Repo stores skills in a canonical format: frontmatter (name, description, metadata) + markdown body — **exactly skills.sh-flavored SKILL.md** (vercel-labs/skills conventions). Interop goal: skills authored for SkillSync install cleanly via `npx skills add` and vice versa. We compete on sync/scope/metrics, not on format.

Adapters translate/install into each tool's expected location and shape:

| Tool | Target (global / project) |
|------|--------|
| Claude Code | `~/.claude/skills/` / `.claude/skills/` |
| Cursor | `~/.cursor/skills/` / `.cursor/skills/` |
| OpenAI Codex CLI | `~/.agents/skills/` / `.agents/skills/` |

**Revised 2026-08-20.** All three tools read `SKILL.md` directories natively, so every adapter
is a plain copy — no format translation, and all share the §5/§10 drift/adopt handling. The
earlier design (Cursor `.mdc` rules, Codex managed section in `AGENTS.md`) was wrong on both
counts: it was lossy, and Cursor's global scope silently produced no files at all. Codex paths
per OpenAI's skills docs (learn.chatgpt.com/docs/build-skills, checked 2026-08-20); `AGENTS.md`
is a separate mechanism for general instructions and is left untouched.

Unverified: whether Codex still reads the older `~/.codex/skills/` as a legacy fallback. We
write only the documented location.

v1 targets: Claude Code, Cursor, Codex CLI. Copilot later. Adapter interface is the extension point for new tools.

### 4. Scope: project manifest + user subscription
- **Project-local:** project commits `skillsync.yaml` listing skills the project needs. Anyone syncing inside that project gets them project-local automatically — team guarantee.
- **Global:** each user subscribes to skills for their own global config (`skillsync add <skill>`), stored in user config.
- **Dedup:** project-local wins. Sync detects a skill present in both scopes for a project and skips/removes the shadowed global copy in that project context so the agent never loads two versions.

### 5. Conflicts: synced skills are managed artifacts
Sync overwrites local edits to installed skill files. On detecting drift: warn once and offer `skillsync fork <skill>` (unmanaged local copy) or `skillsync propose <skill>` (opens branch/PR upstream). Real edits happen in the repo — that is how "edit updates everyone" holds.

### 6. Versioning: branch head + optional pin
- Default: track source repo's default-branch head. Merged edit reaches every machine on next tick.
- Enterprise change control: config/manifest can pin a source or an individual skill to a tag/commit.
- Rollback = git revert upstream.

### 7. Sources: multi-source from v1
Config lists N git repos with precedence order (e.g. org repo > team repo > personal repo > community repo). Name collisions resolved by precedence. Auth = existing git credentials; nothing custom.

### 8. Metrics: tiered detection, pluggable sink
- **Detection:**
  - Claude Code: hook fires on Skill invocation — exact per-skill usage counts.
  - Cursor / Codex: best-effort parsing of local session logs/history; reported as approximate.
  - No beacons injected into skill content.
- **Sink:** daemon batches events locally, POSTs to a configurable HTTPS endpoint. Ship a tiny reference collector (or document PostHog self-host). Indie default: local-only stats via `skillsync stats`. Enterprise: self-hosted sink. Telemetry opt-in/out set in org config.
- **Core metric:** most-used skills, per team and per tool, exact vs approximate flagged.

### 9. Stack: Go single static binary
No runtime dependency on dev machines — critical for daemon reliability across non-JS teams. Shell out to system git (fall back to go-git where absent). Distribute via brew / scoop / curl installer. Cross-compile macOS, Linux, Windows.

## Alternatives — resolved (approach research, 2026-08-20)
- **Wrap skills.sh CLI instead of building:** rejected as foundation (no daemon, no team manifest/scope/dedup, Node dependency, telemetry goes to Vercel's public leaderboard). But its SKILL.md format and per-tool target-directory map are adopted (see §3) — interop over competition.
- **Hosted SaaS (rulesync.dev-style):** deferred, not rejected. Out of scope v1. Trigger to revisit: teams asking for hosted dashboards / managed metrics sink. Candidate monetization layer on top of the free CLI/daemon.

## Non-goals (v1)
- Hosted SaaS registry or dashboards (deferred — see Alternatives)
- Three-way merge of local skill edits
- GitHub Copilot adapter (v2)
- Skill marketplace / discovery UI beyond `skillsync list`

### 10. skills.sh interop depth: detect + conditional adopt (decided 2026-08-20)
On sync, adapters scan target dirs for name collisions with incoming managed skills:
- **Hash matches a known version from source repo** → adopt as managed; auto-sync from then on. (Dev who used `npx skills add` earlier upgrades seamlessly.)
- **Same name, different content** → pre-existing local fork. Never overwrite; warn once, offer `skillsync adopt <skill>` (replace with managed) or rename. Skill skipped on that machine until resolved.
- **No collision** → normal install.

Rationale: managed-overwrite (§5) applies only to files SkillSync installed; hash match is proof of equivalence, so adoption is safe. Uses same per-skill install-hash machinery as drift detection.

## Implementation status (2026-08-20)
All six build slices implemented and verified (unit tests + end-to-end smoke): CLI (`init/sync/sync-all/list/add/remove/adopt`), daemon (launchd + systemd user timer), Claude Code hooks (`hook install`: SessionStart sync + PostToolUse Skill tracking), adapters (Claude Code passthrough, Cursor `.mdc` project rules, Codex managed AGENTS.md section), metrics (`track`/`stats` + HTTPS sink flush), multi-source precedence + per-source pin. Codex adapter decision: managed marker section in AGENTS.md, user content preserved.

## Deferred / open
- Windows daemon: implemented via Task Scheduler; untested on real hardware
- Cursor/Codex approximate usage metrics via session-log parsing — feasibility unproven
- `skillsync propose` (open upstream PR from local edit) — only `fork`-by-rename + `adopt` exist
- Reference metrics collector: build tiny one vs document PostHog
