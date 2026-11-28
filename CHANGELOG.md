# Changelog

All notable changes to Serval will be documented in this file. The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project adheres to [Semantic Versioning](https://semver.org/).

## [0.6.9] - 2026-11-28

### Added

- **AI Agent Tooling, Safety & Structured Plan (`core/ai/`, `aiagent.go`, `aipanel.go`, `core/sshconn/`)**: Added native Anthropic `/v1/messages` protocol with prompt caching, `sftp_write` and `sftp_edit` with Unified Diff approval cards, `tool_output_read` paging for large outputs, concurrent read-only tool execution, automatic context compaction and secret redaction, lexical dangerous-command guards, structured `update_plan` progress bar, and optional sharded encrypted AI conversation sync (`aichat.vault.json`).
- **Live Execution Feedback & MyGo Plugins (`aipanel.go`, `view.go`, `dualpane.go`, `sheets.go`, `settings.go`, `hostform.go`)**: Integrated MyGo's `plugins/loading` (`BouncingDots`, `Orbit`, `Arc`, `Pulse`, `Radar`) and `plugins/glass` (`ScrollEdge`, `Glass{Interactive: true}`) for live AI Agent phase/elapsed feedback, tool execution indicators, code block header bars, floating scroll-to-bottom pill, and unified async busy indicators across the app.

### Changed

- **Idle CPU & Render Optimization (`view.go`, `design/motion.go`, `design/coat.go`, `aipanel.go`, `hostactions.go`)**: Removed the infinite `ReachableBreathe` animation loop that forced idle 120 FPS full-window redraws, added bounded caches for `design.NewCoat` and AI Markdown parsing, and cleared stale `probeChecking` states on re-probe.

### Fixed

- **Scrollable Approval Card, Timeouts & Localized Errors (`aipanel.go`, `monitor.go`, `app.go`, `editor.go`, `sync.go`, `hostactions.go`)**: Made long AI approval command previews scrollable without pushing action buttons off-screen, fixed duplicated live tool output in headers, added 5s/15s timeouts and cancellation to SSH monitor, distro probe, and remote editor reads, decoupled AI shard sync failures from main vault reload, and replaced remaining raw English SSH/probe errors with actionable Chinese messages.

## [0.6.8] - 2026-11-27

### Added

- **Encrypted AI Agent Conversation History Persistence (`aipanel.go`, `library.go`, `app.go`, `settings.go`, `main.go`, `docs/storage.md`)**: Persisted AI Agent conversations inside the encrypted vault (`kindAIChat` / `aichat`) scoped per host/local/global target so history, tool-call summaries, and compressed context survive tab close, app restart, lock/unlock, sync, and backup import/restore, with a "最近会话" quick-resume list on the welcome view and graceful flush on window close.

### Fixed

- **AI Agent Timeout Redesign & Approval Cancellation (`aipanel.go`, `aiagent.go`, `core/ai/agent.go`, `app.go`)**: Removed the fixed 3-minute whole-turn timeout that caused multi-step troubleshooting and human approval waits to be misreported as "无法连接到 AI 服务", replacing it with per-request response-header (90s / 5m local) and stream-idle (120s) timeouts, mapping `context.DeadlineExceeded` to `ai.ErrTimeout`, resetting `toolWaitingApproval` status on cancel/stop, and distinguishing timeout, stream interruption, invalid response, TLS, DNS, and connection-refused errors in `friendlyAIError`.
- **Cross-Provider Protocol Compatibility (`core/ai/client.go`, `core/ai/agent.go`, `core/ai/agent_test.go`, `core/ai/client_test.go`)**: Preserved Google Gemini `extra_content.google.thought_signature` and OpenRouter `reasoning_details` across multi-turn tool calls, added cross-provider message sanitization (`skip_thought_signature_validator` fallback when switching to Gemini, stripping `reasoning_content`/`reasoning_details`/`extra_content` where unsupported, removing non-standard `tool.name`, and normalizing unsafe/overlong `tool_call_id` pairs), parsed Google JSON-array error payloads, separated display `<think>` text from wire `reasoning_content`, tightened `/responses` fallback, hardened SSE tool-call merging and trailing-EOF tolerance, added pre-output transport retries on stale connections, added `IsDangerousExecCommand` for safe multiline `ssh_exec` probes, and sanitized NUL/UTF-8 boundaries in `formatExecResult`.

## [0.6.7] - 2026-11-26

### Fixed

- **Windows Portable Build Missing `ghostty-vt.dll` (`.github/workflows/release.yml`, `.github/release-notes.md`, `app.go`, `locales/`, `terminal_error_test.go`)**: Packaged all entries from `.mygo-files` (including `ghostty-vt.dll`) into the Windows portable zip with post-archive verification, added portable extraction guidance in release notes, and replaced raw missing-library errors with localized actionable guidance.

## [0.6.6] - 2026-11-25

### Fixed

- **Startup Crash on Fast Session Restore (`main.go`, `app.go`, `main_test.go`)**: Moved `restoreSession()` into `mygo.App.WhenReady` after `a.post = win.Update` is initialized, and guarded `restoreSession()` against `a.post == nil`, preventing a `nil` pointer panic in `connectWithLayout` when saved hosts connect before the window finishes initializing.

## [0.6.5] - 2026-11-25

### Fixed

- **Terminal Drag-and-Drop Upload Directory & User Ownership (`files.go`, `folderupload.go`, `promptstate.go`, `core/sshconn/sftp.go`, `app.go`)**: Resolved drop-upload targets via `paneUserAndDir` (supporting OSC 0 and prompt-tracked paths without downgrading full paths to basenames), resolved the switched user's remote `(Home, UID, GID)` after `su`/`sudo` so `~` expands to the active terminal user's home, preserved existing file permissions on overwrite, and applied `Chown` to uploaded files and newly created directories when permitted.

### Changed

- **Non-Intrusive Floating Transfer Queue (`view.go`, `files.go`, `app.go`)**: Terminal drag-and-drop uploads no longer auto-open the SFTP drawer; instead, a compact floating transfer queue appears at the bottom-right of the terminal and automatically dismisses after all transfers succeed (staying visible on errors/cancellations for retry).

## [0.6.4] - 2026-11-19

### Changed

- **Consolidated About & Updates Tab in Settings (`about.go`, `settings.go`, `update.go`, `view.go`, `palette.go`, `main.go`)**: Moved About & Updates into a dedicated "关于" tab in Settings (displaying author attribution, GitHub repository link, MIT License, MyGo engine version, and update checks), removed the redundant sidebar footer icon and General-tab section, and restored the native macOS menu bar About dialog.

## [0.6.3] - 2026-11-19

### Added

- **Cross-Platform About Sheet (`about.go`, `about_test.go`, `view.go`, `palette.go`, `update.go`, `main.go`)**: Added a unified "关于 Serval" sheet across macOS, Windows, and Linux displaying author attribution (`jhzhang09`), GitHub repository link, MIT License, dynamic MyGo engine version, and integrated update check, accessible from the sidebar footer, Command Palette, Settings, and macOS app menu.

## [0.6.2] - 2026-11-19

### Fixed

- **Terminal Prompt Duplication on Resize (`app.go`, `sessionlog_test.go`)**: Enabled MyGo `ShellIntegration: true` on terminal creation so local `zsh` automatically injects OSC 133 prompt markers and remote shells emitting OSC 133 clear the old prompt at the previous width before reflowing, preventing duplicate prompt ghosts when repeatedly toggling the AI Agent drawer or file drawer.
- **AI Slash & Mention Popup Alignment (`aipanel.go`, `aipanel_test.go`)**: Left-aligned rows in the `/` slash command and `@` context mention popups with full-width ellipsis truncation.

### Docs

- **Light-Mode README Screenshots (`docs/screenshots/`)**: Refreshed `welcome.png`, `workspace.png`, and `ai-agent.png` in light mode for `README.md` and `README.zh-CN.md`.

## [0.6.1] - 2026-11-19

### Added

- **AI Agent Multi-Line Composer, Rich Inline Markdown & Tail Log Reading (`aipanel.go`, `aiagent.go`, `core/sshconn/sftp.go`)**: Upgraded the AI input box to a multi-line auto-sizing `ui.TextArea` (`Enter` sends, `Shift+Enter` inserts newline), added keyboard navigation (`Up`/`Down`/`Tab`/`Enter`/`Esc`) for `@` and `/` popups, `@SFTP目录` context attachment, `ui.RichText` inline bold/code rendering, shell-only terminal insert buttons, answer copy, user message edit-and-resend, history conversation inline rename, and `sftp_read(tail=true)` (`Files.ReadTailLimited`) for large remote/local logs.
- **Hardened AI Safety & Persistent Drawer Preferences (`core/ai/client.go`, `aiagent.go`, `settings.go`, `library.go`, `view.go`)**: Hardened `IsDangerousCommand` against path prefixes, command wrappers, command substitutions, and destructive network/process commands; added `IsReadOnlyProbeCommand` so `aiModeAuto` on production hosts allows read-only probes while requiring confirmation for mutating commands; added Telnet/Serial protocol guards, live session snapshot refresh, SFTP relative path resolution, context history compression, and local persistence for `AIWidth`, `FilesWidth`, and `AIMode`.

### Fixed

- **UI Layout, Modal Ordering, Broadcast/Transfer & AI Concurrency (`aipanel.go`, `view.go`, `app.go`, `hostform.go`, `palette.go`, `settings.go`)**: Fixed form scroll clipping, active tab/split-pane highlight clarity, single-line ellipsis truncation, modal occlusion and background wakeup handling, `startAgentRun` step-map data race, empty-state (`tabID == 0`) AI session migration on first tab open, in-place retry for failed turns, and updated AI Agent i18n catalogs (`locales/`).

## [0.6.0] - 2026-11-18

### Added

- **Dual-Pane SFTP Polish & Built-In Local/Remote Text Editor (`core/sshconn/sftp.go`, `dualpane.go`, `files.go`, `editor.go`, `sheets.go`)**: Added local/remote empty-file creation with conflict detection (`ErrRemoteExists`), symlink-safe recursive directory deletion (`Files.RemoveAll` and `removeLocalPath`), download overwrite confirmation, symmetric filter/hide-dotfiles/sort subbars, local directory quick-jump presets, remote path pinning (`filesPinned`), `"Open in Terminal"` navigation, double-click regular file editing in both panes, local file text editing with mtime/hash conflict detection and atomic writes, and an editor status bar (`lines · bytes · UTF-8 · EOL`).
- **Host Form Test Connection & Auto-Auth Password (`hostform.go`, `app.go`)**: Added optional password entry for `Auto` authentication and a `"Test Connection"` probe action (`testHostConnection`) that validates jump hosts, proxies, and host keys before cleaning up the test session.
- **Navigation Shortcuts, Monitor Bars & Sidebar Clarity (`view.go`, `app.go`, `palette.go`, `hostorder.go`)**: Added `⌘N` (New Host), `⌘T` (New Local Terminal), `⌘1`–`⌘9` (switch tabs), and `⌘⌥←`/`→` (step split-pane focus, also exposed in Command Palette); displayed `#tag` chips (up to 3 + `+N`) on host rows, live indicator dots & host counts on collapsed groups, clickable active tunnel badges (`paneTunnels`) on pane headers, and 3px `ui.Progress` resource bars in the Server Monitor strip.
- **Snippet Variable Defaults & Encrypted Vault Backup/Restore (`snippet.go`, `settings.go`, `dialogs.go`)**: Added `{{var:default}}` placeholder prefill and search filtering in the Snippets panel, plus encrypted vault backup export and backup restore/merge (supporting same-vault HLC merge, cross-vault password-unlocked merge, and empty-vault adoption) in Settings.

### Fixed

- **Nested Row Buttons & Missing Palette Arrow Icons (`dualpane.go`, `design/icons/`)**: Refactored file/bookmark rows into side-by-side non-nested buttons for accessibility and automation, and added missing Lucide `arrow-left` and `arrow-right` SVG icons alongside `file-plus`, `radar`, `arrow-up-down`, `pin`, and `pin-off`.

## [0.5.2] - 2026-11-18

### Changed

- **MyGo v0.10.0 Upgrade & UI Lifecycle Migration (`go.mod`, `*.go`)**: Upgraded `github.com/egoist/mygo` from `0.9.0` to `0.10.0`, migrated UI keys to `Context.Key`, replaced per-frame focus polling (`focusSearch`/`focusTerm`) with persistent `ui.Handle` bindings (`searchHandle`/`termHandle` with `SelectAll` on find reopen), and updated UI scroll tests for tighter scroll clipping.
- **Glass Popover & Context Menus (`sheets.go`, `view.go`, `dualpane.go`, `aipanel.go`, `keyspanel.go`, `proxypanel.go`, `snippet.go`, `settings.go`)**: Styled all in-window context menus and dropdown menus with `glass.Glass` panels, card borders, and concentric corners matching modal sheets.
- **Start-Truncated Path Ellipsis (`view.go`, `dualpane.go`, `editor.go`, `settings.go`)**: Added `.TruncateStart()` to pane header paths, dual-pane SFTP titles, remote editor subtitles, session log directories, and local sync folder paths so trailing directory/file names remain visible when space is tight.
- **Cleaner Terminal Exit & Leaner Windows Builds (`app.go`, `.github/workflows/`)**: Enabled `terminal.Options.NoExitMessage` so ended sessions use Serval's header chip and reconnect banner without injecting `[Process exited]` into the terminal screen, and added `-tags=mygo_nowebview` to Windows CI and release builds to omit unused WebView2 loader code.

## [0.5.1] - 2026-11-17

### Fixed

- **Windows / Linux Titlebar Caption Inset (`view.go`, `tab_test.go`)**: Reserved `c.TitleBar().Right` padding in the top tab bar so native window minimize/maximize/close controls no longer overlap or intercept clicks on the Find, SFTP Files, and AI Agent buttons.
- **Sidebar Command Shortcut Wrapping on Windows (`view.go`, `editor.go`, `design/icons/command.svg`)**: Replaced the fixed-width multi-line `Ctrl+Shift+K` badge inside the sidebar filter input with a compact command icon on non-macOS platforms (keeping the full shortcut in its tooltip), and prevented wrapping on the editor save hint and welcome shortcut row.

## [0.5.0] - 2026-11-17

### Added

- **Enter-to-Reconnect on Ended Terminals (`app.go`, `locales/`)**: Pressing `Enter` inside a focused disconnected terminal pane immediately triggers reconnection cleanly without interfering with find bars, dialogs, or active auto-reconnects, and canceling auto-reconnect restores terminal focus.
- **Guided Sync & Restore Onboarding (`sync.go`, `settings.go`, `design/icons/external-link.svg`)**: Upgraded both Settings → Sync and the empty-device **Restore Vault** dialog with WebDAV/S3 vendor presets, beginner-friendly step-by-step guides, vendor-native credential terminology hints (`SecretId`/`SecretKey`, `AK`/`SK`), automatic `Region` inference from Endpoints, one-click console links, and automatic subdirectory fallback (`serval/vault.json` ↔ root `vault.json`).

### Changed

- **Cloud S3 Protocol Compatibility (`core/syncer/s3.go`)**: Automatically selects Virtual-Hosted-Style for AWS S3 (with regional `s3.<region>.amazonaws.com` endpoints), Aliyun OSS, Tencent COS, and Huawei OBS while keeping Path-Style for Cloudflare R2 and MinIO, and classifies S3 XML errors (`NoSuchBucket`, `SignatureDoesNotMatch`, `AccessDenied`, `PermanentRedirect`) for accurate troubleshooting.
- **Dependency Updates**: Upgraded `github.com/egoist/mygo` to `0.9.0`, `golang.org/x/crypto` to `0.59.0`, `github.com/klauspost/compress` to `1.18.7`, and `github.com/cloudflare/circl` to `1.6.3`.

## [0.4.0] - 2026-11-17

### Added

- **Google Gemini & MiniMax AI Providers (`settings.go`, `core/ai/client.go`)**: Added official Google Gemini (OpenAI-compatible endpoint with `imagen`/`veo`/`lyria` filtering) and MiniMax (`MiniMax-M2.7` / `MiniMax-M2.5` presets) providers, plus priority-ranked `/models` discovery and structured API error messages.
- **Per-Terminal AI Conversation History (`aipanel.go`, `design/icons/history.svg`)**: Each terminal tab now maintains its own independent AI conversation history list (`历史会话`), one-click `新开会话` (`/new`), conversation switching, and per-conversation deletion without cross-tab leakage.

### Changed

- **Per-Tab AI & SFTP Drawer Isolation (`app.go`, `aipanel.go`, `aiagent.go`, `files.go`)**: Isolated AI Agent drawer visibility (`open`), active streaming turns, and SFTP file drawer state strictly per terminal tab so opening, closing, or switching tabs never mutates other tabs or inherits old drawers onto newly opened terminals.
- **Unified Workspace Chrome & Entry Points (`view.go`)**: Consolidated duplicate AI and split-pane controls into clean architectural layers—sidebar footer strictly for global vault resources and settings, top tab toolbar strictly for tab tools (`查找 ⌘F`, `文件 ⌘J`, and the single `AI Agent ⌘I` drawer toggle), and per-pane headers/`••• 窗格操作` menus for pane splitting and connection controls.

### Fixed

- **Intentional AI Triggering (`hostactions.go`, `aipanel.go`)**: Opening the AI drawer from the UI or switching tabs never automatically dispatches terminal error analysis; one-click error analysis remains explicitly triggered via `⌘E` / Command Palette.
- **Update Dialog Changelog & Windows Test Stability (`update.go`, `core/sshconn`)**: Enabled full Markdown rendering for GitHub Release notes in the update dialog and resolved Windows loopback probe/port-forward test races.

## [0.3.0] - 2026-11-16

### Added

- **Multi-Provider AI Management & Model Auto-Fetching (`settings.go`, `aipanel.go`, `core/ai`)**: Replaced fixed provider presets with full multi-provider management (`+ 添加提供商`) supporting OpenAI, DeepSeek, Qwen, Kimi, Ollama, and custom OpenAI-compatible endpoints, isolated per-provider keychain API keys, edit/delete actions, and automatic `/models` endpoint fetching with a model picker.
- **Multi-Tab Opening on Double-Click (`hostactions.go`)**: Double-clicking an already-connected host in the sidebar now launches an additional tab (`name (2)`, `name (3)`) instead of only focusing the existing tab.
- **Updated Documentation & Screenshots (`README.md`, `README.zh-CN.md`, `docs/screenshots/`)**: Refreshed hero workspace screenshot and added Welcome/Host Sidebar and Split Terminal/AI Agent previews.

### Changed

- **Host Connectivity & Active-Session Indicators (`core/sshconn/probe.go`, `design/coat.go`, `hostactions.go`, `view.go`)**: Upgraded SSH probing to validate the RFC 4253 `SSH-` banner and tunnel through SOCKS5/HTTP proxies (eliminating Surge/TUN local TCP false positives), separated connectivity state onto host icons (subtle breathing when reachable, muted gray when unreachable) from the right-side green dot reserved strictly for open sessions, and cleared stale probe results when editing a host.

### Fixed

- **macOS Keychain Prompt Caching (`app.go`, `settings.go`, `sync.go`)**: Cached OS keychain reads in memory per session (`isRememberedOnDevice`, `syncPassword`) and persisted `aiKeyMigrated` in `device.json` to prevent repeated macOS Keychain prompts during startup and vault operations.
- **Pane Header Button Shift on Disconnect (`library.go`)**: Fixed the transient `cols × rows` badge disappearance causing `"重新连接"` / `"取消"` action buttons to shift mid-click.

## [0.2.0] - 2026-11-14

### Added

- **S3-Compatible Cloud Sync (`core/syncer/s3.go`)**: Added pure-Go AWS SigV4 sync support for Amazon S3, Cloudflare R2, MinIO, Alibaba OSS, Tencent COS, and Huawei OBS (`s3://bucket/vault.json`), with ETag optimistic locking, unified conflict overwrite option, and unified Settings Sync tab with draft auto-save and one-click **Sync Now**.
- **Telnet & Cross-Platform Serial Transports (`core/telnet`, `core/serial`)**: Added Telnet and cross-platform Serial port connections (including Windows Win32 Overlapped I/O and registry COM port enumeration, macOS, and Linux) with terminal isolation and protocol-specific controls.
- **Docked AI Agent & Quick Error Analysis (`core/ai`, `aiagent.go`, `aipanel.go`)**: Added a resizable right-hand AI Agent drawer (`⌘I` / `Ctrl+Shift+I`) and one-click terminal pane error analysis (`⌘E` / `Ctrl+Shift+E`), supporting OpenAI SSE streaming, collapsible thinking/tool cards, GFM Markdown tables, multi-profile provider switching (DeepSeek, OpenAI, Qwen, Kimi, Ollama, and custom OpenAI-compatible endpoints), `@` context mentions, `/` slash commands, and 3 permission levels (Auto with dangerous-command guard, Confirm, Observer) backed by encrypted keychain API keys.
- **In-App Update Checker (`update.go`)**: Added asynchronous GitHub Releases version checking, startup auto-check toggle, release notes sheet, and one-click Homebrew upgrade command copy / GitHub download link.
- **Lightweight SSH Server Monitor (`monitor.go`)**: Added a server health status bar displaying CPU load, memory, and disk usage for active SSH sessions.
- **OS / Distro Auto-Detection (`core/hosts/distro.go`)**: Automatically detects remote OS/distributions and displays crisp distro icons alongside deterministic host coats.
- **Host Drag-and-Drop Reordering & Persistent Custom Groups (`hostorder.go`)**: Added sidebar drag-and-drop host reordering within groups and across groups, plus persistent empty custom groups.
- **SSH Workflow & 23-Item UI/UX Polish**: Added one-click `ssh-copy-id` public-key deployment, async TCP/RTT latency probing, multi-tag (`#tag`) filtering, logon scripts, `user@host[:port]` paste parsing, terminal copy-on-select & right-click behavior options, SFTP directory bookmarks & editable path bars, resizable sidebar, dedicated **Security** settings tab, global password/secret reveal toggles, scrollable tab bar with `+` Local Shell button and connection pills, `broadcastBar` with `#1..N` pane chips, sidebar Snippets entry with one-click run, local pane context operations in the dual-pane SFTP drawer, and queue retry/remove/clear-completed controls.

### Fixed

- **WebDAV Resilience & Proxy Compatibility**: Enforced HTTP/1.1 with transient 502/504/connection-reset retries, multi-layer transparent Gzip decompression, `PROPFIND` metadata probing, strong ETag normalization, automatic `MKCOL` parent directory creation, and stale temp cleanup.
- **Cross-Platform & UI Stability**: Fixed Windows CI `TestExecContext` failure without `/bin/sh` (`TestExecContextCrossPlatform`), Windows backslash path shortening and loopback RTT/port checks, `ssh-keygen` comment preservation on OpenSSH private-key import, `@`/`/` AI quick menu layout alignment, narrow-window mutual exclusion between SFTP and AI drawers, and zero-tab `⌘W` window-close protection.

## [0.1.0] - 2026-11-09

### Features
- **原生 GPU 渲染工作台**：基于 Go 与 [MyGo](https://github.com/egoist/mygo) 构建，无 WebView / Electron 依赖，支持浅色、深色与跟随系统主题，以及简体中文与英文双语界面。
- **端到端加密保险库（`core/vault`）**：采用 Argon2id 派生主密码与 XChaCha20-Poly1305 按记录加密存储主机、私钥、密码、口令、TOTP 密钥、代理、端口转发与代码片段；支持系统钥匙串自动解锁、闲置自动锁定、混合逻辑时钟（HLC）按记录合并、WebDAV 云同步与本地同步文件夹。
- **SSH 连接、多级跳板机与代理**：支持密码、Ed25519 / RSA 私钥、ssh-agent、keyboard-interactive 多步验证与 RFC 6238 TOTP 自动填写；支持最多 8 级跳板机链路、SOCKS5 / HTTP 代理、ssh-agent 转发、断线指数退避自动重连与上次会话布局恢复。
- **端口转发管理**：支持本地转发（Local）、远程转发（Remote）与动态转发（SOCKS5），可绑定主机随连接自动启停并实时查看流量统计。
- **多标签、分屏终端与本机 Shell**：支持水平／垂直任意比例分屏、终端内查找（`⌘F`）、多窗格广播输入（生产环境独立红框警示）、带 `{{变量}}` 占位符的代码片段与纯文本远程会话日志。
- **双栏可调 SFTP 文件抽屉与 ZMODEM（`rz` / `sz`）**：左右并排显示本机与远程目录路径，支持拖动调整抽屉宽度与左右分栏比例；支持文件与递归文件夹双向拖拽上传／下载、访达拖入上传、拖出到桌面、传输队列进度与取消、新建文件夹／重命名／改权限／删除、远端 UTF-8 文本在线编辑（带外部修改冲突检测），以及纯 Go 原生 ZMODEM（`rz` / `sz`）传输。
