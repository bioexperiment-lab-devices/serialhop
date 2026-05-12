package avr

import "testing"

func TestUnoConstants(t *testing.T) {
	if FlashSize != 32*1024 {
		t.Errorf("FlashSize: got %d, want %d", FlashSize, 32*1024)
	}
	if BootloaderSize != 512 {
		t.Errorf("BootloaderSize: got %d, want 512", BootloaderSize)
	}
	if UserSpace := FlashSize - BootloaderSize; UserSpace != 32256 {
		t.Errorf("user space: got %d, want 32256", UserSpace)
	}
	if PageSize != 128 {
		t.Errorf("PageSize: got %d, want 128", PageSize)
	}
	if BootloaderBaud != 115200 {
		t.Errorf("BootloaderBaud: got %d, want 115200", BootloaderBaud)
	}
	if TargetBaud != 9600 {
		t.Errorf("TargetBaud: got %d, want 9600", TargetBaud)
	}
}
