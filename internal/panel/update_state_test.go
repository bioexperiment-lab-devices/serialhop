package panel

import "testing"

func TestUpdateState_TransitionsHappyPath(t *testing.T) {
	st := UpdateIdle
	if got := nextUpdateState(st, EvUpdateAvailable); got != UpdateAvailable {
		t.Errorf("idle + available: got %v", got)
	}
	st = UpdateAvailable
	if got := nextUpdateState(st, EvDownloadStart); got != UpdateDownloading {
		t.Errorf("available + downloadStart: got %v", got)
	}
	st = UpdateDownloading
	if got := nextUpdateState(st, EvDownloadOK); got != UpdateReady {
		t.Errorf("downloading + ok: got %v", got)
	}
	st = UpdateReady
	if got := nextUpdateState(st, EvInstallStart); got != UpdateInstalling {
		t.Errorf("ready + installStart: got %v", got)
	}
	st = UpdateInstalling
	if got := nextUpdateState(st, EvInstallOK); got != UpdateInstalled {
		t.Errorf("installing + ok: got %v", got)
	}
}

func TestUpdateState_DownloadFailGoesBackToAvailable(t *testing.T) {
	if got := nextUpdateState(UpdateDownloading, EvDownloadFail); got != UpdateDownloadFailed {
		t.Errorf("got %v", got)
	}
	if got := nextUpdateState(UpdateDownloadFailed, EvRetry); got != UpdateAvailable {
		t.Errorf("retry → available: got %v", got)
	}
}

func TestUpdateState_InstallFailGoesToRolledBack(t *testing.T) {
	if got := nextUpdateState(UpdateInstalling, EvInstallFail); got != UpdateInstallFailed {
		t.Errorf("got %v", got)
	}
	if got := nextUpdateState(UpdateInstallFailed, EvRetry); got != UpdateReady {
		t.Errorf("retry on rollback → ready: got %v", got)
	}
}

func TestUpdateState_NoChangeOnInvalidEvent(t *testing.T) {
	// Downloading + ev_install_start (impossible) — state stays put.
	if got := nextUpdateState(UpdateDownloading, EvInstallStart); got != UpdateDownloading {
		t.Errorf("unexpected transition: %v", got)
	}
}

func TestUpdateState_HideTransitions(t *testing.T) {
	// Available row can be dismissed before download.
	if got := nextUpdateState(UpdateAvailable, EvHide); got != UpdateIdle {
		t.Errorf("available + hide: got %v, want UpdateIdle", got)
	}
	// Installed row disappears on panel close.
	if got := nextUpdateState(UpdateInstalled, EvHide); got != UpdateIdle {
		t.Errorf("installed + hide: got %v, want UpdateIdle", got)
	}
}

func TestUpdateState_CancelTransitions(t *testing.T) {
	// Download cancel → back to Available.
	if got := nextUpdateState(UpdateDownloading, EvCancel); got != UpdateAvailable {
		t.Errorf("downloading + cancel: got %v, want UpdateAvailable", got)
	}
	// UAC cancel during install → back to Ready (nothing was restored).
	if got := nextUpdateState(UpdateInstalling, EvCancel); got != UpdateReady {
		t.Errorf("installing + cancel: got %v, want UpdateReady", got)
	}
	// Cancel is a no-op in other states.
	if got := nextUpdateState(UpdateAvailable, EvCancel); got != UpdateAvailable {
		t.Errorf("available + cancel should be no-op: got %v", got)
	}
}
