package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// expandHome 展开路径开头的 ~/，其他路径原样返回。
func expandHome(p string) (string, error) {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法确定用户目录: %w", err)
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}
