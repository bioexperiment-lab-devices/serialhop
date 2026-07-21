package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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

// --- helpers ---

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

func writeTemp(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return p
}

// synthetic 2-step registry for exercising the write path (the real registry
// is empty at ship, so it cannot drive the migration write path in tests).
func testMigs() []Migration {
	return []Migration{
		{To: 2, Desc: "rename rest.port", Ops: []Op{Rename("rest.port", "rest.listen_port")}},
		{To: 3, Desc: "remove legacy", Ops: []Op{Remove("discovery.legacy")}},
	}
}

// --- path/node helpers ---

func TestResolvePath(t *testing.T) {
	top := parseDoc(t, "rest:\n  port: 8080\nlog:\n  level: info\n")

	parent, leaf, val, ok := resolvePath(top, "rest.port")
	if !ok || val == nil || leaf != "port" {
		t.Fatalf("resolvePath rest.port: ok=%v leaf=%q val=%v", ok, leaf, val)
	}
	if val.Value != "8080" || parent == nil {
		t.Fatalf("rest.port value = %q", val.Value)
	}

	// present parent, absent leaf -> ok=true, val=nil
	_, _, v, ok2 := resolvePath(top, "rest.missing")
	if !ok2 || v != nil {
		t.Fatalf("absent leaf: ok=%v v=%v", ok2, v)
	}

	// missing intermediate parent -> ok=false
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

// --- Rename ---

func TestRenameMovesValueAndComment(t *testing.T) {
	src := "rest:\n  port: 8080 # the port\n"
	out, changes := applyOps(t, src, 2, Rename("rest.port", "rest.listen_port"))
	if !strings.Contains(out, "listen_port: 8080") {
		t.Fatalf("value not carried to new key:\n%s", out)
	}
	if strings.Contains(out, "\n  port: 8080") {
		t.Fatalf("old key still active:\n%s", out)
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

// --- Remove ---

func TestRemoveCommentsOutAndPreservesValue(t *testing.T) {
	src := "discovery:\n  legacy_scan: true\n  post_open_settle_ms: 2000\n"
	out, changes := applyOps(t, src, 3, Remove("discovery.legacy_scan"))
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

// --- Add ---

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
	top := parseDoc(t, "rest:\n  port: 8080\n")
	_, err := Add("raw_serial", "wrong_key: 1\n").apply(top, 4)
	if err == nil {
		t.Fatalf("expected error on key/path mismatch")
	}
}

// --- MapValue / MapNode ---

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

// --- EnsureMigrated ---

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
	body := "key: value\n\tbadtab: 1\n  - mixed\n"
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

// --- drift guard ---

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

	var s struct {
		SchemaVersion int `yaml:"schema_version"`
	}
	if err := yaml.Unmarshal(scaffold, &s); err != nil {
		t.Fatalf("parse scaffold: %v", err)
	}
	if s.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("scaffold schema_version = %d, want %d", s.SchemaVersion, CurrentSchemaVersion)
	}
}
