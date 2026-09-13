# Changelog

## 0.4.0

### Minor Changes

- 42e2a5e: Add `skillsync feedback <skill>`: opens a GitHub issue on the skill's source repo with what went wrong, a proposed SKILL.md change, and a ready-to-append eval case, so a fix can be adopted with a test that proves it.
- 392bfc3: Add an `omp` adapter: `--tools omp` installs into `~/.omp/agent/skills` (and `.omp/skills` per project), where omp's native provider outranks the `~/.claude/skills` copies. omp has no `skillOverrides` map, so exposure rides in the installed copy's frontmatter as `hide: true` — not listed to the model, still loaded, still reachable by `/skill:<name>`. Exposure is now decided before installing rather than after, hubs are generated into every enabled tool's skills directory with paths into that directory, and the install record keeps a per-tool output hash so a rewritten copy is not mistaken for local drift.

### Patch Changes

- 392bfc3: `skillsync upgrade` and the session-start notice now compare versions by semver precedence instead of string equality. A binary built from source reports a module pseudo-version that sorts ahead of the tag it came from, so both used to treat the newest release as an upgrade and would replace a newer binary with an older one.

## 0.3.0

### Minor Changes

- Installed is no longer the same as listed. Claude Code loads every installed skill's description into context on every turn and truncates past ~1% of the window, so a hundred synced skills meant lost trigger phrases. Every skill stays installed and runnable by slash command; skillsync now controls which ones Claude can trigger on its own.

  - `metadata.exposure: auto | on-demand` in `SKILL.md` frontmatter declares whether a skill triggers from its description. Absent means `on-demand`. Spec-legal key, so the file still uploads to claude.ai and the Skills API.
  - `skillsync sync` writes Claude Code's `skillOverrides` map in `~/.claude/settings.json` after installing: `auto` → `on`, `on-demand` → `user-invocable-only`. Only managed skills are touched; the file is rewritten only when an entry changes.
  - Per-machine usage rules from the invocation log: an `on-demand` skill invoked 3 or more times in 30 days is listed here; an `auto` skill unused for 90 days drops to `name-only`. Changing a tier in the repo resets both clocks. Thresholds live in the new `exposure` block of `config.json`; `init` writes the defaults out.
  - Hub skills: a prefix family (`marketing-*`, `design-*`) with 8 or more hidden members gets one generated router skill named after the prefix, listed in place of its members, built from their frontmatter. `exposure.hub_min_members: 0` disables hubs.
  - `skillsync check` warns when the listed descriptions still exceed Claude Code's budget and names the largest contributors. Honors `skillListingBudgetFraction` and `SLASH_COMMAND_TOOL_CHAR_BUDGET`. The session-start hook runs `check`, so the warning appears in the session.
  - `skillsync list` gains `TIER`, `CLAUDE SEES`, and `LAST USED` columns.
  - `skillsync uninstall` removes every `skillOverrides` entry it wrote, and generated hubs unless `--keep-skills`.
  - The built-in `skillsync` skill is marked `auto`.
  - Versioning and this changelog are managed with changesets.

  Not in this release: Cursor and Codex have no overrides setting, so on-demand skills install there as before. `skillsync.yaml` keeps its install-list semantics; project-level promotion is a hand-written `.claude/settings.local.json`.

## 0.2.0

Released 2026-08-30, before changesets; written from the commit history.

### Minor Changes

- `skillsync upgrade` fetches the latest GitHub release for this OS and architecture, verifies it against `checksums.txt`, and replaces the running binary. `skillsync version` reports the version stamped by the release build. Sync records the newest tag at most once a day, and the offline `check` hook prints a one-line notice when the installed version is behind.
- `skillsync init --tools claude-code,cursor,codex` picks agent tools at init instead of hand-editing `config.json`; unknown names are rejected. `init` names each step it skipped or that failed and ends with the `auto status` block.
- A `skillsync` skill ships inside the binary and is installed on every sync as the lowest-precedence source `builtin`, so agents know how to set up a machine, edit skills in the repo, and verify delivery. A team skill of the same name overrides it.

### Patch Changes

- `stats` counts only skills installed from a source repo, and its hint no longer tells users with hooks installed to install hooks.

## 0.1.0

Released 2026-08-21.

### Minor Changes

- First release: `init`, `sync`, `list`, `add`, `remove`, `adopt`, `stats`, `auto`, `uninstall`; adapters for Claude Code, Cursor, and Codex in global and project scope; background sync via launchd, systemd user timer, or Task Scheduler; Claude Code hooks for session-start sync and usage tracking; multi-source precedence with per-source pin; drift detection and detect-and-adopt for hand-installed skills; local usage stats with optional HTTPS sink; release binaries for darwin, linux, and windows on amd64 and arm64.
