# Config Migration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add config-migration tooling so a lab operator's `SerialHop_config.yaml` is transparently upgraded to the shape the new binary expects on update — preserving their values, surfacing new settings, commenting out removed ones.

**Architecture:** A `schema_version` integer stamps the file. An append-only, ordered `migrations` registry (typed `Op` builders — `Rename`/`Remove`/`Add`/`MapValue`/`MapNode`) transforms the parsed `yaml.Node` tree so comments and operator values survive. `EnsureMigrated(path)` runs once at each process's startup (service worker, foreground, panel) before load: it backs up, applies every migration with `To > current`, stamps the new version, and atomically rewrites. Ships with an empty registry at `CurrentSchemaVersion == 1` (infrastructure + baseline only).

**Tech Stack:** Go, `gopkg.in/yaml.v3` (already a dependency), standard library.

## Global Constraints

- Go module: `github.com/bioexperiment-lab-devices/serialhop`.
- Tests must pass on **macOS and Windows**. Windows-only code lives in `_windows.go` files; keep the migration engine (package `config`) pure-Go so it runs on both.
- Pre-flight (all must pass): `gofmt -l .` (empty), `go vet ./...`, `golangci-lint run` (errcheck, staticcheck, unused, ineffassign, gosec), `go test -race -count=1 ./...`, `govulncheck ./...`.
- `gosec` flags `os.ReadFile`/`os.WriteFile` with variable paths; annotate with `//nolint:gosec // <reason>` matching the existing style in `internal/config/load.go`.
- Use the `t.Setenv("SERIALHOP_DATA_DIR", t.TempDir())` isolation pattern in tests that touch `paths`.
- Conventional Commits for the eventual PR title; this feature is a `feat:`.

## File Structure

- **Create** `internal/config/migrate.go` — engine: `Op` interface + constructors, dotted-path/node helpers, comment-out helper, version read/stamp, `EnsureMigrated` + unexported `ensureMigrated` seam, `Report`/`Change`.
- **Create** `internal/config/migrations.go` — `CurrentSchemaVersion` const, `Migration` type, append-only `migrations` registry. **The file future PR authors edit.**
- **Create** `internal/config/migrate_test.go` — engine unit tests, registry invariants, drift guard, `EnsureMigrated` file-handling tests.
- **Create** `internal/config/testdata/migrations/baseline-v1.yaml` — frozen v1 scaffold snapshot for the drift guard.
- **Modify** `internal/config/config.go` — add `SchemaVersion` field, set in `Default()`, add `schema_version` line to the scaffold template.
- **Modify** `main.go` — call `EnsureMigrated` in `runForeground` before `config.Load`.
- **Modify** `internal/winsvc/worker.go` — call `EnsureMigrated` before `config.Load`.
- **Modify** `internal/panel/wails_app.go` — call `EnsureMigrated` at the top of `startup`.
- **Modify** `CLAUDE.md` — add "Registering config changes" section.
- **Modify** `docs/configuration.md` — document `schema_version` and migration behaviour.

---

### Task 1: Schema version field, `Default`, and scaffold line

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go` (append)

**Interfaces:**
- Produces: `Config.SchemaVersion int` (yaml/json tag `schema_version`); `config.CurrentSchemaVersion` is referenced here but **defined in Task 2** — for this task, temporarily reference the literal `1` in `Default()` and the scaffold, then switch to the constant in Task 2. To avoid a forward reference, define the constant in this task instead (move it here): add `const CurrentSchemaVersion = 1` at the top of `config.go`. Task 2 will move the `Migration` type and registry into `migrations.go` and keep the constant where it is.

- [ ] **Step 1: Write the failing test**

Append to `internal/config/config_test.go`:

```go
func TestDefaultStampsCurrentSchemaVersion(t *testing.T) {
	if got := Default().SchemaVersion; got != CurrentSchemaVersion {
		t.Fatalf("Default().SchemaVersion = %d, want %d", got, CurrentSchemaVersion)
	}
}

func TestScaffoldContainsSchemaVersion(t *testing.T) {
	var b strings.Builder
	if err := WriteScaffold(&b); err != nil {
		t.Fatalf("WriteScaffold: %v", err)
	}
	want := fmt.Sprintf("schema_version: %d", CurrentSchemaVersion)
	if !strings.Contains(b.String(), want) {
		t.Fatalf("scaffold missing %q:\n%s", want, b.String())
	}
}

func TestScaffoldParsesAndIsCurrent(t *testing.T) {
	var b strings.Builder
	if err := WriteScaffold(&b); err != nil {
		t.Fatalf("WriteScaffold: %v", err)
	}
	c := Default()
	if err := yaml.Unmarshal([]byte(b.String()), &c); err != nil {
		t.Fatalf("scaffold does not parse: %v", err)
	}
	if c.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("scaffold SchemaVersion = %d, want %d", c.SchemaVersion, CurrentSchemaVersion)
	}
}
```

Ensure the test file imports `fmt`, `strings`, and `gopkg.in/yaml.v3` (add any missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'SchemaVersion|Scaffold' -v`
Expected: FAIL — `undefined: CurrentSchemaVersion` and/or `Config` has no field `SchemaVersion`.

- [ ] **Step 3: Implement**

In `internal/config/config.go`, add the constant near the top (after the imports):

```go
// CurrentSchemaVersion is the config schema version the current binary
// expects. Bumped by +1 in the same PR that appends a migration to
// internal/config/migrations.go. Baseline is 1 (the shape at first ship).
const CurrentSchemaVersion = 1
```

Add the field to `Config` (make it the first field so it stamps at the top of a marshalled file):

```go
type Config struct {
	SchemaVersion int              `yaml:"schema_version" json:"schema_version"`
	LabBridge     LabBridgeConfig  `yaml:"lab_bridge" json:"lab_bridge"`
	Rest          RestConfig       `yaml:"rest" json:"rest"`
	Discovery     DiscoveryConfig  `yaml:"discovery" json:"discovery"`
	Log           LogConfig        `yaml:"log" json:"log"`
	AutoUpdate    AutoUpdateConfig `yaml:"auto_update" json:"auto_update"`
	Flashing      FlashingConfig   `yaml:"flashing" json:"flashing"`
	RawSerial     RawSerialConfig  `yaml:"raw_serial" json:"raw_serial"`
}
```

Set it in `Default()` — add as the first field of the returned literal:

```go
	return Config{
		SchemaVersion: CurrentSchemaVersion,
		LabBridge: LabBridgeConfig{
```

Add the `schema_version` line to the top of `scaffoldTemplate`, right after the header comment block and before `lab_bridge:`:

```
# level for downgrade safety.

schema_version: 1         # config schema version. Managed automatically by
                          # SerialHop's migration tooling — do not edit by hand.

lab_bridge:
```

