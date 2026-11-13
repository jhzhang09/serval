// Package serial 实现跨平台串口通信（Serial Port / RS-232 / UART）。
// 支持常见波特率（9600、19200、38400、57600、115200 等）、数据位、停止位和奇偶校验。
// 不依赖 UI。
package serial

import (
	"errors"
	"io"
)

var (
	// ErrUnsupportedBaudRate 表示指定的波特率不受底层系统支持。
	ErrUnsupportedBaudRate = errors.New("不支持的波特率")
	// ErrInvalidConfig 表示串口参数无效。
	ErrInvalidConfig = errors.New("无效的串口配置")
)

// Parity 奇偶校验类型
type Parity string

const (
	ParityNone  Parity = "N" // 无校验
	ParityOdd   Parity = "O" // 奇校验
	ParityEven  Parity = "E" // 偶校验
	ParityMark  Parity = "M" // 标记校验
	ParitySpace Parity = "S" // 空格校验
)

// StopBits 停止位数
type StopBits int

const (
	StopBits1   StopBits = 1
	StopBits1_5 StopBits = 15 // 1.5 停止位
	StopBits2   StopBits = 2
)

// Config 串口配置参数
type Config struct {
	Name     string   // 串口设备路径，例如 /dev/ttyUSB0 或 COM3
	BaudRate int      // 波特率，默认 115200
	DataBits int      // 数据位：5, 6, 7, 8，默认 8
	Parity   Parity   // 奇偶校验，默认 ParityNone
	StopBits StopBits // 停止位，默认 StopBits1
}

// DefaultConfig 返回常用默认串口配置。
func DefaultConfig(name string) Config {
	return Config{
		Name:     name,
		BaudRate: 115200,
		DataBits: 8,
		Parity:   ParityNone,
		StopBits: StopBits1,
	}
}

// Port 表示已打开的串口连接，实现 io.ReadWriteCloser 和 Resize 方法以接入终端。
type Port struct {
	rwc io.ReadWriteCloser
}

// NewPort 用现有的 ReadWriteCloser 创建 Port（方便测试与自定义流）。
func NewPort(rwc io.ReadWriteCloser) *Port {
	return &Port{rwc: rwc}
}

func (p *Port) Read(b []byte) (int, error) {
	return p.rwc.Read(b)
}

func (p *Port) Write(b []byte) (int, error) {
	return p.rwc.Write(b)
}

func (p *Port) Close() error {
	return p.rwc.Close()
}

// Resize 实现 shellConn 接口。串口为物理物理字符流，无需窗口重置协议。
func (p *Port) Resize(cols, rows int) error {
	return nil
}
