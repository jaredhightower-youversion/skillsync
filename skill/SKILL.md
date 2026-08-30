---
name: skillsync
description: How to set up, use, and troubleshoot skillsync, the team skill directory that keeps agent skills in sync from a git repo. Use when a user asks to install or configure skillsync, add or edit a team skill, check whether skill updates are arriving, or when a skillsync command or hook reports a problem.
---

# skillsync

skillsync installs skills from a team git repo into this machine's agent tools
(Claude Code, Cursor, Codex) and keeps them updated in the background. The
repo is the source of truth; local copies are managed files.

## Setting up a machine

Run exactly this, with no extra flags:

    skillsync init <git-url>

It syncs once, registers a background job that re-syncs every 15 minutes, and
installs Claude Code hooks for session-start sync and usage stats. All three
are the product. `--no-daemon` and `--no-hooks` exist for CI and locked-down
machines: with either, skills go stale until someone runs `skillsync sync` by
hand. Do not add them unless the user asked for them by name.

To install into more than Claude Code:

    skillsync init <git-url> --tools claude-code,cursor,codex

`init` ends by printing `auto status`. Report that block to the user verbatim.
If it says "Not updating automatically", the setup is incomplete; run the fix
it names (`skillsync auto on`) before calling the job done.

If a step fails because a settings file is not writable, say so and show the
user `skillsync hook print` so they can add the hooks themselves.

## Adding or changing a skill

Edit the team repo, never the installed copy. Anything under
`~/.claude/skills/<name>/` (or the Cursor/Codex equivalents) that skillsync
manages is overwritten on the next sync, and local edits are lost.

Repo layout, one directory per skill:

    skills/
      code-review/SKILL.md
      deploy-checklist/SKILL.md

Add or edit `skills/<name>/SKILL.md`, commit, push or open a PR. Every
machine syncing from that repo has it within 15 minutes, or immediately after
`skillsync sync`.

To keep a personal variant of a managed skill, copy it under a different name;
unmanaged names are never touched.

## Checking that updates arrive

    skillsync list           what is installed, when each skill last changed, by whom
    skillsync sync           pull now instead of waiting for the background job
    skillsync auto status    is the background job registered, are hooks installed,
                             when did the last sync succeed, what was the last error

To verify a change end to end: push a visible edit to a skill, run
`skillsync sync`, then read the installed `SKILL.md` and confirm the edit is
there.

## Project-local skills

A `skillsync.yaml` at a project root lists skills to install into that
project's `.claude/skills/` (and the equivalents for other tools):

    skills:
      - code-review
      - deploy-checklist

## Troubleshooting

- `skillsync check` is fast and offline; it prints only when something is
  wrong. The Claude Code session-start hook runs it.
- Background sync errors go to `~/.skillsync/daemon.log`.
- "git fetch: could not read Username" means the repo URL needs an SSH key or
  a credential helper on this machine.
- `list` or `stats` warning about no sync in four hours means you are reading
  stale data; run `skillsync auto status` for the cause.
- Config lives in `~/.skillsync/config.json`: `sources` (precedence-ordered,
  first wins name collisions), `tools`, and an optional `metrics.endpoint`.

## Usage stats

`skillsync stats` counts how often each skill was invoked on this machine. It
needs the Claude Code hooks; Cursor and Codex do not report invocations. Counts
stay local unless `metrics.endpoint` is set.
