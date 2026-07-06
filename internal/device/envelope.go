// Package device implements the core runtime for SerialHop's high-level JSON
// device protocol: the shared request/response envelope, job model, serial
// transaction discipline, persistent state store, and the per-device session
// actor hosting a device-type driver.
// See docs/superpowers/specs/2026-07-05-json-device-protocol-design.md and
// docs/protocol_translation_docs/ for the per-device contracts.
package device

import "encoding/json"

// Shared error codes (JSON_PROTOCOL.md §2) plus hub-level codes (spec §4).
const (
	CodeInvalidRequest    = "invalid_request"
	CodeUnknownCommand    = "unknown_command"
	CodeInvalidParams     = "invalid_params"
	CodeBusy              = "busy"
	CodeNotCalibrated     = "not_calibrated"
	CodeNotHomed          = "not_homed"
	CodeHardwareError     = "hardware_error"
	CodeInternalError     = "internal_error"
	CodeUnknownDevice     = "unknown_device"
	CodeDeviceUnreachable = "device_unreachable"
)

// Request is the command envelope every device protocol shares.
type Request struct {
	ID     string          `json:"id"`
	Cmd    string          `json:"cmd"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response is the reply envelope. Exactly one of Result/Error is set.
type Response struct {
	ID     string    `json:"id"`
	Status string    `json:"status"` // "ok" | "error"
	Result any       `json:"result,omitempty"`
	Error  *CmdError `json:"error,omitempty"`
}

// CmdError is a protocol-level error with a stable code.
type CmdError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *CmdError) Error() string { return e.Code + ": " + e.Message }

func OK(id string, result any) Response {
	return Response{ID: id, Status: "ok", Result: result}
}

func Err(id string, e *CmdError) Response {
	return Response{ID: id, Status: "error", Error: e}
}

func ErrInvalidParams(param string, value any, msg string) *CmdError {
	return &CmdError{Code: CodeInvalidParams, Message: msg,
		Details: map[string]any{"param": param, "value": value}}
}

func ErrUnknownCommand(cmd string) *CmdError {
	return &CmdError{Code: CodeUnknownCommand, Message: "unknown command: " + cmd}
}

func ErrBusy(msg string, details any) *CmdError {
	return &CmdError{Code: CodeBusy, Message: msg, Details: details}
}

func ErrHardware(msg string) *CmdError {
	return &CmdError{Code: CodeHardwareError, Message: msg}
}

func ErrInternal(msg string) *CmdError {
	return &CmdError{Code: CodeInternalError, Message: msg}
}

func ErrNotCalibrated(msg string) *CmdError {
	return &CmdError{Code: CodeNotCalibrated, Message: msg}
}
