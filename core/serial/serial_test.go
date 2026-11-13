package serial

import (
	"bytes"
	"io"
	"os"
	"testing"

	"github.com/creack/pty"
)

func TestPortReadWriteAndResize(t *testing.T) {
	buf := &bytes.Buffer{}
	port := NewPort(struct {
		io.Reader
		io.Writer
		io.Closer
	}{
		Reader: buf,
		Writer: buf,
		Closer: io.NopCloser(buf),
	})

	n, err := port.Write([]byte("hello serial"))
	if err != nil || n != 12 {
		t.Fatalf("Write failed: n=%d, err=%v", n, err)
	}

	readBuf := make([]byte, 12)
	nr, err := port.Read(readBuf)
	if err != nil || nr != 12 {
		t.Fatalf("Read failed: nr=%d, err=%v", nr, err)
	}
	if string(readBuf) != "hello serial" {
		t.Fatalf("expected 'hello serial', got %q", string(readBuf))
	}

	if err := port.Resize(100, 30); err != nil {
		t.Fatalf("Resize failed: %v", err)
	}

	if err := port.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestSerialOpenPTY(t *testing.T) {
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("无法打开 PTY 测试环境: %v", err)
	}
	defer master.Close()
	defer slave.Close()

	// 在 Unix 环境下打开从端 tty
	port, err := Open(Config{
		Name:     slave.Name(),
		BaudRate: 115200,
		DataBits: 8,
		Parity:   ParityNone,
		StopBits: StopBits1,
	})
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer port.Close()

	// 验证向串口写入
	msg := []byte("serial test message\n")
	if _, err := port.Write(msg); err != nil {
		t.Fatalf("port.Write failed: %v", err)
	}

	// 从 master 端读取
	recv := make([]byte, len(msg))
	if _, err := io.ReadFull(master, recv); err != nil {
		t.Fatalf("master read failed: %v", err)
	}
	if !bytes.Equal(recv, msg) {
		t.Fatalf("expected %q, got %q", msg, recv)
	}
}

func TestInvalidConfig(t *testing.T) {
	// 测试不支持的波特率
	f, err := os.CreateTemp("", "serial-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	_, err = Open(Config{
		Name:     f.Name(),
		BaudRate: 9999999, // 不支持的波特率
	})
	if err == nil {
		t.Fatal("expected error for invalid baudrate, got nil")
	}
}
