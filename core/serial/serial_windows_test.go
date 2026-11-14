//go:build windows

package serial

import (
	"errors"
	"slices"
	"testing"
	"unsafe"
)

func TestNormalizeWinPortName(t *testing.T) {
	cases := map[string]string{
		"COM3":       `\\.\COM3`,
		"com12":      `\\.\com12`,
		`\\.\COM10`:  `\\.\COM10`,
		`\\?\COM4`:   `\\?\COM4`,
		" /tmp/dev ": "/tmp/dev",
		"":           "",
	}
	for in, want := range cases {
		if got := normalizeWinPortName(in); got != want {
			t.Fatalf("normalizeWinPortName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWinDCBLayoutAndConfig(t *testing.T) {
	if size := unsafe.Sizeof(winDCB{}); size != 28 {
		t.Fatalf("winDCB size = %d, want 28", size)
	}
	if size := unsafe.Sizeof(winCommTimeouts{}); size != 20 {
		t.Fatalf("winCommTimeouts size = %d, want 20", size)
	}

	var dcb winDCB
	cfg := Config{Name: "COM3", BaudRate: 115200, DataBits: 8, Parity: ParityEven, StopBits: StopBits2}
	if err := applyWinDCB(&dcb, cfg); err != nil {
		t.Fatalf("applyWinDCB: %v", err)
	}
	if dcb.BaudRate != 115200 || dcb.ByteSize != 8 || dcb.Parity != winEvenParity || dcb.StopBits != winTwoStopBits {
		t.Fatalf("unexpected DCB: %+v", dcb)
	}
	if dcb.Flags&(winBinaryFlag|winParityFlag) != (winBinaryFlag | winParityFlag) {
		t.Fatalf("expected binary+parity flags set, got %#x", dcb.Flags)
	}

	if err := applyWinDCB(&dcb, Config{BaudRate: 0, DataBits: 8, StopBits: StopBits1}); !errors.Is(err, ErrUnsupportedBaudRate) {
		t.Fatalf("expected ErrUnsupportedBaudRate, got %v", err)
	}
	if err := applyWinDCB(&dcb, Config{BaudRate: 9600, DataBits: 9, StopBits: StopBits1}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("expected ErrInvalidConfig, got %v", err)
	}
}

func TestSortCOMPorts(t *testing.T) {
	got := []string{"COM10", "COM2", "COM1", "COM20", "COM3"}
	sortCOMPorts(got)
	want := []string{"COM1", "COM2", "COM3", "COM10", "COM20"}
	if !slices.Equal(got, want) {
		t.Fatalf("sortCOMPorts = %v, want %v", got, want)
	}
}

func TestOpenMissingPortReturnsErrNotFound(t *testing.T) {
	_, err := Open(DefaultConfig("COM254"))
	if err == nil || !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound for nonexistent port, got %v", err)
	}
}
