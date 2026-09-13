---
"skillsync": minor
---

Add an `omp` adapter: `--tools omp` installs into `~/.omp/agent/skills` (and `.omp/skills` per project), where omp's native provider outranks the `~/.claude/skills` copies. omp has no `skillOverrides` map, so exposure rides in the installed copy's frontmatter as `hide: true` — not listed to the model, still loaded, still reachable by `/skill:<name>`. Exposure is now decided before installing rather than after, hubs are generated into every enabled tool's skills directory with paths into that directory, and the install record keeps a per-tool output hash so a rewritten copy is not mistaken for local drift.
