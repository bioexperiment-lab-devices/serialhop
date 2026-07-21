# SerialHop local config migration — design

## Problem

`%ProgramData%\SerialHop\SerialHop_config.yaml` is the source of truth for the
service. When a lab operator updates SerialHop, the on-disk config can drift
from what the new binary expects:

- **New settings** introduced by the update are invisible to the operator. They
  get their Go default silently, so the operator never learns the knob exists.
- **Renamed keys** are worse: the old key is silently ignored by the forgiving
  YAML unmarshal, and the operator's customised value is lost — the new key
  falls back to its default without warning.
- **Changed value semantics** (an enum value renamed, a unit changed) leave a
  now-wrong value in place with no signal.
- **Removed settings** linger as dead keys that do nothing, with no explanation.

Today's loader (`config.Load`) does `yaml.Unmarshal` over `Default()`, so unknown
keys are dropped and missing keys default. That silently papers over every case
above. We want an explicit, reviewable migration path so that each config change
shipped in a release is applied to the operator's file automatically on update.

## Goals

- On update, transparently migrate the operator's config file to the shape the
  new binary expects, **preserving the operator's values** wherever possible.
- Make **new settings visible** in the file, with the same explanatory comments
  the first-run scaffold uses.
- **Comment out** removed settings rather than deleting them, with a marker
  explaining when and why.
- Carry a renamed key's **value** across to its new name.
- Give a **strictly defined, reviewable way** to register each config change,
  documented in `CLAUDE.md`, so every config-changing PR records its migration.
- Be safe: never lose data, never write a partial file, never block startup on a
  migration bug worse than the current behaviour.

## Non-goals

- **Making the panel's `SaveConfig` comment-preserving.** `SaveConfig` currently
  does `yaml.Marshal(cfg)`, which strips all comments (including
  migration-injected ones) the next time an operator saves through the UI.
  Migration's injected comments are therefore most valuable at update time. A
  comment-preserving panel save is a possible follow-up, tracked separately.
- **A panel "your config was migrated" notice.** On a real update the service
  worker restarts and migrates the file *first*, so by the time the operator
  opens the panel `EnsureMigrated` reports `Migrated: false` — a panel-local
  report would almost never fire. A reliable notice needs a persisted,
  cross-process "last migration" record; that is deferred. For now the migration
  outcome is logged in every process and the `.bak` file is the visible
  artifact.
- **Downgrade migrations.** We do not transform a newer file back to an older
  schema. See the downgrade guard below.
- **Generating the scaffold from the migration chain.** The scaffold's hand-
  written prose comments stay authoritative; a drift test keeps them in sync
  with the migrations instead. Deriving one from the other is possible future
  work.

## Terminology

- **Schema version** — a monotonic integer stamped in the config file as the
  top-level `schema_version` key. Independent of the app's SemVer.
- **Baseline** — schema version `1`, the config shape at the moment this feature
  first ships. A file with no `schema_version` key is treated as baseline.
- **Migration** — one ordered step that transforms a file from schema `N-1` to
  schema `N`, composed of typed operations.

## Versioning model

- A new package constant `config.CurrentSchemaVersion` is the single source of
  truth for the latest schema. It ships at `1` (baseline) and is bumped by `+1`
  in the same PR that adds a migration.
- `Config` gains a field:
  ```go
  SchemaVersion int `yaml:"schema_version" json:"schema_version"`
  ```
  `Default()` sets it to `CurrentSchemaVersion`. The field exists so the value
  round-trips through both write paths (`SaveConfig`'s `yaml.Marshal` and the
  first-run scaffold). It is **not** a panel-editable field; the frontend
  ignores it.
- The first-run scaffold template gains a leading `schema_version: <N>` line,
  rendered from `CurrentSchemaVersion`. A fresh install is therefore already at
  the latest schema and needs no migration.
- **A file with no `schema_version` key is read as version 1.** Because the
  registry starts empty, an existing field install is already at baseline and
  needs no retroactive churn. Migrations only ever get added going forward.

## Migration registry

Migrations live in `internal/config/migrations.go` — the one file a PR author
touches to register a config change.

```go
// migrations is the ordered, append-only history of config schema changes.
// Never edit or renumber an existing entry. Each PR that changes config
// appends exactly one entry and bumps CurrentSchemaVersion. See CLAUDE.md.
var migrations = []Migration{
    // {To: 2, Desc: "rename rest.port -> rest.listen_port", Ops: []Op{ ... }},
}

type Migration struct {
    To   int    // target schema version this migration produces
    Desc string // one-line human summary, surfaced in logs and the panel notice
    Ops  []Op   // ordered operations applied to the YAML document
}
```

Invariants (enforced by a registry test):

- `To` values are contiguous and ascending, starting at `2`.
- `CurrentSchemaVersion == last(migrations).To`, or `== 1` when the registry is
  empty.

## Migration engine (`internal/config/migrate.go`)

