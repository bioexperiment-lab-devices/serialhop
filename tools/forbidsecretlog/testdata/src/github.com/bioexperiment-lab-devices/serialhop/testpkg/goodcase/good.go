package goodcase

import (
	"log/slog"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

func good(cfg config.Config) {
	slog.Info("save", "user", cfg.LabBridge.User)
}
