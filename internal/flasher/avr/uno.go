// Package avr holds per-chip constants for the AVR family of targets.
// Only the Arduino Uno R3 (ATmega328P with optiboot) is supported today;
// add new files (e.g., mega2560.go) when widening coverage.
package avr

const (
	// FlashSize is the total program-flash capacity in bytes.
	FlashSize = 32 * 1024

	// BootloaderSize is the optiboot region at the top of flash.
	// The user sketch is constrained to FlashSize - BootloaderSize bytes.
	BootloaderSize = 512

	// PageSize is the program-flash page size in bytes. STK500v1 writes and
	// reads happen in page-sized chunks.
	PageSize = 128

	// BootloaderBaud is the line rate the optiboot bootloader speaks.
	BootloaderBaud = 115200

	// TargetBaud is the line rate the user sketch is built against —
	// matches discovery / /command across this codebase.
	TargetBaud = 9600
)
