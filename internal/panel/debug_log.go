package panel

import (
	"fmt"
	"os"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/paths"
)

// writePanelDebugLog appends a single line to SerialHop_panel_error.log
// inside %ProgramData%\SerialHop\logs\. Used for failures the operator
// might want to inspect post-mortem without surfacing a popup.
// Best-effort: if the target path is unreachable (paths.LogsDir() == ""),
// the entry is silently dropped.
func writePanelDebugLog(code string, err error) {
	target := paths.PanelErrorLogPath()
	if target == "" {
		return
	}
	line := fmt.Sprintf("%s %s: %v\n", time.Now().Format(time.RFC3339), code, err)
	f, ferr := os.OpenFile(target, //nolint:gosec // target is paths.PanelErrorLogPath(), not user-controlled
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if ferr != nil {
		return
	}
	defer f.Close() //nolint:errcheck
	_, _ = f.WriteString(line)
}
