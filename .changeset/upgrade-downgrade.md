---
"skillsync": patch
---

`skillsync upgrade` and the session-start notice now compare versions by semver precedence instead of string equality. A binary built from source reports a module pseudo-version that sorts ahead of the tag it came from, so both used to treat the newest release as an upgrade and would replace a newer binary with an older one.
