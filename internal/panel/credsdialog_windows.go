//go:build windows

package panel

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
	"github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

// runCredsDialog shows the first-run credentials dialog modal to no
// parent. Returns true if the user submitted credentials and the
// config was written; false on cancel or fatal error.
func runCredsDialog(cfgPath string, cfg config.Config) bool {
	var (
		dlg       *walk.Dialog
		userEdit  *walk.LineEdit
		passEdit  *walk.LineEdit
		statusLbl *walk.Label
		saveBtn   *walk.PushButton
		cancelBtn *walk.PushButton
	)
	saved := false
	hc := &http.Client{Timeout: 10 * time.Second}
	userAgent := "SerialHop/" + version.Base() + " (firstrun)"
	base := "https://" + cfg.LabBridge.Host

	showStatus := func(msg string) {
		_ = statusLbl.SetText(msg)
		statusLbl.SetVisible(msg != "")
	}

	doSave := func(user, pass string) {
		if err := writeOrPatchCreds(cfgPath, user, pass); err != nil {
			walk.MsgBox(dlg, "Error", "Couldn't save config: "+err.Error(), walk.MsgBoxIconError)
			return
		}
		saved = true
		dlg.Accept()
	}

	onSubmit := func() {
		user := strings.TrimSpace(userEdit.Text())
		pass := strings.TrimSpace(passEdit.Text())
		if user == "" || pass == "" {
			showStatus("Username and password are required.")
			return
		}
		saveBtn.SetEnabled(false)
		cancelBtn.SetEnabled(false)
		showStatus("Verifying…")

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := verifyCredentials(ctx, hc, base, user, pass, userAgent)

			dlg.Synchronize(func() {
				saveBtn.SetEnabled(true)
				cancelBtn.SetEnabled(true)
				switch result.Kind {
				case CredsOK:
					showStatus("")
					doSave(user, pass)
				case CredsUnauthorized:
					showStatus("Server rejected these credentials. Check the username and password.")
				case CredsNeedsConfirm:
					msg := fmt.Sprintf("Couldn't reach %s to verify the credentials (%s). Save anyway?",
						cfg.LabBridge.Host, result.Detail)
					answer := walk.MsgBox(dlg, "Can't reach server", msg,
						walk.MsgBoxYesNo|walk.MsgBoxIconWarning|walk.MsgBoxDefButton2)
					if answer == walk.DlgCmdYes {
						doSave(user, pass)
					} else {
						showStatus("")
					}
				}
			})
		}()
	}

	_, err := Dialog{
		AssignTo:      &dlg,
		Title:         "SerialHop — Set credentials",
		MinSize:       Size{Width: 380, Height: 220},
		Layout:        VBox{},
		DefaultButton: &saveBtn,
		CancelButton:  &cancelBtn,
		Children: []Widget{
			Label{Text: "Lab-bridge server is configured to reach " + cfg.LabBridge.Host + ".\nEnter your credentials:"},
			Composite{
				Layout: Grid{Columns: 2},
				Children: []Widget{
					Label{Text: "Username:"},
					LineEdit{AssignTo: &userEdit},
					Label{Text: "Password:"},
					LineEdit{AssignTo: &passEdit}, // plain text per user requirement
				},
			},
			Label{
				AssignTo:  &statusLbl,
				TextColor: walk.RGB(192, 0, 0),
				Visible:   false,
			},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					HSpacer{},
					PushButton{AssignTo: &cancelBtn, Text: "Cancel", OnClicked: func() { dlg.Cancel() }},
					PushButton{AssignTo: &saveBtn, Text: "Save", OnClicked: onSubmit},
				},
			},
		},
	}.Run(nil)

	if err != nil {
		writePanelDebugLog("creds_dialog_error", err)
		return false
	}
	return saved
}
