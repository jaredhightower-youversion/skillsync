# skillsync

Team skill directory with background auto-sync. Edit a skill once in your team's git repo , 
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
([skills.sh](https://www.skills.sh) conventions, repos work with `npx skills add` too):

```
skills/
  code-review/SKILL.md
  deploy-checklist/SKILL.md
```

Edit a skill, merge the PR, everyone has the new version within 15 minutes.

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
| `skillsync init <git-url>` | Set up this machine (see above); `--no-daemon` / `--no-hooks` to skip a part |
| `skillsync sync` | Sync now |
| `skillsync list` | Skills with install status, when each last changed, and who changed it |
| `skillsync add / remove <skill>` | Manage global subscriptions (default: all) |
| `skillsync adopt <skill>` | Replace a hand-installed skill with the managed version |
| `skillsync stats` | How often each skill has been used on this machine |
| `skillsync auto on / off / status` | Automatic updating: the background job and Claude Code hooks |
| `skillsync uninstall` | Remove skillsync and everything it installed |

Realistically you type `init` once and `list` or `stats` occasionally; everything else is for
when something needs checking.

`skillsync list` answers "what changed lately", newest first:

```
STATUS     SKILL                        UPDATED      BY               SOURCE
installed  deploy-checklist             2h ago       Grace Hopper     team
installed  code-review                  6d ago       Ada Lovelace     org
available  release-notes                2026-05-01   Ada Lovelace     org
```

Dates come from the skill repo's git history, so they reflect when a skill was actually
edited, not when your machine last synced.

## Which skills actually get used

`skillsync stats` shows how often each skill has been invoked on your machine:

```
SKILL                          USES
code-review                    24
deploy-checklist               9
release-notes                  1
```

Two things to know about the numbers:

- **They need the hooks.** Counting happens through the Claude Code hook that `init` installs.
  If you ran `init --no-hooks`, or removed them, stats stay empty. `skillsync hook install`
  turns counting on later.
- **Claude Code only.** Claude Code reports skill invocations, so those counts are exact.
  Cursor and Codex don't expose an equivalent signal, so skills used there are not counted.
  A team living mostly in Cursor will see numbers that undercount real usage.

By default the counts never leave your machine. Set `metrics.endpoint` in the config to send
them to a team collector instead, see below.

## Is it actually working?

The failure that matters is silent: a token expires or the repo moves, background syncs start
failing, and nothing tells you until a teammate asks why you missed their fix. Three things
guard against that.

`skillsync auto status` gives you the whole picture:

```
background job:  registered, runs every 15 minutes
Claude Code:     hooks installed
last sync:       3m ago

skills are updating automatically.
```

When something is wrong it says so, in plain language, with the fix:

```
background job:  registered, runs every 15 minutes
Claude Code:     hooks installed
last error:      git fetch: could not read Username for 'https://github.com'

Syncs are failing: git fetch: could not read Username for 'https://github.com'
  Fix: run `skillsync sync` to see the full error.
```

Second, **Claude Code tells you at session start.** The hook checks health before syncing and
prints a warning into your session if syncs have been failing, so you find out while working
rather than never. It stays silent when everything is fine.

Third, **`list` and `stats` warn** if there has been no successful sync in over four hours, so
you know when you're reading stale data.

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

- **sources**, precedence-ordered; first source wins name collisions. `pin` (tag/commit)
  freezes a source for change control; omit to track the default branch.
- **tools**, which agent tools to install into. Default: `claude-code` only.
  Available: `claude-code` (`~/.claude/skills`), `cursor` (`~/.cursor/skills`),
  `codex` (`~/.agents/skills`). Each also installs project-locally under the
  same relative path.
- **metrics.endpoint**, optional sink for usage events; must be `https://` (cleartext is
  refused, since events name what you are working on). Omit for local-only stats.

## ⚠️ Local edits get overwritten

Skills installed by skillsync are **managed files**. If you edit one on your machine
(e.g. `~/.claude/skills/code-review/SKILL.md`), the next background sync, within 15
minutes, **replaces it with the team version and your changes are lost**. Sync overrides;
it never merges. A warning is printed, but background syncs only log it to
`~/.skillsync/daemon.log`, so you may not see it before the overwrite.

This is deliberate: silent local forks are the drift problem this tool exists to kill.

What to do instead:

- **Improve a skill for everyone**, edit it in the team skill repo and merge a PR.
  Every machine gets it within 15 minutes.
- **Keep a personal variant**, copy the skill directory under a new name
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

This removes only skills that skillsync installed, anything you put in
`~/.claude/skills/` yourself is left alone. Then delete the binary:

```sh
sudo rm /usr/local/bin/skillsync
```

### Just stop the automatic part

```sh
skillsync auto off   # stop background syncing and remove the hooks; skills stay
skillsync auto on    # turn it back on
```

This only strips skillsync's own entries from `~/.claude/settings.json`; your other hooks and
settings stay as they are.

## Behavior notes

- Project-local skills shadow a global skill of the same name; sync installs both and
  reports the shadowing (agent tools prefer the project copy).
- Unsubscribing a skill, or dropping it from `skillsync.yaml`, uninstalls it on the next sync.
- Source URLs must be `https://`, `ssh://`, `git://`, `file://`, or `user@host:path`. Git
  transport helpers (`ext::`) are refused: they run shell commands.
- Windows is supported: background sync registers with Task Scheduler. Claude Code hooks
  and the git credentials work the same as elsewhere.

## Release

Tag and push, CI cross-compiles and attaches binaries:

```sh
git tag v0.1.0 && git push origin v0.1.0
```
