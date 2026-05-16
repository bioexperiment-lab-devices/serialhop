package badcase

import (
	"log/slog"

	"github.com/bioexperiment-lab-devices/serialhop/internal/config"
)

func bad(cfg config.Config) {
	slog.Info("save",
		"user", cfg.LabBridge.User,
		"pass", cfg.LabBridge.Pass, // want "logged secret"
	)
}
