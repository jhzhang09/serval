#!/usr/bin/env bash
# 启动真机验收环境：一台只监听回环地址的开发 SSH 服务器，加一个开启了自动化接口的 Serval。
#
#   tools/e2e/run.sh <工作目录>
#
# 工作目录里会生成：serval-e2e、devsshd、dev-password（临时密码）、config/（Serval 的配置目录，
# 和你自己的数据完全隔离）、serval.sock（自动化接口）。关闭 Serval 窗口后开发服务器一起退出。
# 用完要关闭 Serval 窗口；用脚本关闭时注意两个进程都是用相对路径启动的（./serval-e2e、./devsshd），
# 按路径匹配进程会漏掉它们，按工作目录或 PID 关闭。
# 然后在另一个终端里运行：python3 tools/e2e/serval_e2e.py <工作目录> smoke
#
# 环境变量：PORT（开发服务器端口，默认 22222）、AUTHORIZED_KEY（.pub 文件，开发服务器只认这把钥匙）。
set -euo pipefail

work=${1:?用法: tools/e2e/run.sh <工作目录>}
repo=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p "$work/config"
work=$(cd "$work" && pwd)

cd "$repo"
go build -tags e2e -o "$work/serval-e2e" .
go build -o "$work/devsshd" ./cmd/devsshd

if [[ ! -s "$work/dev-password" ]]; then
	(umask 077 && openssl rand -hex 12 >"$work/dev-password")
fi
args=(-addr "127.0.0.1:${PORT:-22222}")
if [[ -n "${AUTHORIZED_KEY:-}" ]]; then
	args+=(-authorized-key "$AUTHORIZED_KEY")
fi

cd "$work"
SERVAL_DEV_PASSWORD=$(cat dev-password) ./devsshd "${args[@]}" &
server=$!
trap 'kill $server 2>/dev/null || true' EXIT
# 端口被占用时 devsshd 会立即退出。这时如果继续启动 Serval，会连到占着端口的另一台服务器上，
# 表现成“密码不正确”这类难以理解的失败，所以直接报错。
sleep 0.5
if ! kill -0 "$server" 2>/dev/null; then
	echo "开发服务器没有启动（端口 ${PORT:-22222} 可能被占用：lsof -nP -iTCP:${PORT:-22222} -sTCP:LISTEN）" >&2
	exit 1
fi

# Unix socket 路径有长度上限（macOS 约 104 字节），所以在工作目录里用相对路径。
rm -f serval.sock
MYGO_AUTOMATION=serval.sock SERVAL_CONFIG_DIR="$work/config" ./serval-e2e
