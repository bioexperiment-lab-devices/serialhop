package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

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

// Report describes the outcome of an EnsureMigrated call.
type Report struct {
	From       int      `json:"from"`
	To         int      `json:"to"`
	Migrated   bool     `json:"migrated"`
	Changes    []Change `json:"changes"`
	BackupPath string   `json:"backup_path"`
}

// --- node/path helpers ---

// docMapping returns the top-level mapping node of a parsed document, or nil
// if the document is empty or its root is not a mapping.
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

// childIndex returns the index i in parent.Content such that parent.Content[i]
// is the key node matching key (its value node is at i+1). Returns -1 if absent.
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

// childValue returns the value node for key in the mapping parent, or nil.
func childValue(parent *yaml.Node, key string) *yaml.Node {
	if i := childIndex(parent, key); i >= 0 {
		return parent.Content[i+1]
	}
	return nil
}

// resolvePath walks a dotted path from top through existing mappings. Returns
// the parent mapping, the leaf key, and the leaf value node (nil if the leaf
// itself is absent). ok is false only if an intermediate segment is missing or
// is not a mapping.
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

// ensureParent walks a dotted path, creating any missing intermediate mappings,
// and returns the leaf's parent mapping and the leaf key.
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

// commentOutChild removes key from the mapping parent and preserves the removed
// "key: value" block as a comment attached to a neighbouring node, prefixed
// with marker. Returns false if key was absent.
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

// --- ops ---

// Rename moves the value node at from to to, carrying its comments and creating
// any missing parent maps under to. If to is already populated, the source is
// commented out as superseded.
func Rename(from, to string) Op { return renameOp{from: from, to: to} }

type renameOp struct{ from, to string }

func (o renameOp) apply(top *yaml.Node, _ int) ([]Change, error) {
	srcParent, srcKey, srcVal, ok := resolvePath(top, o.from)
	if !ok || srcVal == nil {
		return nil, nil // source absent -> no-op
	}
	dstParent, dstKey := ensureParent(top, o.to)
	if childValue(dstParent, dstKey) != nil {
		commentOutChild(srcParent, srcKey, "superseded by "+o.to+":")
		return []Change{{Kind: "rename", Path: o.from,
			Detail: fmt.Sprintf("%s already set; %s commented out", o.to, o.from)}}, nil
	}
	i := childIndex(srcParent, srcKey)
	keyNode := srcParent.Content[i]
	valNode := srcParent.Content[i+1]
	srcParent.Content = append(srcParent.Content[:i], srcParent.Content[i+2:]...)
	keyNode.Value = dstKey // carries the key node's head/line comments
	dstParent.Content = append(dstParent.Content, keyNode, valNode)
	return []Change{{Kind: "rename", Path: o.from, Detail: o.from + " -> " + o.to}}, nil
}

// Remove comments out the key at path (never deletes the operator's value),
// with a "removed in schema vN" marker.
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

// Add inserts snippet at path if the key is absent. snippet must be a
// self-contained "key: value" YAML block (optionally with leading comments)
// whose top key equals the final segment of path. No-op if the key exists.
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

// MapValue rewrites the scalar at path via fn (enum rename, unit change),
// preserving the node's style. No-op if absent, non-scalar, or unchanged.
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

// MapNode is the escape hatch for structural transforms the typed ops can't
// express. fn receives the value node at path and mutates it in place.
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

// --- orchestration ---

// EnsureMigrated migrates the config file at path up to CurrentSchemaVersion,
// rewriting it in place if needed. Idempotent; safe under concurrent callers
// (deterministic transform + atomic write). Never writes a partial file: on any
// migration error it returns the error and leaves the file untouched.
func EnsureMigrated(path string) (Report, error) {
	return ensureMigrated(path, migrations, CurrentSchemaVersion)
}

func ensureMigrated(path string, migs []Migration, current int) (Report, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.ConfigPath(), not user-controlled
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

// readSchemaVersion reads the top-level schema_version scalar, defaulting to 1
// (baseline) when absent or unparseable.
func readSchemaVersion(top *yaml.Node) int {
	if v := childValue(top, "schema_version"); v != nil && v.Kind == yaml.ScalarNode {
		if n, err := strconv.Atoi(strings.TrimSpace(v.Value)); err == nil {
			return n
		}
	}
	return 1
}

// stampSchemaVersion sets (or inserts, at the front) the top-level
// schema_version key to ver.
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
