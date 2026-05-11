package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

// FirstRunAction is the decision returned by decideFirstRun.
type FirstRunAction int

const (
	FirstRunOpenPanel FirstRunAction = iota
	FirstRunShowDialog
)

// FirstRunState describes everything decideFirstRun needs about the
// on-disk config to choose an action.
type FirstRunState struct {
	Exists   bool          // config file exists
	ParseErr error         // non-nil iff YAML parse failed
	Cfg      config.Config // populated from Default() and overlaid with whatever parsed cleanly
}

// readFirstRunState inspects path and returns a FirstRunState describing
// the file's existence and parsed contents.
func readFirstRunState(path string) FirstRunState {
	s := FirstRunState{Cfg: config.Default()}
	data, err := os.ReadFile(path) //nolint:gosec // path is paths.ConfigPath()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s
		}
		s.Exists = true
		s.ParseErr = err
		return s
	}
	s.Exists = true
	if uerr := yaml.Unmarshal(data, &s.Cfg); uerr != nil {
		s.ParseErr = uerr
		s.Cfg = config.Default()
	}
	return s
}

// decideFirstRun returns ShowDialog when the file is missing or both
// credentials are absent; otherwise OpenPanel. Malformed YAML opens the
// panel (the existing validation-warning label surfaces the parse error
// — we don't silently overwrite a file we cannot understand).
func decideFirstRun(s FirstRunState) FirstRunAction {
	if !s.Exists {
		return FirstRunShowDialog
	}
	if s.ParseErr != nil {
		return FirstRunOpenPanel
	}
	if s.Cfg.LabBridge.User == "" || s.Cfg.LabBridge.Pass == "" {
		return FirstRunShowDialog
	}
	return FirstRunOpenPanel
}

// CredsCheckKind enumerates how the dialog should react to verifyCredentials.
type CredsCheckKind int

const (
	CredsOK           CredsCheckKind = iota // 200 — save.
	CredsUnauthorized                       // 401 — inline error, stay in dialog.
	CredsNeedsConfirm                       // 5xx or network — prompt the user to "save anyway?".
)

// CredsCheckResult is the verdict of verifyCredentials.
type CredsCheckResult struct {
	Kind   CredsCheckKind
	Detail string // human-readable reason for Confirm/Unauthorized; empty on OK.
}

// verifyCredentials makes one /api/public/clients/{user} call and
// classifies the outcome. base must be the scheme+host (e.g. "https://x").
func verifyCredentials(ctx context.Context, hc *http.Client, base, user, pass, userAgent string) CredsCheckResult {
	_, err := labbridge.FetchClient(ctx, hc, base, user, pass, userAgent)
	switch {
	case err == nil:
		return CredsCheckResult{Kind: CredsOK}
	case errors.Is(err, labbridge.ErrUnauthorized):
		return CredsCheckResult{Kind: CredsUnauthorized, Detail: "server rejected credentials"}
	default:
		return CredsCheckResult{Kind: CredsNeedsConfirm, Detail: err.Error()}
	}
}

// patchCredentials replaces (or appends) lab_bridge.user and
// lab_bridge.pass inside yamlBytes, preserving comments and unrelated
// fields. The two values are written as double-quoted scalars to match
// the scaffold style.
func patchCredentials(yamlBytes []byte, user, pass string) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(yamlBytes, &root); err != nil {
		return nil, fmt.Errorf("patchCredentials: parse: %w", err)
	}
	if root.Kind == 0 {
		// Empty input: build a fresh mapping from scratch.
		root = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return nil, fmt.Errorf("patchCredentials: unexpected YAML shape (kind=%d)", root.Kind)
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("patchCredentials: top-level YAML must be a mapping (kind=%d)", doc.Kind)
	}

	labBridge := findMappingChild(doc, "lab_bridge")
	if labBridge == nil {
		// Append a new lab_bridge block.
		doc.Content = append(doc.Content,
			scalarKey("lab_bridge"),
			newLabBridgeMapping(user, pass),
		)
	} else {
		setOrAppendScalar(labBridge, "user", user)
		setOrAppendScalar(labBridge, "pass", pass)
	}

	out, err := yaml.Marshal(&root)
	if err != nil {
		return nil, fmt.Errorf("patchCredentials: marshal: %w", err)
	}
	return out, nil
}

// findMappingChild returns the value Node for key inside a mapping
// Node, or nil if not present. Caller must ensure parent.Kind == MappingNode.
func findMappingChild(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		k, v := parent.Content[i], parent.Content[i+1]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			return v
		}
	}
	return nil
}

// setOrAppendScalar sets parent[key] to a double-quoted scalar value.
// Appends a new key+value pair if key is absent.
func setOrAppendScalar(parent *yaml.Node, key, value string) {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		k := parent.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == key {
			parent.Content[i+1] = scalarString(value)
			return
		}
	}
	parent.Content = append(parent.Content, scalarKey(key), scalarString(value))
}

func scalarKey(name string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Value: name}
}

func scalarString(v string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle, Value: v}
}

func newLabBridgeMapping(user, pass string) *yaml.Node {
	return &yaml.Node{
		Kind: yaml.MappingNode,
		Content: []*yaml.Node{
			scalarKey("user"), scalarString(user),
			scalarKey("pass"), scalarString(pass),
		},
	}
}

// writeOrPatchCreds writes the credentials to path. If path does not
// exist, the full scaffold is rendered with user and pass substituted.
// If path exists, only lab_bridge.user/pass are updated; everything
// else (including comments) is preserved. The file is written
// atomically (tmp + rename) at 0600.
func writeOrPatchCreds(path, user, pass string) error {
	existing, err := os.ReadFile(path) //nolint:gosec // path is paths.ConfigPath()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("writeOrPatchCreds: read: %w", err)
	}
	var out []byte
	if errors.Is(err, os.ErrNotExist) {
		out = renderScaffoldWithCreds(user, pass)
	} else {
		out, err = patchCredentials(existing, user, pass)
		if err != nil {
			return err
		}
	}
	return atomicWriteFile(path, out, 0o600)
}

// renderScaffoldWithCreds returns the bytes of the scaffold template
// with user and pass substituted.
func renderScaffoldWithCreds(user, pass string) []byte {
	var buf strings.Builder
	if err := config.WriteScaffold(&buf); err != nil {
		// WriteScaffold can only fail if its io.Writer fails; strings.Builder doesn't.
		panic(fmt.Sprintf("WriteScaffold to strings.Builder failed: %v", err))
	}
	s := buf.String()
	s = strings.Replace(s, `user: ""`, fmt.Sprintf(`user: %q`, user), 1)
	s = strings.Replace(s, `pass: ""`, fmt.Sprintf(`pass: %q`, pass), 1)
	return []byte(s)
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-cfg-*")
	if err != nil {
		return fmt.Errorf("atomicWriteFile: create temp: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWriteFile: write: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWriteFile: close: %w", err)
	}
	if err := os.Chmod(tmpPath, perm); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWriteFile: chmod: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("atomicWriteFile: rename: %w", err)
	}
	return nil
}
