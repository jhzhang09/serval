package sshconn

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// ErrInvalidPublicKey 表示待部署的公钥内容不是合法的 OpenSSH 单行公钥。
var ErrInvalidPublicKey = errors.New("sshconn: 无效的 OpenSSH 公钥")

// DeployPublicKeyResult 描述一次公钥部署的结果。
type DeployPublicKeyResult struct {
	// AlreadyPresent 为 true 表示远端 authorized_keys 里已经包含该公钥，未重复写入。
	AlreadyPresent bool
}

// DeployPublicKey 把 OpenSSH 公钥幂等地追加到远端 ~/.ssh/authorized_keys，
// 自动创建 ~/.ssh（0700）与 authorized_keys（0600），已存在相同公钥时跳过。
func DeployPublicKey(ctx context.Context, client *Client, pubKey string) (DeployPublicKeyResult, error) {
	if client == nil {
		return DeployPublicKeyResult{}, errors.New("sshconn: 客户端未连接")
	}
	trimmed := strings.TrimSpace(pubKey)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n\x00") {
		return DeployPublicKeyResult{}, ErrInvalidPublicKey
	}
	parsed, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(trimmed))
	if err != nil {
		return DeployPublicKeyResult{}, fmt.Errorf("%w: %v", ErrInvalidPublicKey, err)
	}
	canonical := strings.TrimSpace(string(bytes.TrimSpace(ssh.MarshalAuthorizedKey(parsed))))
	if comment != "" {
		canonical += " " + comment
	}
	// 使用 POSIX sh 并在远端严格校验目录/文件权限；单引号转义避免命令注入。
	keyLiteral := "'" + strings.ReplaceAll(canonical, "'", `'\''`) + "'"
	script := fmt.Sprintf(`umask 077 && dir="$HOME/.ssh" && auth="$dir/authorized_keys" && key=%s && mkdir -p "$dir" && chmod 700 "$dir" && touch "$auth" && chmod 600 "$auth" && if grep -qxF -- "$key" "$auth"; then printf 'ALREADY\n'; else if [ -s "$auth" ] && [ -n "$(tail -c 1 "$auth")" ]; then printf '\n' >> "$auth"; fi; printf '%%s\n' "$key" >> "$auth" && printf 'ADDED\n'; fi`, keyLiteral)

	res, err := client.ExecContext(ctx, script, 8<<10)
	if err != nil {
		return DeployPublicKeyResult{}, fmt.Errorf("部署公钥失败: %w", err)
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(res.Stdout)
		}
		if msg == "" {
			msg = fmt.Sprintf("退出码 %d", res.ExitCode)
		}
		return DeployPublicKeyResult{}, fmt.Errorf("部署公钥失败: %s", msg)
	}
	out := strings.TrimSpace(res.Stdout)
	if strings.Contains(out, "ALREADY") {
		return DeployPublicKeyResult{AlreadyPresent: true}, nil
	}
	return DeployPublicKeyResult{AlreadyPresent: false}, nil
}
