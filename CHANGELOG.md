# Changelog

All notable changes to Serval will be documented in this file.

## [0.1.0] - 2026-11-09

### Features
- **原生 GPU 渲染工作台**：基于 Go 与 [MyGo](https://github.com/egoist/mygo) 构建，无 WebView / Electron 依赖，支持浅色、深色与跟随系统主题，以及简体中文与英文双语界面。
- **端到端加密保险库（`core/vault`）**：采用 Argon2id 派生主密码与 XChaCha20-Poly1305 按记录加密存储主机、私钥、密码、口令、TOTP 密钥、代理、端口转发与代码片段；支持系统钥匙串自动解锁、闲置自动锁定、混合逻辑时钟（HLC）按记录合并、WebDAV 云同步与本地同步文件夹。
- **SSH 连接、多级跳板机与代理**：支持密码、Ed25519 / RSA 私钥、ssh-agent、keyboard-interactive 多步验证与 RFC 6238 TOTP 自动填写；支持最多 8 级跳板机链路、SOCKS5 / HTTP 代理、ssh-agent 转发、断线指数退避自动重连与上次会话布局恢复。
- **端口转发管理**：支持本地转发（Local）、远程转发（Remote）与动态转发（SOCKS5），可绑定主机随连接自动启停并实时查看流量统计。
- **多标签、分屏终端与本机 Shell**：支持水平／垂直任意比例分屏、终端内查找（`⌘F`）、多窗格广播输入（生产环境独立红框警示）、带 `{{变量}}` 占位符的代码片段与纯文本远程会话日志。
- **双栏可调 SFTP 文件抽屉与 ZMODEM（`rz` / `sz`）**：左右并排显示本机与远程目录路径，支持拖动调整抽屉宽度与左右分栏比例；支持文件与递归文件夹双向拖拽上传／下载、访达拖入上传、拖出到桌面、传输队列进度与取消、新建文件夹／重命名／改权限／删除、远端 UTF-8 文本在线编辑（带外部修改冲突检测），以及纯 Go 原生 ZMODEM（`rz` / `sz`）传输。
