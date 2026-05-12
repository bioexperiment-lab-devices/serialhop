package api

import "time"

type DeviceDTO struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	TypeCode byte   `json:"type_code"`
	Port     string `json:"port"`
}

type DevicesResponse struct {
	Devices      []DeviceDTO `json:"devices"`
	DiscoveredAt *time.Time  `json:"discovered_at"`
}

type CommandRequest struct {
	Command []int `json:"command"`
}

type CommandResponse struct {
	Response []int `json:"response"`
}

type ErrorBody struct {
	Error  string `json:"error"`
	Detail string `json:"detail,omitempty"`
}

type PortDTO struct {
	Name       string `json:"name"`
	Discovered bool   `json:"discovered"`
	DeviceID   string `json:"device_id,omitempty"`
}

type PortsResponse struct {
	Ports []PortDTO `json:"ports"`
}

type DetailedPortDTO struct {
	Name         string `json:"name"`
	IsUSB        bool   `json:"is_usb"`
	VID          string `json:"vid"`
	PID          string `json:"pid"`
	SerialNumber string `json:"serial_number"`
	Product      string `json:"product"`
	Discovered   bool   `json:"discovered"`
	DeviceID     string `json:"device_id,omitempty"`
}

type DetailedPortsResponse struct {
	Ports []DetailedPortDTO `json:"ports"`
}

type DisconnectResponse struct {
	Released int `json:"released"`
}

type FlashRequest struct {
	Firmware         string `json:"firmware"`
	TestCommand      string `json:"test_command,omitempty"`
	ExpectedResponse string `json:"expected_response,omitempty"`
	TimeoutMs        *int   `json:"timeout_ms,omitempty"`
	InterByteMs      *int   `json:"inter_byte_ms,omitempty"`
	PostOpenSettleMs *int   `json:"post_open_settle_ms,omitempty"`
}

type StageDTO struct {
	Status              string `json:"status"`
	DurationMs          int64  `json:"duration_ms,omitempty"`
	Error               string `json:"error,omitempty"`
	FirstMismatchOffset string `json:"first_mismatch_offset,omitempty"`
	VerifyStatus        string `json:"verify_status,omitempty"`
}

type BackupDTO struct {
	Hex       string `json:"hex"`
	SavedPath string `json:"saved_path"`
	SHA256    string `json:"sha256"`
	SizeBytes int    `json:"size_bytes"`
	Scope     string `json:"scope"`
}

type TestResultDTO struct {
	Sent     string `json:"sent"`
	Expected string `json:"expected"`
	Received string `json:"received"`
	Match    bool   `json:"match"`
}

type FlashResponse struct {
	Outcome      string              `json:"outcome"`
	Port         string              `json:"port"`
	Stages       map[string]StageDTO `json:"stages"`
	Backup       BackupDTO           `json:"backup"`
	TestResult   *TestResultDTO      `json:"test_result,omitempty"`
	RecoveryHint string              `json:"recovery_hint,omitempty"`
}
