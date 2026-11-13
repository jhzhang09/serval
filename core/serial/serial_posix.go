//go:build !windows

package serial

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
)

// Open 打开并配置指定的串口设备。
func Open(cfg Config) (*Port, error) {
	if cfg.BaudRate <= 0 {
		cfg.BaudRate = 115200
	}
	if cfg.DataBits == 0 {
		cfg.DataBits = 8
	}
	if cfg.StopBits == 0 {
		cfg.StopBits = StopBits1
	}

	fd, err := unix.Open(cfg.Name, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("打开串口 %s 失败: %w", cfg.Name, err)
	}

	if err := configurePort(fd, cfg); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	file := os.NewFile(uintptr(fd), cfg.Name)
	return &Port{rwc: file}, nil
}
