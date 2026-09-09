<div align="center">

<picture><source media="(prefers-color-scheme: dark)" srcset="https://shieldcn.dev/header/grid.svg?title=skillsync&amp;subtitle=Team+skills+that+update+themselves&amp;mode=dark"><img alt="skillsync" src="https://shieldcn.dev/header/grid.svg?title=skillsync&amp;subtitle=Team+skills+that+update+themselves&amp;mode=light"></picture>

<picture><source media="(prefers-color-scheme: dark)" srcset="https://www.shieldcn.dev/group/github/stars/jaredhightower-youversion/skillsync+github/forks/jaredhightower-youversion/skillsync+github/watchers/jaredhightower-youversion/skillsync.svg?variant=secondary&amp;size=sm&amp;mode=dark"><img alt="Stars, forks, watchers" src="https://www.shieldcn.dev/group/github/stars/jaredhightower-youversion/skillsync+github/forks/jaredhightower-youversion/skillsync+github/watchers/jaredhightower-youversion/skillsync.svg?variant=secondary&amp;size=sm&amp;mode=light"></picture>
<picture><source media="(prefers-color-scheme: dark)" srcset="https://www.shieldcn.dev/group/github/commits/jaredhightower-youversion/skillsync+github/last-commit/jaredhightower-youversion/skillsync+github/contributors/jaredhightower-youversion/skillsync.svg?variant=secondary&amp;size=sm&amp;mode=dark"><img alt="Commits, last commit, contributors" src="https://www.shieldcn.dev/group/github/commits/jaredhightower-youversion/skillsync+github/last-commit/jaredhightower-youversion/skillsync+github/contributors/jaredhightower-youversion/skillsync.svg?variant=secondary&amp;size=sm&amp;mode=light"></picture>
<picture><source media="(prefers-color-scheme: dark)" srcset="https://www.shieldcn.dev/group/github/open-issues/jaredhightower-youversion/skillsync+github/open-prs/jaredhightower-youversion/skillsync.svg?variant=ghost&amp;size=sm&amp;mode=dark"><img alt="Open issues, open PRs" src="https://www.shieldcn.dev/group/github/open-issues/jaredhightower-youversion/skillsync+github/open-prs/jaredhightower-youversion/skillsync.svg?variant=ghost&amp;size=sm&amp;mode=light"></picture>
<picture><source media="(prefers-color-scheme: dark)" srcset="https://www.shieldcn.dev/group/github/release/jaredhightower-youversion/skillsync+github/ci/jaredhightower-youversion/skillsync+github/license/jaredhightower-youversion/skillsync.svg?variant=secondary&amp;size=sm&amp;mode=dark"><img alt="Release, CI, license" src="https://www.shieldcn.dev/group/github/release/jaredhightower-youversion/skillsync+github/ci/jaredhightower-youversion/skillsync+github/license/jaredhightower-youversion/skillsync.svg?variant=secondary&amp;size=sm&amp;mode=light"></picture>

Team skill directory with background auto-sync. Edit a skill once in your team's git repo and
every teammate's machine updates automatically across Claude Code, Cursor, and Codex.
No update command, no drift, with usage metrics to show which skills earn their keep.

