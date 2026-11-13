//go:build windows

package serial

import (
	"errors"
)

// Open 在 Windows 下打开串口。
func Open(cfg Config) (*Port, error) {
	return nil, errors.New("Windows 平台串口正在开发中")
}