The engine operates on the parsed `yaml.Node` tree (not the typed struct) so that
comments and the operator's values survive. A **path** is a dot-separated
sequence of mapping keys, e.g. `rest.port`, `discovery.include`. Only mapping
traversal is supported (no array indexing) — sufficient for the current config.

### Operation vocabulary

`Op` is an interface; each constructor returns a value that, when applied to the
document, mutates it and returns zero or more `Change` records. All ops are
no-ops when their target path is absent (except `Add`, which inserts).

| Constructor | Behaviour |
| --- | --- |
| `Rename(from, to string)` | Move the value node at `from` to `to`, carrying its inline/head comments and creating any missing parent maps under `to`. If `to` is already present, leave it and comment out `from` as *superseded by `to`*. |
| `Remove(path string)` | Comment out the key and its value **in place**, prefixed with `# removed in schema vN — no longer used:`. Never deletes the operator's data. |
| `Add(path, snippet string)` | If `path` is absent, splice in `snippet`. `snippet` is a self-contained `key: value` YAML block (with leading comments) whose top key equals the final path segment; the engine inserts it under the parent identified by the path prefix, creating parent maps as needed. No-op if the key already exists (respects the operator's value). |
| `MapValue(path string, fn func(string) string)` | Rewrite the scalar at `path` via `fn` (enum rename, unit change), preserving quoting style. No-op if absent or non-scalar. |
| `MapNode(path string, fn func(*yaml.Node) error)` | Escape hatch for structural transforms the typed ops can't express. |

### Comment-out mechanism

`Remove` (and `Rename`'s superseded case) implement "comment out" via yaml.v3's
node comment fields: the removed `key: value` subtree is rendered to text, each
line is prefixed with `# ` plus the marker, and the block is attached as a
`HeadComment` on the following sibling key (or, if the key was last, appended to
the preceding node's `FootComment`). The actual nodes are then removed from the
mapping. This keeps everything inside the node model so a single final
`yaml.Marshal` produces the migrated bytes. Exact attachment placement is an
implementation detail covered by golden-fixture tests.

### Report

```go
type Change struct {
    Kind   string // "rename" | "remove" | "add" | "map-value" | "map-node"
    Path   string
    Detail string // e.g. "rest.port -> rest.listen_port", "value verbose -> debug"
}

type Report struct {
    From, To   int
    Migrated   bool     // false when nothing needed doing
    Changes    []Change
    BackupPath string   // "" when no migration ran
}
```

## Runtime behaviour: `EnsureMigrated`

```go
// EnsureMigrated migrates the config file at path up to CurrentSchemaVersion,
// rewriting it in place if needed. Idempotent and safe to call concurrently
// from multiple processes. Returns a Report describing what changed.
func EnsureMigrated(path string) (Report, error)
```

Algorithm:

1. Read the file. If it does not exist, return an empty report (no-op — the
   first-run scaffold path owns creation).
2. Parse the bytes into a `yaml.Node`. **If parsing fails, return an empty
   report and no error** — leave the file untouched and let the normal `Load`
   path surface the parse error exactly as today.
3. Read `schema_version` from the node (absent ⇒ `1`). Call it `from`.
   - If `from == CurrentSchemaVersion`: no-op.
   - If `from > CurrentSchemaVersion`: **downgrade guard** — no-op, log a
     warning. The older-shaped binary already ignores unknown keys via forgiving
     unmarshal.
   - If `from < CurrentSchemaVersion`: continue.
4. Apply every migration with `To > from`, in order, accumulating `Change`s.
   Build the fully-migrated document **in memory**. If any op errors, **abort
   the whole batch, write nothing**, and return the error (the caller logs it and
   proceeds to load the un-migrated file).
5. Stamp `schema_version: CurrentSchemaVersion` in the node.
6. **Back up** the original bytes to `SerialHop_config.<from>.bak.yaml` in the
   same directory.
7. **Atomically** write the migrated bytes (tmp file + rename, `0600`) — reusing
   the existing `atomicWriteFile` pattern from `firstrun.go`.
8. Return the populated report.

### Idempotency & concurrency

- After a successful run the file is stamped at `CurrentSchemaVersion`, so a
  second call short-circuits at step 3 without writing.
- The service and panel are separate processes that can both start right after
  an update. Migration is deterministic and atomically written, so concurrent
  runs converge to identical bytes; the worst case is one redundant identical
  write and a duplicate `.bak`. No lock is required.

### Call sites

`EnsureMigrated` is called **once at each process's startup, before the first
`Load`/`LoadPartial`**:

- `main.go` `runForeground` — right after the scaffold-exists check, before
  `config.Load`.
- `internal/winsvc/worker.go` — before its `config.Load`.
- Panel startup (`internal/panel`) — before the first `LoadPartial`, as a
  defensive belt-and-suspenders for the panel-launched-without-service-restart
  case. The panel logs the outcome; it does not render a UI notice (see
  non-goals).

It is **not** called inside `Load`/`LoadPartial`, which run repeatedly (the panel
polls `LoadPartial` for status); migration belongs at process start.

The migration outcome is logged via structured slog at info level in every
process (service worker, foreground, panel), so headless service starts still
record what changed. The `<name>.<from>.bak.yaml` backup is the operator-visible
artifact of a migration.

## Interaction with existing code

- `Load` / `LoadPartial` are unchanged. They continue to overlay parsed YAML on
  `Default()`; by the time they run, `EnsureMigrated` has already reconciled the
  file.
- `Validate` is unchanged and runs after migration as always. If a migration
  produces an invalid config, validation fails loudly like any bad config —
  visible in the logs and the panel's validation warning.
- The first-run scaffold (`WriteScaffold`) gains the `schema_version` line.
- `SaveConfig` gains `SchemaVersion` in its round-trip automatically (struct
  field) but is otherwise untouched; its comment-stripping is a documented
  non-goal.

## CLAUDE.md rules

A new section, **"Registering config changes,"** is added to `CLAUDE.md`:

> Any PR that adds, renames, or removes a field on `config.Config`, or changes a
> field's accepted values, **must register a migration**:
>
> 1. Bump `config.CurrentSchemaVersion` by 1 and append exactly one
>    `Migration{To: <new>, Desc, Ops}` to `internal/config/migrations.go`.
>    **Never edit or renumber an existing migration** — the list is append-only
>    history.
> 2. Choose ops from the fixed vocabulary: add → `Add`, rename → `Rename`,
>    remove → `Remove` (comments the key out — never silently delete a value),
>    value/enum change → `MapValue`, anything structural → `MapNode`.
> 3. For `Add`, reuse the exact comment text from the first-run scaffold and
>    update the scaffold in the same PR. The drift test enforces they agree.
> 4. Add a before/after fixture case under
>    `internal/config/testdata/migrations/` and a table entry in
>    `migrate_test.go`.
> 5. One PR = one schema bump. Do not batch unrelated config changes.

## Testing

- **Drift guard:** take a frozen `testdata/migrations/baseline-v1.yaml` fixture
  (the scaffold's key set as it stood at schema 1) and replay **every** migration
  in the registry against it. The resulting key set must equal the key set of the
  current first-run scaffold, and both must stamp `schema_version ==
  CurrentSchemaVersion`. This catches an `Add` migration that forgot to update the
  scaffold (a new field invisible to fresh installs) or a scaffold change that
  forgot its `Add` migration (a new field invisible to existing installs). Key-set
  equality — not byte equality — since yaml.v3 re-marshalling won't reproduce the
  hand-written scaffold's exact formatting.
- **Registry invariants:** contiguous ascending `To` from 2;
  `CurrentSchemaVersion` matches the last entry (or 1 when empty).
- **Per-migration golden fixtures:** each migration has a `before.yaml` /
  `after.yaml` pair under `testdata/migrations/`; a table-driven test applies the
  single migration and asserts byte-equality (comments included).
- **Behavioural unit tests** for the engine, independent of any real migration:
  rename carries the value and comment; rename to an occupied destination
  comments out the source; remove comments out with the marker and preserves the
  value text; add is a no-op when the key exists and injects with its comment
  when absent; map-value rewrites only the matching scalar.
- **Idempotency:** `EnsureMigrated` on an already-current file writes nothing and
  reports `Migrated: false`; a second consecutive call is a no-op.
- **Downgrade guard:** a file stamped above `CurrentSchemaVersion` is left
  untouched.
- **Unparseable input:** a garbage file is left untouched and returns no error.
- **Failure isolation:** a migration whose op errors leaves the file byte-for-
  byte unchanged.
- **Cross-platform:** the whole package is pure Go and uses the
  `SERIALHOP_DATA_DIR` temp-dir pattern, so it runs identically on macOS and
  Windows — no `_windows.go` split.

## File layout

- `internal/config/migrate.go` — the engine: `Op` interface and constructors,
  the apply loop, `EnsureMigrated`, `Report`/`Change`, dotted-path helpers,
  comment-out helper.
- `internal/config/migrations.go` — `CurrentSchemaVersion`, the `Migration`
  type, and the append-only `migrations` registry. **The file PR authors edit.**
- `internal/config/migrate_test.go` + `internal/config/testdata/migrations/` —
  engine unit tests, registry-invariant tests, drift guard, golden fixtures.
- Edits to `internal/config/config.go` (struct field, `Default`, scaffold line),
  `main.go`, `internal/winsvc/worker.go`, `internal/panel` (defensive startup
  call + log), `CLAUDE.md`, and `docs/configuration.md` (document
  `schema_version` and the migration behaviour for operators).

## Initial shipped state

The registry ships **empty**, `CurrentSchemaVersion == 1`. This PR delivers the
infrastructure and the baseline stamp only; no field changes yet. The first real
migration lands in whatever future PR first changes a config field, following the
CLAUDE.md rules above.
