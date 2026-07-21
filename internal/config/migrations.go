package config

// migrations is the ordered, APPEND-ONLY history of config schema changes.
// Never edit or renumber an existing entry — see CLAUDE.md ("Registering
// config changes"). Each config-changing PR appends exactly one Migration and
// bumps CurrentSchemaVersion (in config.go) by 1.
//
// Ships empty at baseline (CurrentSchemaVersion == 1): this delivers the
// migration infrastructure only. The first real migration lands as {To: 2, ...}.
var migrations = []Migration{
	{
		To:   2,
		Desc: "add remote_update section (default disabled)",
		Ops: []Op{
			Add("remote_update", `remote_update:
  enabled: false          # allow lab-bridge admins to push updates via
                          # POST /agent/update (admin-gated server-side, like
                          # /flash). the update installs with no operator
                          # action. off by default.`),
		},
	},
}
