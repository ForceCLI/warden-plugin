# warden Go Port Checklist

Upstream: `~/git/warden` 0.11.3 (`d217cf5`, MIT, Jacob Richter) and its library `@syntax-syllogism/warden-core` 0.7.0 (MIT; compiled JS unpacked from the npm tarball, source at github.com/Syntax-Syllogism/warden-core).

## Decisions

- Module path `github.com/ForceCLI/warden-plugin`; plugin name `warden`; published as `ghcr.io/forcecli/sf-plugins/warden`.
- Every command has the org requirement `required`; the org comes from the `SF_PLUGIN_*` variables force sets (`sfplugin.OrgFromEnv`).
- `src/sfd/` (integration with the sfd host) is not ported.
- Interactive prompts: bubbletea + lipgloss.
- `go.work` is local and not committed (`go work init . ../force`).

## Progress

- [x] `sfapi`: REST client (query, describe, record changes with collections and single-record fallback, upsert, Metadata API read and list, user id) and `sfapitest` fake
- [x] `core`: errors, ordered JSON, field maps, CSV, definition validation, matching, targeting
- [x] `access` (delegated port; 6 scenarios compared with the JS library)
- [x] `provisioning` with related records (delegated port; 15 conformance fixtures pass)
- [x] `lifecycle` (delegated port; 81 scenarios compared byte for byte with the JS library)
- [x] `orgdata`: personas, ledger, runs (delegated port; upstream tests transcribed)
- [x] `cmd`: access, diff, freeze, unfreeze, snapshot, restore, strip, provision, persona export/import/diff
- [x] Command tests against the fake org
- [x] End to end through `force --sf` against `aer server`: access, diff, snapshot, restore, freeze, unfreeze, provision, strip
- [x] README, publish workflow (not run)

## Open

- [ ] `--interactive` mode (bubbletea + lipgloss prompts); the flag is refused for now
- [ ] Run against a real org: record-type audit over the Metadata API, collections resource, persona import upsert, warden-base objects
- [ ] Transcribe the remaining CLI-level cases of warden's `planApply.test.ts`, `process.test.ts`, `interactive.test.ts`
- [ ] Raise the force version in `go.mod` once a force release contains `sfplugin`
- [ ] Confirm the module path and the copyright line in `LICENSE`