[Install](#install) &nbsp;&middot;&nbsp; [Setup](#setup-one-time-per-machine) &nbsp;&middot;&nbsp; [Commands](#commands) &nbsp;&middot;&nbsp; [Design decisions](SPEC.md)

</div>

## Install

Download the binary for your platform from
[Releases](../../releases/latest), then:

```sh
chmod +x skillsync-darwin-arm64
sudo mv skillsync-darwin-arm64 /usr/local/bin/skillsync
```

Or with Go installed:

```sh
go install github.com/jaredhightower-youversion/skillsync@latest
```

### Upgrading

```sh
skillsync upgrade
```

Downloads the latest release for your platform, checks it against the release's
`checksums.txt`, and swaps it in place of the running binary. If skillsync lives in a
directory you can't write to (like `/usr/local/bin`), run it with `sudo`.

You don't have to remember to check. The background sync looks up the newest release once
a day, and the next Claude Code session prints one line if you're behind:

```
skillsync v0.2.0 is available (you have v0.1.0). Upgrade: skillsync upgrade
```

`skillsync version` prints what's installed.

## Setup (one time, per machine)

```sh
skillsync init git@github.com:your-org/skills.git
```

That syncs your skills, registers a background job that re-syncs every 15 minutes, and adds
Claude Code hooks for session-start sync and usage tracking. It ends by printing
`skillsync auto status`, so you can see all three pieces landed.

Done. Skills appear in `~/.claude/skills/` and update on their own.

To install into Cursor or Codex as well:

```sh
skillsync init git@github.com:your-org/skills.git --tools claude-code,cursor,codex
```

`--no-daemon` and `--no-hooks` skip the background job or the hooks. They exist for CI and
locked-down machines; with either one, skills go stale until someone runs `skillsync sync`
by hand, and `init` says so. Add them only when you mean that.

### Letting an agent set it up

Every synced machine gets a built-in `skillsync` skill (`~/.claude/skills/skillsync/SKILL.md`),
so an agent knows how to set up skillsync, add skills to the team repo, and verify updates
arrived. Before the first sync there is nothing installed yet, so if you ask an agent to
onboard a machine, the instructions that matter are: run plain `skillsync init <url>` with no
`--no-*` flags unless you asked for them, then report the `auto status` block it prints.

## Team skill repo

Any git repo where each skill is a directory containing a `SKILL.md`
([skills.sh](https://www.skills.sh) conventions, repos work with `npx skills add` too):

```
skills/
  code-review/SKILL.md
  deploy-checklist/SKILL.md
```

Edit a skill, merge the PR, everyone has the new version within 15 minutes.

skillsync also installs one skill of its own, `skillsync`, which tells agents how to use
this tool. It has the lowest precedence: a `skills/skillsync/SKILL.md` in your repo
replaces it, and `skillsync remove skillsync` drops it.

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
| `skillsync init <git-url>` | Set up this machine (see above); `--tools` picks agent tools, `--no-daemon` / `--no-hooks` are escape hatches that leave skills stale |
| `skillsync sync` | Sync now |
| `skillsync list` | Skills with install status, what Claude sees of each, when each last changed, and who changed it |
| `skillsync check` | Warn if syncs are failing or Claude's skill listing is over budget (the session hook runs this) |
| `skillsync add / remove <skill>` | Manage global subscriptions (default: all) |
| `skillsync adopt <skill>` | Replace a hand-installed skill with the managed version |
| `skillsync stats` | How often each skill has been used on this machine |
| `skillsync auto on / off / status` | Automatic updating: the background job and Claude Code hooks |
| `skillsync upgrade` | Replace the binary with the latest release |
| `skillsync version` | Print the installed version |
| `skillsync uninstall` | Remove skillsync and everything it installed |

Realistically you type `init` once and `list` or `stats` occasionally; everything else is for
when something needs checking.

`skillsync list` answers "what changed lately", newest first:

```
STATUS     SKILL                        TIER       CLAUDE SEES          LAST USED    UPDATED      BY               SOURCE
installed  deploy-checklist             on-demand  on                   1d ago       2h ago       Grace Hopper     team
installed  code-review                  auto       on                   3h ago       6d ago       Ada Lovelace     org
installed  marketing-seo-audit          on-demand  user-invocable-only  unknown      9d ago       Grace Hopper     team
available  release-notes                on-demand  -                    unknown      2026-05-01   Ada Lovelace     org
```

`TIER` is what the skill repo declared, `CLAUDE SEES` is what this machine wrote into Claude
Code after applying the usage rules (see below).

Dates come from the skill repo's git history, so they reflect when a skill was actually
edited, not when your machine last synced.

## What Claude sees (installed is not the same as listed)

Claude Code loads every installed skill's name and description into context on every turn,
and caps that listing at about 1% of the context window. Past that it truncates descriptions,
and the trigger phrases are the first thing to go: the wrong skill fires, or none does. A
hundred skills is already over.

So skillsync keeps two things apart:

- **Available**: the files are on the machine and the `/` command works. Every synced skill.
- **Exposed**: the description is in Claude's context so it can trigger by itself. Only
  skills the repo marks `auto`.

A skill declares its tier in `SKILL.md` frontmatter. The key is spec-legal, so the same
file still uploads to claude.ai and the Skills API:

```yaml
---
name: marketing-seo-audit
description: ...
metadata:
  exposure: on-demand   # or: auto. Absent = on-demand.
---
```

On every sync skillsync writes Claude Code's own `skillOverrides` map in
`~/.claude/settings.json`, touching only skills it manages. `auto` becomes `"on"`,
`on-demand` becomes `"user-invocable-only"` (hidden from Claude, still in the `/` menu).

Then three things adjust that per machine, without touching the repo:

- **Promotion.** An `on-demand` skill you invoked 3 or more times in the last 30 days is
  listed with its description here. Use it and it earns its place.
- **Demotion.** An `auto` skill nobody invoked in 90 days drops to `"name-only"` here.
  Changing a skill's tier in the repo resets both clocks on every machine.
- **Hubs.** A prefix family (`marketing-*`, `design-*`) with 8 or more hidden members gets
  one generated router skill named after the prefix. Its description is listed; its body is
  a table of the members and where their `SKILL.md` lives. One description buys the whole
  family, and since it is built from the members' frontmatter it cannot go stale. Hubs
  carry a marker comment and are regenerated or removed by sync.

`skillsync check` (which the session-start hook runs) warns when the descriptions that are
listed still exceed the budget, naming the biggest ones. It honors
`skillListingBudgetFraction` in settings and `SLASH_COMMAND_TOOL_CHAR_BUDGET` if set.

A project can promote skills for sessions inside it by writing the same `skillOverrides`
shape into its `.claude/settings.local.json`; skillsync leaves that file alone.

Cursor and Codex have no equivalent setting, so on-demand skills are simply installed there
as before.

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
- **Managed skills only.** Only skills skillsync installed from your source repos are counted.
  Skills you added by hand, and the built-in `skillsync` skill, are ignored, and their names
  are never written to the event log or sent to a metrics endpoint.
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
  "metrics": {"enabled": true, "endpoint": "https://metrics.internal/skills"},
  "exposure": {"promote_uses": 3, "promote_days": 30, "demote_days": 90, "hub_min_members": 8}
}
```

- **sources**, precedence-ordered; first source wins name collisions. `pin` (tag/commit)
  freezes a source for change control; omit to track the default branch.
- **tools**, which agent tools to install into (set at `init` with `--tools`, or edit here). Default: `claude-code` only.
  Available: `claude-code` (`~/.claude/skills`), `cursor` (`~/.cursor/skills`),
  `codex` (`~/.agents/skills`). Each also installs project-locally under the
  same relative path.
- **metrics.endpoint**, optional sink for usage events; must be `https://` (cleartext is
  refused, since events name what you are working on). Omit for local-only stats.
- **exposure**, thresholds for the promote/demote rules and hub generation described above.
  `hub_min_members: 0` turns hubs off. The values shown are the defaults; `init` writes them
  out so they are there to edit.

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
- Unsubscribing a skill, or dropping it from `skillsync.yaml`, uninstalls it on the next sync
  and drops the `skillOverrides` entry skillsync wrote for it. `skillsync uninstall` removes
  every entry it wrote and leaves the rest of `settings.json` alone.
- Source URLs must be `https://`, `ssh://`, `git://`, `file://`, or `user@host:path`. Git
  transport helpers (`ext::`) are refused: they run shell commands.
- Windows is supported: background sync registers with Task Scheduler. Claude Code hooks
  and the git credentials work the same as elsewhere.

## Release

Add a section for the version to `CHANGELOG.md`, then tag and push. CI runs the tests,
cross-compiles, attaches binaries, and uses that changelog section as the release notes:

```sh
git tag v0.2.0 && git push origin v0.2.0
```

## License

MIT. See [LICENSE](LICENSE).
