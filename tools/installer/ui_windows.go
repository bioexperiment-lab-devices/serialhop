//go:build windows

package main

import (
	"fmt"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// runDialog shows the install dialog and returns the exit code. Drives
// Runner.Run on a goroutine and marshals status updates onto the GUI thread
// via mw.Synchronize.
func runDialog(opts *options) int {
	var (
		mw         *walk.MainWindow
		pathEdit   *walk.LineEdit
		statusLine *walk.Label
		installBtn *walk.PushButton
	)

	exitCode := 0
	produceResult := func(r Result) {
		mw.Synchronize(func() {
			if r.Err != nil {
				statusLine.SetText("Error: " + r.Err.Error())
				exitCode = r.ExitCode
				installBtn.SetEnabled(true)
				return
			}
			msg := r.Message
			if msg == "" {
				msg = fmt.Sprintf("Installed SerialHop v%s.", r.BundledVer)
			}
			statusLine.SetText(msg)
			exitCode = r.ExitCode
			// On success, leave the window open with the status visible. The
			// operator closes it manually. (The panel has already been
			// launched in a detached child by maybeLaunch.)
			installBtn.SetEnabled(true)
		})
	}

	err := MainWindow{
		AssignTo: &mw,
		Title:    "SerialHop Installer",
		MinSize:  Size{Width: 520, Height: 200},
		Layout:   VBox{},
		Children: []Widget{
			Label{Text: "Install location:"},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					LineEdit{
						AssignTo: &pathEdit,
						Text:     opts.InstallDir,
					},
					PushButton{
						Text: "Browse…",
						OnClicked: func() {
							dlg := walk.FileDialog{
								Title:    "Choose install directory",
								FilePath: pathEdit.Text(),
							}
							ok, err := dlg.ShowBrowseFolder(mw)
							if err != nil || !ok {
								return
							}
							pathEdit.SetText(dlg.FilePath)
						},
					},
				},
			},
			Label{AssignTo: &statusLine, Text: "Ready."},
			Composite{
				Layout: HBox{},
				Children: []Widget{
					PushButton{
						AssignTo: &installBtn,
						Text:     "Install",
						OnClicked: func() {
							installBtn.SetEnabled(false)
							statusLine.SetText("Installing…")
							runOpts := *opts
							runOpts.InstallDir = pathEdit.Text()
							go func() {
								r := newProductionRunner()
								produceResult(r.Run(runOpts))
							}()
						},
					},
					PushButton{
						Text:      "Cancel",
						OnClicked: func() { mw.Close() },
					},
				},
			},
		},
	}.Create()
	if err != nil {
		fmt.Println("create dialog:", err)
		return 1
	}
	mw.Run()
	return exitCode
}
