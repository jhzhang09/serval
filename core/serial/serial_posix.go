//go:build !windows

package serial

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
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
		switch {
		case errors.Is(err, unix.EACCES), errors.Is(err, unix.EPERM):
			return nil, fmt.Errorf("%w: %s", ErrPermission, cfg.Name)
		case errors.Is(err, unix.ENOENT):
			return nil, fmt.Errorf("%w: %s", ErrNotFound, cfg.Name)
		case errors.Is(err, unix.EBUSY):
			return nil, fmt.Errorf("%w: %s", ErrBusy, cfg.Name)
		}
		return nil, fmt.Errorf("打开串口 %s 失败: %w", cfg.Name, err)
	}

	if err := configurePort(fd, cfg); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	file := os.NewFile(uintptr(fd), cfg.Name)
	return &Port{rwc: file}, nil
}

// ListPorts 列出当前系统上可用的常见串口设备路径。
func ListPorts() []string {
	patterns := []string{
		"/dev/ttyUSB*",
		"/dev/ttyACM*",
		"/dev/tty.usb*",
		"/dev/cu.usb*",
		"/dev/ttyS*",
	}
	seen := map[string]bool{}
	var out []string
	for _, pat := range patterns {
		matches, _ := filepath.Glob(pat)
		for _, m := range matches {
			if seen[m] {
				continue
			}
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}