(Place the `schema_version:` block immediately before the existing `lab_bridge:` line. The literal `1` is correct at baseline; a future schema bump updates this line in the same PR that bumps `CurrentSchemaVersion`, enforced by the drift guard in Task 9.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run 'SchemaVersion|Scaffold' -v`
Expected: PASS.

- [ ] **Step 5: Run the whole config package + gofmt**

Run: `gofmt -l internal/config/ && go test ./internal/config/ -count=1`
Expected: no gofmt output; PASS. (Existing `json_test.go`/`config_test.go` golden assertions may reference the struct shape — if any fail because a golden now includes `schema_version`, update that golden to include the new field.)

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: add schema_version to config and scaffold"
```

---

### Task 2: Migration and Op types + empty registry + invariant test

**Files:**
- Create: `internal/config/migrations.go`
- Create: `internal/config/migrate.go` (types only in this task)
- Test: `internal/config/migrate_test.go`

**Interfaces:**
- Consumes: `CurrentSchemaVersion` (Task 1).
- Produces:
  - `type Change struct { Kind, Path, Detail string }` (json tags `kind`,`path`,`detail`).
  - `type Op interface { apply(top *yaml.Node, toVersion int) ([]Change, error) }`.
  - `type Migration struct { To int; Desc string; Ops []Op }`.
  - `var migrations []Migration` (empty at ship).

- [ ] **Step 1: Write the failing test**

Create `internal/config/migrate_test.go`:

```go
package config

import "testing"

func TestRegistryInvariants(t *testing.T) {
	// Migrations must be contiguous, ascending, starting at 2.
	want := 2
	for i, m := range migrations {
		if m.To != want {
			t.Fatalf("migrations[%d].To = %d, want %d (must be contiguous from 2)", i, m.To, want)
		}
		if m.Desc == "" {
			t.Fatalf("migrations[%d] (To=%d) has empty Desc", i, m.To)
		}
		want++
	}
	// CurrentSchemaVersion must equal the last migration's To, or 1 when empty.
	expected := 1
	if n := len(migrations); n > 0 {
		expected = migrations[n-1].To
	}
	if CurrentSchemaVersion != expected {
		t.Fatalf("CurrentSchemaVersion = %d, want %d (last migration To or 1)", CurrentSchemaVersion, expected)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestRegistryInvariants -v`
Expected: FAIL — `undefined: migrations`.

- [ ] **Step 3: Implement the types and registry**

Create `internal/config/migrate.go` with the shared types (engine functions come in later tasks):

```go
package config

import "gopkg.in/yaml.v3"

// Change records a single mutation an Op made, for logging and reports.
type Change struct {
	Kind   string `json:"kind"`   // rename | remove | add | map-value | map-node
	Path   string `json:"path"`   // dotted config path affected
	Detail string `json:"detail"` // human-readable specifics
}

// Op is one typed transformation applied to the parsed config document.
// Implementations are no-ops when their target path is absent (except Add,
// which inserts). toVersion is the schema version the enclosing migration
// produces, used e.g. in the "removed in schema vN" marker.
type Op interface {
	apply(top *yaml.Node, toVersion int) ([]Change, error)
}

// Migration is one ordered step transforming the config from schema To-1 to To.
type Migration struct {
	To   int
	Desc string
	Ops  []Op
}
```

Create `internal/config/migrations.go`:

```go
package config

// migrations is the ordered, APPEND-ONLY history of config schema changes.
// Never edit or renumber an existing entry — see CLAUDE.md ("Registering
// config changes"). Each config-changing PR appends exactly one Migration and
// bumps CurrentSchemaVersion (in config.go) by 1.
//
// Ships empty at baseline (CurrentSchemaVersion == 1): this delivers the
// migration infrastructure only. The first real migration lands as {To: 2, ...}.
var migrations = []Migration{}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestRegistryInvariants -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/migrate.go internal/config/migrations.go internal/config/migrate_test.go
git commit -m "feat: add config migration registry and invariants"
```

---

### Task 3: Dotted-path and node helpers

**Files:**
- Modify: `internal/config/migrate.go`
- Test: `internal/config/migrate_test.go`

**Interfaces:**
- Produces (all unexported, package `config`):
  - `func docMapping(root *yaml.Node) *yaml.Node` — top mapping of a document, or nil.
  - `func childIndex(parent *yaml.Node, key string) int` — index of key node, or -1.
  - `func childValue(parent *yaml.Node, key string) *yaml.Node` — value node, or nil.
  - `func resolvePath(top *yaml.Node, path string) (parent *yaml.Node, leaf string, value *yaml.Node, ok bool)` — walk existing maps; ok=false if an intermediate segment is missing/not a map.
  - `func ensureParent(top *yaml.Node, path string) (parent *yaml.Node, leaf string)` — walk, creating missing intermediate maps.

- [ ] **Step 1: Write the failing test**

Add to `internal/config/migrate_test.go` (add imports `strings`, `gopkg.in/yaml.v3`):

```go
func parseDoc(t *testing.T, s string) *yaml.Node {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(s), &root); err != nil {
		t.Fatalf("parse: %v", err)
	}
	top := docMapping(&root)
	if top == nil {
		t.Fatalf("no top mapping in %q", s)
	}
	return top
}

func TestResolvePath(t *testing.T) {
	top := parseDoc(t, "rest:\n  port: 8080\nlog:\n  level: info\n")

	parent, leaf, val, ok := resolvePath(top, "rest.port")
	if !ok || val == nil || leaf != "port" {
		t.Fatalf("resolvePath rest.port: ok=%v leaf=%q val=%v", ok, leaf, val)
	}
	if val.Value != "8080" || parent == nil {
		t.Fatalf("rest.port value = %q", val.Value)
	}

	if _, _, _, ok := resolvePath(top, "rest.missing"); ok {
		// present-parent, absent-leaf still returns ok=true with val=nil.
		_, _, v, ok2 := resolvePath(top, "rest.missing")
		if !ok2 || v != nil {
			t.Fatalf("absent leaf: ok=%v v=%v", ok2, v)
		}
	}

	if _, _, _, ok := resolvePath(top, "nope.deep.path"); ok {
		t.Fatalf("resolvePath through missing parent should be ok=false")
	}
}

func TestEnsureParentCreates(t *testing.T) {
	top := parseDoc(t, "rest:\n  port: 8080\n")
	parent, leaf := ensureParent(top, "new_block.child")
	if leaf != "child" {
		t.Fatalf("leaf = %q", leaf)
	}
	if childValue(top, "new_block") == nil {
		t.Fatalf("ensureParent did not create new_block")
	}
	if parent != childValue(top, "new_block") {
		t.Fatalf("ensureParent returned wrong parent")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'ResolvePath|EnsureParent' -v`
Expected: FAIL — `undefined: docMapping` etc.

- [ ] **Step 3: Implement the helpers**

Add to `internal/config/migrate.go` (add `"strings"` to imports):

```go
func docMapping(root *yaml.Node) *yaml.Node {
	if root == nil || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

func childIndex(parent *yaml.Node, key string) int {
	if parent == nil {
		return -1
	}
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Kind == yaml.ScalarNode && parent.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func childValue(parent *yaml.Node, key string) *yaml.Node {
	if i := childIndex(parent, key); i >= 0 {
		return parent.Content[i+1]
	}
	return nil
}

// resolvePath walks a dotted path from top through existing mappings.
// Returns the parent mapping, the leaf key, and the leaf value node (nil if
// the leaf itself is absent). ok is false only if an intermediate segment is
// missing or is not a mapping.
func resolvePath(top *yaml.Node, path string) (parent *yaml.Node, leaf string, value *yaml.Node, ok bool) {
	segs := strings.Split(path, ".")
	parent = top
	for _, seg := range segs[:len(segs)-1] {
		v := childValue(parent, seg)
		if v == nil || v.Kind != yaml.MappingNode {
			return nil, "", nil, false
		}
		parent = v
	}
	leaf = segs[len(segs)-1]
	return parent, leaf, childValue(parent, leaf), true
}

// ensureParent walks a dotted path, creating any missing intermediate
// mappings, and returns the leaf's parent mapping and the leaf key.
func ensureParent(top *yaml.Node, path string) (parent *yaml.Node, leaf string) {
	segs := strings.Split(path, ".")
	parent = top
	for _, seg := range segs[:len(segs)-1] {
		v := childValue(parent, seg)
		if v == nil || v.Kind != yaml.MappingNode {
			v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			parent.Content = append(parent.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: seg}, v)
		}
		parent = v
	}
	return parent, segs[len(segs)-1]
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run 'ResolvePath|EnsureParent' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/migrate.go internal/config/migrate_test.go
git commit -m "feat: add config migration path helpers"
```

---

### Task 4: `Rename` op

**Files:**
- Modify: `internal/config/migrate.go`
- Test: `internal/config/migrate_test.go`

**Interfaces:**
- Consumes: helpers from Task 3, `commentOutChild` — **defined in Task 5**. To avoid a forward dependency, Task 4 implements `Rename`'s move path only and, for the "destination already set" branch, deletes the source pair outright with a recorded Change; Task 5 upgrades that branch to comment-out. (This keeps each task independently green.)
- Produces: `func Rename(from, to string) Op`.

- [ ] **Step 1: Write the failing test**

Add to `migrate_test.go`:

```go
// applyOps runs ops against a parsed doc and returns the re-marshalled YAML.
func applyOps(t *testing.T, src string, toVersion int, ops ...Op) (string, []Change) {
	t.Helper()
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		t.Fatalf("parse: %v", err)
	}
	top := docMapping(&root)
	var all []Change
	for _, op := range ops {
		cs, err := op.apply(top, toVersion)
		if err != nil {
			t.Fatalf("apply: %v", err)
		}
		all = append(all, cs...)
	}
	out, err := yaml.Marshal(&root)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(out), all
}

func TestRenameMovesValueAndComment(t *testing.T) {
	src := "rest:\n  port: 8080 # the port\n"
	out, changes := applyOps(t, src, 2, Rename("rest.port", "rest.listen_port"))
	if strings.Contains(out, "port: 8080") && !strings.Contains(out, "listen_port") {
		t.Fatalf("rename did not happen:\n%s", out)
	}
	if !strings.Contains(out, "listen_port: 8080") {
		t.Fatalf("value not carried to new key:\n%s", out)
	}
	if !strings.Contains(out, "the port") {
		t.Fatalf("inline comment not carried:\n%s", out)
	}
	if len(changes) != 1 || changes[0].Kind != "rename" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestRenameAbsentSourceIsNoop(t *testing.T) {
	src := "rest:\n  port: 8080\n"
	out, changes := applyOps(t, src, 2, Rename("rest.nope", "rest.other"))
	if len(changes) != 0 {
		t.Fatalf("expected no changes, got %+v", changes)
	}
	if !strings.Contains(out, "port: 8080") {
		t.Fatalf("no-op altered doc:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestRename -v`
Expected: FAIL — `undefined: Rename`.

- [ ] **Step 3: Implement**

Add to `migrate.go` (add `"fmt"` to imports):

```go
func Rename(from, to string) Op { return renameOp{from: from, to: to} }

type renameOp struct{ from, to string }

func (o renameOp) apply(top *yaml.Node, _ int) ([]Change, error) {
	srcParent, srcKey, srcVal, ok := resolvePath(top, o.from)
	if !ok || srcVal == nil {
		return nil, nil // source absent -> no-op
	}
	dstParent, dstKey := ensureParent(top, o.to)
	if childValue(dstParent, dstKey) != nil {
		// Destination already populated. Drop the stale source. (Task 5
		// upgrades this to comment-out via commentOutChild.)
		i := childIndex(srcParent, srcKey)
		srcParent.Content = append(srcParent.Content[:i], srcParent.Content[i+2:]...)
		return []Change{{Kind: "rename", Path: o.from,
			Detail: fmt.Sprintf("%s already set; dropped %s", o.to, o.from)}}, nil
	}
	i := childIndex(srcParent, srcKey)
	keyNode := srcParent.Content[i]
	valNode := srcParent.Content[i+1]
	srcParent.Content = append(srcParent.Content[:i], srcParent.Content[i+2:]...)
	keyNode.Value = dstKey // carries the key node's head/line comments
	dstParent.Content = append(dstParent.Content, keyNode, valNode)
	return []Change{{Kind: "rename", Path: o.from, Detail: o.from + " -> " + o.to}}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestRename -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/migrate.go internal/config/migrate_test.go
git commit -m "feat: add config migration Rename op"
```

---

### Task 5: Comment-out helper + `Remove` op + Rename superseded branch

**Files:**
- Modify: `internal/config/migrate.go`
- Test: `internal/config/migrate_test.go`

**Interfaces:**
- Produces:
  - `func Remove(path string) Op`.
  - `func commentOutChild(parent *yaml.Node, key, marker string) bool` (unexported).
- Modifies: `renameOp.apply` destination-occupied branch to call `commentOutChild(srcParent, srcKey, "superseded by <to>")`.

- [ ] **Step 1: Write the failing test**

Add to `migrate_test.go`:

```go
func TestRemoveCommentsOutAndPreservesValue(t *testing.T) {
	src := "discovery:\n  legacy_scan: true\n  post_open_settle_ms: 2000\n"
	out, changes := applyOps(t, src, 3, Remove("discovery.legacy_scan"))
	// The key is no longer active YAML...
	var parsed struct {
		Discovery struct {
			LegacyScan *bool `yaml:"legacy_scan"`
		} `yaml:"discovery"`
	}
	if err := yaml.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("reparse: %v", err)
	}
	if parsed.Discovery.LegacyScan != nil {
		t.Fatalf("legacy_scan still active:\n%s", out)
	}
	// ...but preserved as a comment with the schema-version marker and value.
	if !strings.Contains(out, "removed in schema v3") {
		t.Fatalf("missing removal marker:\n%s", out)
	}
	if !strings.Contains(out, "# legacy_scan: true") {
		t.Fatalf("removed value not preserved in comment:\n%s", out)
	}
	if len(changes) != 1 || changes[0].Kind != "remove" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestRenameSupersededCommentsOutSource(t *testing.T) {
	src := "rest:\n  port: 8080\n  listen_port: 9090\n"
	out, _ := applyOps(t, src, 2, Rename("rest.port", "rest.listen_port"))
	if !strings.Contains(out, "listen_port: 9090") {
		t.Fatalf("existing destination clobbered:\n%s", out)
	}
	if !strings.Contains(out, "superseded by rest.listen_port") {
		t.Fatalf("source not commented as superseded:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'TestRemove|Superseded' -v`
Expected: FAIL — `undefined: Remove` / superseded marker absent.

- [ ] **Step 3: Implement**

Add to `migrate.go`:

```go
func Remove(path string) Op { return removeOp{path: path} }

type removeOp struct{ path string }

func (o removeOp) apply(top *yaml.Node, toVersion int) ([]Change, error) {
	parent, leaf, val, ok := resolvePath(top, o.path)
	if !ok || val == nil {
		return nil, nil
	}
	commentOutChild(parent, leaf, fmt.Sprintf("removed in schema v%d (no longer used):", toVersion))
	return []Change{{Kind: "remove", Path: o.path, Detail: "commented out"}}, nil
}

// commentOutChild removes key from the mapping parent and preserves the
// removed "key: value" block as a comment attached to a neighbouring node,
// prefixed with marker. Returns false if key was absent.
func commentOutChild(parent *yaml.Node, key, marker string) bool {
	i := childIndex(parent, key)
	if i < 0 {
		return false
	}
	frag := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{parent.Content[i], parent.Content[i+1]}}
	raw, err := yaml.Marshal(frag)
	if err != nil {
		raw = []byte(key + ": ...")
	}
	block := commentPrefix(marker, string(raw))
	parent.Content = append(parent.Content[:i], parent.Content[i+2:]...)
	switch {
	case i < len(parent.Content):
		next := parent.Content[i]
		next.HeadComment = joinComments(block, next.HeadComment)
	case len(parent.Content) > 0:
		prev := parent.Content[len(parent.Content)-1]
		prev.FootComment = joinComments(prev.FootComment, block)
	default:
		parent.FootComment = joinComments(parent.FootComment, block)
	}
	return true
}

// commentPrefix turns raw text into YAML comment lines (each prefixed with
// "# "), preceded by an optional marker line.
func commentPrefix(marker, raw string) string {
	var b strings.Builder
	if marker != "" {
		b.WriteString("# " + marker + "\n")
	}
	for _, line := range strings.Split(strings.TrimRight(raw, "\n"), "\n") {
		if line == "" {
			b.WriteString("#\n")
		} else {
			b.WriteString("# " + line + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// joinComments concatenates two comment blocks, skipping empties.
func joinComments(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "\n" + b
	}
}
```

Update `renameOp.apply`'s destination-occupied branch (replace the delete-outright block from Task 4):

```go
	if childValue(dstParent, dstKey) != nil {
		commentOutChild(srcParent, srcKey, "superseded by "+o.to+":")
		return []Change{{Kind: "rename", Path: o.from,
			Detail: fmt.Sprintf("%s already set; %s commented out", o.to, o.from)}}, nil
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run 'TestRemove|Superseded|TestRename' -v`
Expected: PASS (all rename + remove tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/migrate.go internal/config/migrate_test.go
git commit -m "feat: add config migration Remove op and comment-out helper"
```

---

### Task 6: `Add` op

**Files:**
- Modify: `internal/config/migrate.go`
- Test: `internal/config/migrate_test.go`

**Interfaces:**
- Produces: `func Add(path, snippet string) Op`. `snippet` is a self-contained `key: value` YAML block (optionally with leading comments) whose top key equals the final segment of `path`.

- [ ] **Step 1: Write the failing test**

Add to `migrate_test.go`:

```go
func TestAddInjectsWhenAbsent(t *testing.T) {
	src := "rest:\n  port: 8080\n"
	snippet := "# raw serial access (added in schema v4)\nraw_serial:\n  enabled: false # off by default\n"
	out, changes := applyOps(t, src, 4, Add("raw_serial", snippet))
	if !strings.Contains(out, "raw_serial:") || !strings.Contains(out, "enabled: false") {
		t.Fatalf("block not added:\n%s", out)
	}
	if !strings.Contains(out, "added in schema v4") {
		t.Fatalf("snippet comment not preserved:\n%s", out)
	}
	if len(changes) != 1 || changes[0].Kind != "add" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestAddRespectsExistingValue(t *testing.T) {
	src := "raw_serial:\n  enabled: true\n"
	snippet := "raw_serial:\n  enabled: false\n"
	out, changes := applyOps(t, src, 4, Add("raw_serial", snippet))
	if !strings.Contains(out, "enabled: true") {
		t.Fatalf("Add clobbered operator value:\n%s", out)
	}
	if len(changes) != 0 {
		t.Fatalf("expected no changes when key present, got %+v", changes)
	}
}

func TestAddNestedLeaf(t *testing.T) {
	src := "flashing:\n  enabled: false\n"
	out, _ := applyOps(t, src, 5, Add("flashing.keep_n", "keep_n: 10 # retain N backups\n"))
	if !strings.Contains(out, "keep_n: 10") {
		t.Fatalf("nested leaf not added:\n%s", out)
	}
}

func TestAddKeyMismatchErrors(t *testing.T) {
	var root yaml.Node
	_ = yaml.Unmarshal([]byte("rest:\n  port: 8080\n"), &root)
	top := docMapping(&root)
	_, err := Add("raw_serial", "wrong_key: 1\n").apply(top, 4)
	if err == nil {
		t.Fatalf("expected error on key/path mismatch")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestAdd -v`
Expected: FAIL — `undefined: Add`.

- [ ] **Step 3: Implement**

Add to `migrate.go`:

```go
func Add(path, snippet string) Op { return addOp{path: path, snippet: snippet} }

type addOp struct{ path, snippet string }

func (o addOp) apply(top *yaml.Node, _ int) ([]Change, error) {
	if _, _, val, ok := resolvePath(top, o.path); ok && val != nil {
		return nil, nil // already present -> respect operator's value
	}
	var frag yaml.Node
	if err := yaml.Unmarshal([]byte(o.snippet), &frag); err != nil {
		return nil, fmt.Errorf("Add %q: parse snippet: %w", o.path, err)
	}
	fm := docMapping(&frag)
	if fm == nil || len(fm.Content) < 2 {
		return nil, fmt.Errorf("Add %q: snippet must be a single key: value block", o.path)
	}
	keyNode, valNode := fm.Content[0], fm.Content[1]
	segs := strings.Split(o.path, ".")
	last := segs[len(segs)-1]
	if keyNode.Value != last {
		return nil, fmt.Errorf("Add %q: snippet key %q must equal final path segment %q", o.path, keyNode.Value, last)
	}
	parent, _ := ensureParent(top, o.path)
	parent.Content = append(parent.Content, keyNode, valNode)
	return []Change{{Kind: "add", Path: o.path, Detail: "inserted"}}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestAdd -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/migrate.go internal/config/migrate_test.go
git commit -m "feat: add config migration Add op"
```

---

### Task 7: `MapValue` and `MapNode` ops

**Files:**
- Modify: `internal/config/migrate.go`
- Test: `internal/config/migrate_test.go`

**Interfaces:**
- Produces:
  - `func MapValue(path string, fn func(string) string) Op`.
  - `func MapNode(path string, fn func(*yaml.Node) error) Op`.

- [ ] **Step 1: Write the failing test**

Add to `migrate_test.go`:

```go
func TestMapValueRewritesScalar(t *testing.T) {
	src := "log:\n  level: verbose\n"
	out, changes := applyOps(t, src, 2, MapValue("log.level", func(v string) string {
		if v == "verbose" {
			return "debug"
		}
		return v
	}))
	if !strings.Contains(out, "level: debug") {
		t.Fatalf("value not remapped:\n%s", out)
	}
	if len(changes) != 1 || changes[0].Kind != "map-value" {
		t.Fatalf("changes = %+v", changes)
	}
}

func TestMapValueUnchangedIsNoop(t *testing.T) {
	src := "log:\n  level: info\n"
	_, changes := applyOps(t, src, 2, MapValue("log.level", func(v string) string { return v }))
	if len(changes) != 0 {
		t.Fatalf("expected no change, got %+v", changes)
	}
}

func TestMapNodeTransforms(t *testing.T) {
	src := "discovery:\n  include: COM3\n"
	out, changes := applyOps(t, src, 2, MapNode("discovery.include", func(n *yaml.Node) error {
		// promote a scalar to a one-element sequence
		if n.Kind == yaml.ScalarNode {
			v := *n
			n.Kind = yaml.SequenceNode
			n.Tag = "!!seq"
			n.Value = ""
			n.Content = []*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: v.Value}}
		}
		return nil
	}))
	if !strings.Contains(out, "- COM3") {
		t.Fatalf("MapNode did not transform:\n%s", out)
	}
	if len(changes) != 1 || changes[0].Kind != "map-node" {
		t.Fatalf("changes = %+v", changes)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run 'MapValue|MapNode' -v`
Expected: FAIL — `undefined: MapValue`.

- [ ] **Step 3: Implement**

Add to `migrate.go`:

```go
func MapValue(path string, fn func(string) string) Op { return mapValueOp{path: path, fn: fn} }

type mapValueOp struct {
	path string
	fn   func(string) string
}

func (o mapValueOp) apply(top *yaml.Node, _ int) ([]Change, error) {
	_, _, val, ok := resolvePath(top, o.path)
	if !ok || val == nil || val.Kind != yaml.ScalarNode {
		return nil, nil
	}
	old := val.Value
	nv := o.fn(old)
	if nv == old {
		return nil, nil
	}
	val.Value = nv
	return []Change{{Kind: "map-value", Path: o.path, Detail: fmt.Sprintf("value %s -> %s", old, nv)}}, nil
}

func MapNode(path string, fn func(*yaml.Node) error) Op { return mapNodeOp{path: path, fn: fn} }

type mapNodeOp struct {
	path string
	fn   func(*yaml.Node) error
}

func (o mapNodeOp) apply(top *yaml.Node, _ int) ([]Change, error) {
	_, _, val, ok := resolvePath(top, o.path)
	if !ok || val == nil {
		return nil, nil
	}
	if err := o.fn(val); err != nil {
		return nil, fmt.Errorf("MapNode %q: %w", o.path, err)
	}
	return []Change{{Kind: "map-node", Path: o.path, Detail: "transformed"}}, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run 'MapValue|MapNode' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/migrate.go internal/config/migrate_test.go
git commit -m "feat: add config migration MapValue and MapNode ops"
```

---

### Task 8: `EnsureMigrated` orchestration + file handling

**Files:**
- Modify: `internal/config/migrate.go`
- Test: `internal/config/migrate_test.go`

**Interfaces:**
- Produces:
  - `type Report struct { From, To int; Migrated bool; Changes []Change; BackupPath string }` (json tags `from`,`to`,`migrated`,`changes`,`backup_path`).
  - `func EnsureMigrated(path string) (Report, error)` — uses package `migrations` + `CurrentSchemaVersion`.
  - `func ensureMigrated(path string, migs []Migration, current int) (Report, error)` — test seam.
  - Unexported: `readSchemaVersion`, `stampSchemaVersion`, `migrateTree`, `backupPath`, `atomicWrite`.

- [ ] **Step 1: Write the failing test**

Add to `migrate_test.go` (add imports `os`, `path/filepath`, `strconv`):

```go
func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// synthetic 2-step registry for exercising the write path (real registry is empty at ship)
func testMigs() []Migration {
	return []Migration{
		{To: 2, Desc: "rename rest.port", Ops: []Op{Rename("rest.port", "rest.listen_port")}},
		{To: 3, Desc: "remove legacy", Ops: []Op{Remove("discovery.legacy")}},
	}
}

func TestEnsureMigratedAppliesAndBacksUp(t *testing.T) {
	p := writeTemp(t, "SerialHop_config.yaml",
		"schema_version: 1\nrest:\n  port: 8080\ndiscovery:\n  legacy: true\n")
	rep, err := ensureMigrated(p, testMigs(), 3)
	if err != nil {
		t.Fatalf("ensureMigrated: %v", err)
	}
	if !rep.Migrated || rep.From != 1 || rep.To != 3 {
		t.Fatalf("report = %+v", rep)
	}
	data, _ := os.ReadFile(p) //nolint:gosec
	got := string(data)
	if !strings.Contains(got, "listen_port: 8080") {
		t.Fatalf("rename not applied:\n%s", got)
	}
	if !strings.Contains(got, "schema_version: 3") {
		t.Fatalf("version not stamped:\n%s", got)
	}
	if rep.BackupPath == "" {
		t.Fatalf("no backup path")
	}
	bak, err := os.ReadFile(rep.BackupPath) //nolint:gosec
	if err != nil || !strings.Contains(string(bak), "port: 8080") {
		t.Fatalf("backup missing original: err=%v", err)
	}
}

func TestEnsureMigratedIdempotent(t *testing.T) {
	p := writeTemp(t, "SerialHop_config.yaml", "schema_version: 3\nrest:\n  listen_port: 8080\n")
	rep, err := ensureMigrated(p, testMigs(), 3)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if rep.Migrated {
		t.Fatalf("already-current file should not migrate: %+v", rep)
	}
}

func TestEnsureMigratedDowngradeGuard(t *testing.T) {
	body := "schema_version: 99\nrest:\n  listen_port: 8080\n"
	p := writeTemp(t, "SerialHop_config.yaml", body)
	rep, err := ensureMigrated(p, testMigs(), 3)
	if err != nil || rep.Migrated {
		t.Fatalf("downgrade should be no-op: rep=%+v err=%v", rep, err)
	}
	data, _ := os.ReadFile(p) //nolint:gosec
	if string(data) != body {
		t.Fatalf("downgrade guard rewrote the file:\n%s", data)
	}
}

func TestEnsureMigratedUnparseableUntouched(t *testing.T) {
	body := "this: : : not: valid: yaml\n\t- broken\n"
	p := writeTemp(t, "SerialHop_config.yaml", body)
	rep, err := ensureMigrated(p, testMigs(), 3)
	if err != nil {
		t.Fatalf("unparseable should not error: %v", err)
	}
	if rep.Migrated {
		t.Fatalf("unparseable should be no-op")
	}
	data, _ := os.ReadFile(p) //nolint:gosec
	if string(data) != body {
		t.Fatalf("unparseable file was rewritten")
	}
}

func TestEnsureMigratedMissingFileIsNoop(t *testing.T) {
	rep, err := ensureMigrated(filepath.Join(t.TempDir(), "nope.yaml"), testMigs(), 3)
	if err != nil || rep.Migrated {
		t.Fatalf("missing file: rep=%+v err=%v", rep, err)
	}
}

func TestEnsureMigratedFailureLeavesFileUntouched(t *testing.T) {
	body := "schema_version: 1\nrest:\n  port: 8080\n"
	p := writeTemp(t, "SerialHop_config.yaml", body)
	bad := []Migration{{To: 2, Desc: "boom", Ops: []Op{
		MapNode("rest.port", func(*yaml.Node) error { return fmt.Errorf("boom") }),
	}}}
	_, err := ensureMigrated(p, bad, 2)
	if err == nil {
		t.Fatalf("expected error from failing op")
	}
	data, _ := os.ReadFile(p) //nolint:gosec
	if string(data) != body {
		t.Fatalf("failing migration rewrote the file:\n%s", data)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestEnsureMigrated -v`
Expected: FAIL — `undefined: ensureMigrated`.

- [ ] **Step 3: Implement**

Add to `migrate.go` (add imports `errors`, `os`, `path/filepath`, `strconv`):

```go
// Report describes the outcome of an EnsureMigrated call.
type Report struct {
	From       int      `json:"from"`
	To         int      `json:"to"`
	Migrated   bool     `json:"migrated"`
	Changes    []Change `json:"changes"`
	BackupPath string   `json:"backup_path"`
}

// EnsureMigrated migrates the config file at path up to CurrentSchemaVersion,
// rewriting it in place if needed. Idempotent; safe under concurrent callers
// (deterministic + atomic write). Never writes a partial file.
func EnsureMigrated(path string) (Report, error) {
	return ensureMigrated(path, migrations, CurrentSchemaVersion)
}

func ensureMigrated(path string, migs []Migration, current int) (Report, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.ConfigPath()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Report{}, nil // first-run scaffold path owns creation
		}
		return Report{}, err
	}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return Report{}, nil // unparseable: leave for Load to surface
	}
	top := docMapping(&root)
	if top == nil {
		return Report{}, nil
	}
	from := readSchemaVersion(top)
	if from >= current {
		return Report{From: from, To: from}, nil // current or downgrade guard
	}
	changes, err := migrateTree(top, from, migs)
	if err != nil {
		return Report{}, err // partial doc discarded; file untouched
	}
	stampSchemaVersion(top, current)
	out, err := yaml.Marshal(&root)
	if err != nil {
		return Report{}, fmt.Errorf("marshal migrated config: %w", err)
	}
	backup := backupPath(path, from)
	if err := os.WriteFile(backup, data, 0o600); err != nil {
		return Report{}, fmt.Errorf("write backup %s: %w", backup, err)
	}
	if err := atomicWrite(path, out, 0o600); err != nil {
		return Report{}, err
	}
	return Report{From: from, To: current, Migrated: true, Changes: changes, BackupPath: backup}, nil
}

// migrateTree applies every migration in migs whose To > from, in list order.
func migrateTree(top *yaml.Node, from int, migs []Migration) ([]Change, error) {
	var changes []Change
	for _, m := range migs {
		if m.To <= from {
			continue
		}
		for _, op := range m.Ops {
			cs, err := op.apply(top, m.To)
			if err != nil {
				return nil, fmt.Errorf("migrate to v%d (%s): %w", m.To, m.Desc, err)
			}
			changes = append(changes, cs...)
		}
	}
	return changes, nil
}

func readSchemaVersion(top *yaml.Node) int {
	if v := childValue(top, "schema_version"); v != nil && v.Kind == yaml.ScalarNode {
		if n, err := strconv.Atoi(strings.TrimSpace(v.Value)); err == nil {
			return n
		}
	}
	return 1 // baseline: absent or unparseable version
}

func stampSchemaVersion(top *yaml.Node, ver int) {
	s := strconv.Itoa(ver)
	if i := childIndex(top, "schema_version"); i >= 0 {
		top.Content[i+1].Value = s
		top.Content[i+1].Tag = "!!int"
		return
	}
	top.Content = append([]*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "schema_version"},
		{Kind: yaml.ScalarNode, Tag: "!!int", Value: s},
	}, top.Content...)
}

// backupPath returns "<dir>/<stem>.v<from>.bak<ext>" next to configPath.
func backupPath(configPath string, from int) string {
	dir := filepath.Dir(configPath)
	base := filepath.Base(configPath)
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	return filepath.Join(dir, fmt.Sprintf("%s.v%d.bak%s", stem, from, ext))
}

// atomicWrite writes data to path via a temp file + rename in the same dir.
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-cfg-*")
	if err != nil {
		return fmt.Errorf("atomicWrite: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWrite: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWrite: close: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWrite: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWrite: rename: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestEnsureMigrated -v`
Expected: PASS (all six cases).

- [ ] **Step 5: Full package + race**

Run: `go test -race -count=1 ./internal/config/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/migrate.go internal/config/migrate_test.go
git commit -m "feat: add EnsureMigrated orchestration"
```

---

### Task 9: Drift guard + baseline-v1 fixture

**Files:**
- Create: `internal/config/testdata/migrations/baseline-v1.yaml`
- Modify: `internal/config/migrate_test.go`

**Interfaces:**
- Consumes: `WriteScaffold`, `migrateTree`, `docMapping`, `migrations`, `CurrentSchemaVersion`.

- [ ] **Step 1: Create the frozen baseline fixture**

Generate it from the current scaffold so it captures the v1 key set exactly:

```bash
go run ./tools/... 2>/dev/null || true   # (no tool needed; use the helper below)
```

Instead, create the fixture by capturing the scaffold once. Add a throwaway helper test, run it, copy output — OR write the fixture directly. Since the scaffold IS the v1 shape, create `internal/config/testdata/migrations/baseline-v1.yaml` as a byte-for-byte copy of the scaffold produced by `WriteScaffold` (Task 1). Use this command to write it:

```bash
cat > /tmp/gen_baseline.go <<'EOF'
//go:build ignore
package main
import ("os"; "github.com/bioexperiment-lab-devices/serialhop/internal/config")
func main(){ f,_:=os.Create("internal/config/testdata/migrations/baseline-v1.yaml"); _=config.WriteScaffold(f); _=f.Close() }
EOF
mkdir -p internal/config/testdata/migrations
go run /tmp/gen_baseline.go
rm /tmp/gen_baseline.go
```

Verify the file exists and contains `schema_version: 1`.

- [ ] **Step 2: Write the drift-guard test**

Add to `migrate_test.go`:

```go
// keyPaths returns the set of dotted key paths in a YAML mapping document.
func keyPaths(t *testing.T, data []byte) map[string]bool {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		t.Fatalf("keyPaths parse: %v", err)
	}
	out := map[string]bool{}
	var walk func(prefix string, v any)
	walk = func(prefix string, v any) {
		mm, ok := v.(map[string]any)
		if !ok {
			return
		}
		for k, cv := range mm {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			out[p] = true
			walk(p, cv)
		}
	}
	walk("", m)
	return out
}

func TestScaffoldMatchesMigratedBaseline(t *testing.T) {
	baseline, err := os.ReadFile("testdata/migrations/baseline-v1.yaml") //nolint:gosec
	if err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	var root yaml.Node
	if err := yaml.Unmarshal(baseline, &root); err != nil {
		t.Fatalf("parse baseline: %v", err)
	}
	top := docMapping(&root)
	if _, err := migrateTree(top, 1, migrations); err != nil {
		t.Fatalf("replay migrations on baseline: %v", err)
	}
	stampSchemaVersion(top, CurrentSchemaVersion)
	migrated, err := yaml.Marshal(&root)
	if err != nil {
		t.Fatalf("marshal migrated baseline: %v", err)
	}

	var sb strings.Builder
	if err := WriteScaffold(&sb); err != nil {
		t.Fatalf("WriteScaffold: %v", err)
	}
	scaffold := []byte(sb.String())

	got := keyPaths(t, migrated)
	want := keyPaths(t, scaffold)
	for k := range want {
		if !got[k] {
			t.Errorf("scaffold has key %q that baseline+migrations does not produce — missing Add migration?", k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("baseline+migrations produces key %q absent from scaffold — scaffold not updated?", k)
		}
	}

	// Both must be stamped current.
	var s struct{ SchemaVersion int `yaml:"schema_version"` }
	_ = yaml.Unmarshal(scaffold, &s)
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("scaffold schema_version = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
}
```

- [ ] **Step 3: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestScaffoldMatchesMigratedBaseline -v`
Expected: PASS (at ship, baseline == scaffold, empty registry → trivially equal key sets, both stamped 1).

- [ ] **Step 4: Commit**

```bash
git add internal/config/testdata/migrations/baseline-v1.yaml internal/config/migrate_test.go
git commit -m "test: add config migration drift guard and baseline fixture"
```

---

### Task 10: Wire into service startup (foreground + worker)

**Files:**
- Modify: `main.go` (in `runForeground`, around line 118-140)
- Modify: `internal/winsvc/worker.go` (in `Execute`, around line 54-62)

**Interfaces:**
- Consumes: `config.EnsureMigrated(path) (Report, error)` (Task 8).

- [ ] **Step 1: Edit `main.go` `runForeground`**

After the scaffold-not-exist block (which `return`s) and **before** `cfg, err := config.Load(cfgPath)`, insert the migration call. Capture the report and log it after the logger is configured. Replace:

```go
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	configureStdoutLogger(cfg.Log.Level)
```

with:

```go
	migReport, migErr := config.EnsureMigrated(cfgPath)

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	configureStdoutLogger(cfg.Log.Level)

	if migErr != nil {
		slog.Warn("config migration failed; loaded existing file", "err", migErr)
	} else if migReport.Migrated {
		slog.Info("config migrated",
			"from", migReport.From, "to", migReport.To,
			"changes", len(migReport.Changes), "backup", migReport.BackupPath)
	}
```

Confirm `log/slog` is already imported in `main.go` (it is used elsewhere; add if missing).

- [ ] **Step 2: Build the foreground path**

Run: `go build ./...`
Expected: no errors.

- [ ] **Step 3: Edit `internal/winsvc/worker.go` `Execute`**

Before `cfg, err := config.Load(cfgPath)` (line ~55), insert:

```go
	if rep, mErr := config.EnsureMigrated(cfgPath); mErr != nil {
		slog.Warn("config migration failed; loading existing file", "err", mErr)
	} else if rep.Migrated {
		slog.Info("config migrated",
			"from", rep.From, "to", rep.To,
			"changes", len(rep.Changes), "backup", rep.BackupPath)
	}

	cfg, err := config.Load(cfgPath)
```

`log/slog` and `config` are already imported in this file.

- [ ] **Step 4: Build Windows target**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: no errors.

- [ ] **Step 5: Vet + full test**

Run: `go vet ./... && go test -count=1 ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add main.go internal/winsvc/worker.go
git commit -m "feat: run config migration at service startup"
```

---

### Task 11: Wire into panel startup

**Files:**
- Modify: `internal/panel/wails_app.go` (in `startup`, around line 74-77)

**Interfaces:**
- Consumes: `config.EnsureMigrated(path) (Report, error)`.

- [ ] **Step 1: Edit `startup`**

At the very top of `func (a *App) startup(ctx context.Context)`, before `cfg, _ := config.LoadPartial(...)`, insert the defensive call:

```go
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if rep, err := config.EnsureMigrated(paths.ConfigPath()); err != nil {
		slog.Warn("config migration failed; loading existing file", "err", err)
	} else if rep.Migrated {
		slog.Info("config migrated",
			"from", rep.From, "to", rep.To,
			"changes", len(rep.Changes), "backup", rep.BackupPath)
	}
	cfg, _ := config.LoadPartial(paths.ConfigPath())
```

`log/slog`, `config`, and `paths` are already imported in `wails_app.go`.

- [ ] **Step 2: Build Windows target (panel is windows-only)**

Run: `GOOS=windows GOARCH=amd64 go build ./...`
Expected: no errors.

- [ ] **Step 3: Full test (macOS)**

Run: `go test -count=1 ./...`
Expected: PASS (panel package is windows-only; the migration logic it calls is covered by the config package tests on macOS).

- [ ] **Step 4: Commit**

```bash
git add internal/panel/wails_app.go
git commit -m "feat: run config migration defensively at panel startup"
```

---

### Task 12: Document the rules (CLAUDE.md + configuration.md)

**Files:**
- Modify: `CLAUDE.md`
- Modify: `docs/configuration.md`

**Interfaces:** none (docs only).

- [ ] **Step 1: Add the CLAUDE.md section**

Insert a new section in `CLAUDE.md` after the "Tooling rule: Go programs, not shell" section (before "## Releases — what NOT to touch"):

```markdown
## Registering config changes

Any PR that adds, renames, or removes a field on `config.Config`
(`internal/config/config.go`), or changes a field's accepted values, **must
register a migration** so existing operators' `SerialHop_config.yaml` is upgraded
on their next update. The migration engine lives in `internal/config/migrate.go`;
the registry you edit is `internal/config/migrations.go`.

To register a change:

1. **Bump `config.CurrentSchemaVersion` by 1** (in `config.go`) and **append
   exactly one** `Migration{To: <new>, Desc, Ops}` to the `migrations` slice.
   The list is **append-only history — never edit or renumber an existing
   entry.**
2. **Pick ops from the fixed vocabulary** (no bespoke node code):
   - add a field → `Add(path, snippet)` — injects the key with its comment if absent.
   - rename a field → `Rename(from, to)` — carries the operator's value across.
   - remove a field → `Remove(path)` — **comments the key out; never silently deletes a value.**
   - change a value/enum → `MapValue(path, fn)`; anything structural → `MapNode(path, fn)`.
3. **For `Add`, reuse the exact comment text from the first-run scaffold** in
   `config.go` and update that scaffold (and the `schema_version:` line) in the
   same PR. The `TestScaffoldMatchesMigratedBaseline` drift guard fails if the
   scaffold and the migrations disagree on the key set.
4. **Add a before/after fixture** under `internal/config/testdata/migrations/`
   and a table entry/test in `migrate_test.go` covering your migration.
5. One PR = one schema bump. Do not batch unrelated config changes.

Migration runs automatically at service and panel startup (`EnsureMigrated`),
backing up the original to `SerialHop_config.v<old>.bak.yaml` before rewriting.
```

- [ ] **Step 2: Add operator-facing docs to `docs/configuration.md`**

Add a subsection under "## Editing the YAML directly" (after the paragraph about the scaffold), and a `schema_version` row note in the field reference intro:

```markdown
### `schema_version` and automatic migration

The file's first line is `schema_version:` — an integer SerialHop manages
automatically. **Do not edit it by hand.**

When you update SerialHop, the new version may add, rename, or retire settings.
On the next service or panel start, SerialHop migrates your config to match:

- **new settings** are added with their documented defaults and comments, so you
  can see and tune them;
- **renamed settings** keep your value under the new name;
- **retired settings** are commented out (not deleted), with a note saying which
  schema version removed them;
- **your file is backed up first** to `SerialHop_config.v<old>.bak.yaml` in the
  same folder.

Migration never deletes a value you set. If you ever need to see exactly what
changed, compare the file to its `.bak.yaml` backup, or check the Logs tab —
the migration is logged with the count of changes.
```

- [ ] **Step 3: Verify docs render / no broken structure**

Run: `gofmt -l . && go vet ./...`
Expected: no output / no errors (docs don't affect these, but confirms nothing else broke).

- [ ] **Step 4: Commit**

```bash
git add CLAUDE.md docs/configuration.md
git commit -m "docs: document config migration rules and behaviour"
```

---

### Task 13: Full pre-flight sweep

**Files:** none (verification only).

- [ ] **Step 1: Run the complete CI-equivalent gate**

```bash
gofmt -l .
go vet ./...
golangci-lint run
go test -race -count=1 ./...
GOOS=windows GOARCH=amd64 go build ./...
govulncheck ./...
```

Expected: `gofmt -l .` prints nothing; every other command exits 0. Fix anything that fails before opening the PR. If `golangci-lint`/`govulncheck` are not installed locally, note it and rely on CI, but run everything else.

- [ ] **Step 2: Confirm no gitignored artifacts staged**

Run: `git status --porcelain`
Expected: only the intended source/doc/test files; no `assets/manifest.xml`, `dist/`, `*.exe`, or `.syso`.

---

## Self-Review

**Spec coverage:**
- Versioning model (schema_version, baseline, CurrentSchemaVersion) → Tasks 1, 2, 8.
- Config struct field + scaffold line → Task 1.
- Registry + invariants → Task 2.
- Engine ops (Rename/Remove/Add/MapValue/MapNode) → Tasks 4-7.
- Comment-out mechanism → Task 5.
- EnsureMigrated (algorithm, backup, atomic, downgrade, unparseable, failure isolation, idempotency) → Task 8.
- Drift guard + baseline fixture → Task 9.
- Call sites (foreground, worker, panel) + logging → Tasks 10, 11.
- CLAUDE.md rules + operator docs → Task 12.
- Cross-platform (pure-Go engine, windows-only wiring) → Tasks 10, 11 build both targets; Task 13 builds windows.
- Testing strategy (all bullets) → Tasks 4-9, 13.

**Placeholder scan:** No TBD/TODO; every code step shows complete code; the one generated fixture (Task 9) has an exact generation command.

**Type consistency:** `Op.apply(top *yaml.Node, toVersion int)` signature consistent across Tasks 2/4/5/6/7/8. `Change{Kind,Path,Detail}` and `Report{From,To,Migrated,Changes,BackupPath}` consistent (Tasks 2, 8). `Rename/Remove/Add/MapValue/MapNode` names consistent between engine (Tasks 4-7), registry docs (Task 12), and CLAUDE.md. `EnsureMigrated`/`ensureMigrated`/`migrateTree` seam consistent (Task 8, used in Tasks 9-11).

**Note on Task 4→5 sequencing:** Task 4 ships a temporary "drop stale source" branch for the destination-occupied case; Task 5 replaces it with comment-out. Both leave the suite green — intentional to keep `commentOutChild` (Task 5) from being a forward dependency of Task 4.
