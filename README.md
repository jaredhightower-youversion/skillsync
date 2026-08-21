# skillsync

Team skill directory with background auto-sync. Edit a skill once in your team's git repo —
every teammate's machine updates automatically across Claude Code, Cursor, and Codex.
No update command, no drift, with usage metrics to show which skills earn their keep.

See [SHAPING.md](SHAPING.md) for the full design and [SPEC.md](SPEC.md) for the decision record.

## Install

Download the binary for your platform from
[Releases](../../releases/latest), then:

```sh
chmod +x skillsync-darwin-arm64
sudo mv skillsync-darwin-arm64 /usr/local/bin/skillsync
```

Or with Go installed:

```sh
go install github.com/OWNER/skillsync@latest
```

## Setup (one time, per machine)

```sh
skillsync init git@github.com:your-org/skills.git
```

That syncs your skills, registers a background job that re-syncs every 15 minutes, and adds
Claude Code hooks for session-start sync and usage tracking. Skip either piece with
`--no-daemon` or `--no-hooks`.

Done. Skills appear in `~/.claude/skills/` and update on their own.

## Team skill repo

Any git repo where each skill is a directory containing a `SKILL.md`
([skills.sh](https://www.skills.sh) conventions — repos work with `npx skills add` too):

```
skills/
  code-review/SKILL.md
  deploy-checklist/SKILL.md
```

Edit a skill, merge the PR — everyone has the new version within 15 minutes.

## Project skills

Commit a `skillsync.yaml` at the project root; everyone syncing in that project
gets these skills project-locally:

```yaml
skills:
  - code-review
  - deploy-checklist
```

## Commands

| Command | What it does |
|---|---|
| `skillsync sync` | Sync now (global + current project) |
| `skillsync list` | Skills with install status, when each last changed, and who changed it |
| `skillsync add / remove <skill>` | Manage global subscriptions (default: all) |
| `skillsync adopt <skill>` | Replace a hand-installed skill with the managed version |
| `skillsync stats` | Local skill-usage counts |
| `skillsync daemon status` | Background job status |
| `skillsync hook print` | Hook JSON for hand-managed Claude Code settings |
| `skillsync uninstall` | Remove skillsync and everything it installed |

`skillsync list` answers "what changed lately", newest first:

```
STATUS     SKILL                        UPDATED      BY               SOURCE
installed  deploy-checklist             2h ago       Grace Hopper     team
installed  code-review                  6d ago       Ada Lovelace     org
available  release-notes                2026-05-01   Ada Lovelace     org
```

Dates come from the skill repo's git history, so they reflect when a skill was actually
edited, not when your machine last synced.

## Configuration

`~/.skillsync/config.json`:

```json
{
  "sources": [
    {"name": "org",  "url": "git@github.com:acme/org-skills.git"},
    {"name": "team", "url": "git@github.com:acme/team-skills.git", "pin": "v2.1.0"}
  ],
  "tools": ["claude-code", "cursor", "codex"],
  "metrics": {"enabled": true, "endpoint": "https://metrics.internal/skills"}
}
```

- **sources** — precedence-ordered; first source wins name collisions. `pin` (tag/commit)
  freezes a source for change control; omit to track the default branch.
- **tools** — adapter targets. Default: `claude-code` only.
- **metrics.endpoint** — optional sink for usage events; must be `https://` (cleartext is
  refused, since events name what you are working on). Omit for local-only stats.

## ⚠️ Local edits get overwritten

Skills installed by skillsync are **managed files**. If you edit one on your machine
(e.g. `~/.claude/skills/code-review/SKILL.md`), the next background sync — within 15
minutes — **replaces it with the team version and your changes are lost**. Sync overrides;
it never merges. A warning is printed, but background syncs only log it to
`~/.skillsync/daemon.log`, so you may not see it before the overwrite.

This is deliberate: silent local forks are the drift problem this tool exists to kill.

What to do instead:

- **Improve a skill for everyone** — edit it in the team skill repo and merge a PR.
  Every machine gets it within 15 minutes.
- **Keep a personal variant** — copy the skill directory under a new name
  (e.g. `code-review-mine`). Unmanaged names are never touched.
- **Skills you installed by hand** (before skillsync, or via `npx skills add`) are safe:
  same content gets adopted into management; different content is never overwritten unless
  you explicitly run `skillsync adopt <skill>`.

## Removing skills, or removing skillsync

### One skill, just for you

```sh
skillsync remove code-review   # unsubscribe
skillsync sync                 # uninstalls it from your machine
```

Your first `remove` turns "everything in the sources" into an explicit list minus that skill,
so later syncs won't quietly reinstall it. Re-subscribe with `skillsync add code-review`.

### One skill, for the whole team

Delete it from the skill repo (or drop it from a project's `skillsync.yaml`) and merge.
Every machine uninstalls it on the next sync.

### Everything skillsync installed

```sh
skillsync uninstall                  # skills, background job, hooks, and ~/.skillsync
skillsync uninstall --keep-skills    # same, but leave the skill files on disk
```

This removes only skills that skillsync installed — anything you put in
`~/.claude/skills/` yourself is left alone. Then delete the binary:

```sh
sudo rm /usr/local/bin/skillsync
```

### Removing pieces individually

```sh
skillsync daemon uninstall   # stop background syncing, keep everything else
skillsync hook uninstall     # remove the Claude Code hooks, keep the skills
```

`hook uninstall` only strips skillsync's own entries from `~/.claude/settings.json`; your
other hooks and settings stay as they are.

## Behavior notes

- Project-local skills shadow a global skill of the same name; sync installs both and
  reports the shadowing (agent tools prefer the project copy).
- Unsubscribing a skill, or dropping it from `skillsync.yaml`, uninstalls it on the next sync.
- Source URLs must be `https://`, `ssh://`, `git://`, `file://`, or `user@host:path`. Git
  transport helpers (`ext::`) are refused: they run shell commands.
- Windows is supported: background sync registers with Task Scheduler. Claude Code hooks
  and the git credentials work the same as elsewhere.

## Release

Tag and push — CI cross-compiles and attaches binaries:

```sh
git tag v0.1.0 && git push origin v0.1.0
```
