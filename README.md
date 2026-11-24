# warden-plugin

A Go port of the [warden](https://github.com/Syntax-Syllogism/warden) Salesforce CLI plugin 0.11.3 and of its library [warden-core](https://github.com/Syntax-Syllogism/warden-core) 0.7.0, built as a plugin for the sf-compatible mode of the [force CLI](https://github.com/ForceCLI/force). It provides the `sf warden` commands for managing the users of an org:

| Command | What it does |
|---|---|
| `sf warden access` | Lists the active users who have access to a field, object, Apex class, Visualforce page, custom permission, tab or record type, and through which profile, permission set or permission set group. With `--user`, lists what one user has access to. |
| `sf warden diff` | Compares users with the access their personas define, or one user with another. `--verify` reports conformance. |
| `sf warden provision` | Creates and updates users from a users definition (JSON, CSV or TSV) and persona definitions, with optional related records. |
| `sf warden freeze`, `sf warden unfreeze` | Sets `UserLogin.IsFrozen`. |
| `sf warden strip` | Removes a user's permission sets, groups, queues and licenses, freezes the login and deactivates the user; or removes only the access of chosen personas. |
| `sf warden snapshot`, `sf warden restore` | Saves the assignments of users to a file, and puts back what is missing. |
| `sf warden persona export`, `import`, `diff` | Moves persona definitions between a JSON file and the personas stored in the org by the optional warden-base package. |

The plugin is one executable and needs no Node.js. The file formats, flags, messages, output and exit statuses are those of warden; its documentation applies: <https://github.com/Syntax-Syllogism/warden/tree/release/docs>.

## Install

```
sf plugins install warden
```

`sf` here is the force CLI in sf-compatible mode (the `force` binary under the name `sf`, or `force --sf`). The name resolves to `ghcr.io/forcecli/sf-plugins/warden:latest`.

To use a local build:

```
go build -o warden .
sf plugins link ./warden
```

## The Target Org

Every command works on an org. The force CLI resolves `--target-org` (or the default org) and `--api-version`, and passes the instance URL and an access token to the plugin in the `SF_PLUGIN_*` environment variables described in force's `docs/sf-plugins.md`. The plugin accepts both flags and does not read them itself. Run directly, without those variables, a command reports `No default environment found.`

## Usage

```
sf warden access --target-org myOrg --type apex-class --target My_Apex_Class
sf warden diff --target-org myOrg --users-def users.json --personas-def personas.json
sf warden snapshot --target-org myOrg --user 'Username:user@example.com' --out snapshot.json
sf warden restore --target-org myOrg --snapshot snapshot.json
sf warden freeze --target-org myOrg --user 'Username:user@example.com'
sf warden provision --target-org myOrg --users-def users.json --personas-def personas.json
sf warden strip --target-org myOrg --user 'Username:user@example.com' --snapshot pre-strip.json
```

`provision`, `freeze`, `unfreeze`, `strip` and `restore` change the org. Each accepts `--dry-run` to show the changes without making them, and asks for confirmation before writing unless `--no-prompt` or `--json` is given. `sf warden <command> --help` lists the flags.

Exit status: 0 when no user failed; 1 when a user failed or the command reported an error; 2 for an unknown or malformed flag. With `--json` the envelope of a result has `status` 0 also when a user failed, as in warden; the exit status reports the failure.

## Parity With Upstream

| Package | Ported from | How it was checked |
|---|---|---|
| `core` | warden-core `shared/`, `spec/`, `matching/`, `lifecycle/targeting`, `provisioning/definitionReader` | Unit tests. |
| `access` | warden-core `access/` | Cases of warden's `access.test.ts`; six scenarios compared with the JavaScript library run under node. |
| `provisioning` | warden-core `provisioning/`, `relatedRecords/` | The library's 15 conformance fixtures (`provisioning/testdata/conformance`), required to match row for row; cases of warden's `provision.test.ts`. |
| `lifecycle` | warden-core `lifecycle/` and the freeze, strip, diff, snapshot and restore use cases | 81 scenarios (`lifecycle/testdata`) compared byte for byte with the output of the JavaScript library run under node: queries, record changes, results, text, CSV and snapshot files. |
| `orgdata` | warden `src/userShared/personas.ts`, `orgWrites.ts`, `orgContract.ts`, `provisionLedger.ts` | All cases of warden's tests for those modules. |
| `cmd` | warden `src/commands/` | Tests against an in-memory org, and runs through `force --sf` against a local test server (`aer server`): access, diff, snapshot, restore, freeze, unfreeze, provision and strip. |

Known differences:

| Area | warden | This port |
|---|---|---|
| `--interactive` | Prompts for missing values | Not ported. The flag is refused with a message; pass the values as flags. |
| Confirmation before a write | Prompt of the sf CLI | A `(y/n)` question on stderr, answered on stdin, with the same 10 second limit. |
| The `sfd` integration (`src/sfd/`) | Operations for the sfd host | Not ported. |
| Record changes | jsforce | Lists of records go through the collections resource (`composite/sobjects`), 200 at a time. One record uses the single-record resource. A server without the collections resource gets one request for each record. |
| Sorting of names | `localeCompare` (ICU) | The same order for ASCII text; other characters are ordered by code point. |
| Record-type access audit | Reads profiles through the Metadata API with 2 concurrent requests | The same requests, one at a time. |
| Text of a JSON syntax error | V8's message | Go's message; the error code is the same. |
| Run records in the org (`--log-to-org`) | `wdn_Cli_Version__c` holds the npm package version | It holds the plugin version, 0.11.3. |

Not checked against a real Salesforce org: every test ran against an in-memory org or the local test server. The record-type audit (Metadata API over SOAP), the collections resource, the upsert used by `persona import`, and the warden-base org objects were exercised only with fakes. warden's own org tests (`*.nut.ts`) were not run.

## Development

```
go work init . ../force     # once; go.work is not committed
go test ./...
```

`go.mod` requires `github.com/ForceCLI/force` for the `sfplugin` package, which defines the plugin protocol. Until a force release contains `sfplugin`, the plugin builds only against a checkout of force through a local `go.work`. After such a release, raise the version in `go.mod`.

`sfapi` is the Salesforce API client (queries, describes, record changes, Metadata API reads) and `sfapi/sfapitest` an in-memory org for tests. `cmd/messages/` holds warden's message files, which the commands read for their texts.

## Publishing

`.github/workflows/publish.yml` builds the executables for Linux, macOS and Windows on amd64 and arm64 when a tag `v*` is pushed, and publishes them to `ghcr.io/forcecli/sf-plugins/warden` with `force --sf plugins publish`. It needs a force release that has the `plugins publish` command. It has not been run.

## License

MIT, as warden and warden-core; see `LICENSE` and `LICENSE-warden`.
