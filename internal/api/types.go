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
