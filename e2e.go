//go:build e2e

package main

// 自动化驱动只在 e2e 构建中启用，不进入发布版本。
import _ "github.com/egoist/mygo/plugins/automation"
