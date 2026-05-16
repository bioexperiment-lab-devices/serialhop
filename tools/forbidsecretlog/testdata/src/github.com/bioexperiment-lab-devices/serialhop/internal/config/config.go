// Package config is a minimal stub for the forbidsecretlog analyser tests.
// Only the fields referenced by the test fixtures are defined here.
package config

// LabBridgeConfig holds the lab-bridge connection credentials.
type LabBridgeConfig struct {
	Host string
	User string
	Pass string
}

// Config is the top-level application configuration.
type Config struct {
	LabBridge LabBridgeConfig
}
