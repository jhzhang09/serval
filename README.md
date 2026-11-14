<div align="center">

<img src="resources/icon.png" width="96" alt="Serval icon">

# Serval

**A fast, lightweight, native cross-platform SSH & SFTP workbench written in Go.**

Connect via SSH, Telnet, Serial, or Local Shell with multi-tab and split-pane terminals, multi-hop jump hosts, SOCKS5 & HTTP proxies, port forwarding, a resizable dual-pane SFTP drawer, native in-terminal `rz` / `sz` (ZMODEM) transfers, RFC 6238 TOTP autofill, multi-pane broadcast input, parameterized snippets, and a docked AI Agent — all backed by an end-to-end encrypted vault with conflict-free sync over WebDAV, S3-compatible storage, or local sync folders.

[![Checks](https://github.com/jhzhang09/serval/actions/workflows/check.yml/badge.svg)](https://github.com/jhzhang09/serval/actions/workflows/check.yml)
[![Release](https://img.shields.io/github/v/release/jhzhang09/serval?include_prereleases&sort=semver)](https://github.com/jhzhang09/serval/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/jhzhang09/serval)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Platforms](https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey)
[![Listed on Awesome MyGo](https://awesomemygo.dev/badge.svg)](https://awesomemygo.dev/apps/serval)

**English** · [简体中文](README.zh-CN.md)

</div>

![Serval Workspace](docs/screenshots/welcome.png)

## Why Serval

- **True native desktop UI, zero web view.** Built with [MyGo](https://github.com/egoist/mygo): pure Go, GPU-rendered native controls, no Electron, no WebView, no Node.js, and no external runtime dependencies. Ships as a ~5.7 MB macOS installer and idles at ~100 MB RAM across multiple live sessions with near-0% CPU.
- **End-to-end encrypted vault with conflict-free sync.** Every host, private key, password, passphrase, TOTP secret, proxy, port-forwarding rule, and snippet is encrypted per record with **XChaCha20-Poly1305** under a vault key wrapped by **Argon2id** (even hostnames and IP addresses are never stored in plaintext on disk). Built-in Hybrid Logical Clock (HLC) per-record merging syncs seamlessly across devices via **WebDAV**, **S3-compatible object storage** (AWS S3, Cloudflare R2, MinIO, Alibaba OSS, Tencent COS), or a **local sync folder** (iCloud Drive, Dropbox, Syncthing).
- **Cool glass aesthetic & host safety guards.** Translucent native sidebar and titlebar vibrancy (supporting macOS Liquid Glass), deterministic geometric host coats and detected OS/distro badges, explicit red borders and broadcast confirmation guards for **Production** hosts, and a subtle dim veil on inactive split panes.
- **Jitter-free terminal, SFTP, and ZMODEM workflow.** Toggling the sidebar or dual-pane SFTP drawer commits geometry in a single step — never triggering dozens of `SIGWINCH` reflow storms on remote servers. The resizable dual-pane SFTP drawer tracks the active terminal directory (`OSC 7`), supports bidirectional file/folder drag-and-drop, inline remote UTF-8 editing, and clean-room pure-Go `rz` / `sz` (ZMODEM).
- **Strict host-key and credential security.** First-time connections require explicit fingerprint confirmation (encrypted in the vault and synced across devices, while `~/.ssh/known_hosts` is consulted read-only). Changed host keys are always rejected without an insecure "continue anyway" bypass.

## Features

| Area | Capabilities |
|---|---|
| **Encrypted Vault & Sync** | Per-record XChaCha20-Poly1305 encryption with Argon2id key derivation; guided master-password setup on first secret save; dedicated **Security** settings tab for idle auto-lock, OS keychain remember toggle, master-password rotation, and Known Hosts; global password/secret reveal toggles (auto-cleared on close or lock); multi-device sync via **WebDAV**, **S3-compatible storage**, or **local sync folder** with ETag optimistic locking, draft auto-save, and one-click **Sync Now**. |
| **Hosts, Groups & Palette** | Resizable host sidebar (persisted locally); add, edit, duplicate, delete, and reorder hosts via drag-and-drop across persistent empty or populated custom groups; multi-tag (`#tag`) horizontal filter pills and instant search clear; paste `user@host[:port]` auto-split and collapsible advanced form sections; async RTT latency probing and OS/distro detection badges; **Production** host red borders and broadcast guards; `⌘K` Command Palette for quick connect and actions. |
| **Authentication, MFA & Keys** | Auto (private key → `ssh-agent` → password), Password-only, or Key-only modes; built-in Key Manager to import or generate **Ed25519** / **RSA 4096** keys, import keys directly inside the host form, and idempotently deploy public keys (`ssh-copy-id`) to `~/.ssh/authorized_keys`; `keyboard-interactive` (PAM / OTP) + **RFC 6238 TOTP** (`otpauth://` or Base32); optional `ssh-agent` forwarding and automatic logon scripts. |
| **Jump Hosts & Proxies** | Chain up to **8 jump hosts** with address summaries, route previews, and automatic cycle detection, plus per-hop fingerprint and secret prompts; manage **SOCKS5** and **HTTP** proxies (with encrypted proxy passwords) reusable across hosts. |
| **Port Forwarding** | Unified management of **Local**, **Remote**, and **Dynamic (SOCKS5)** port forwards; compact list actions (toggle/delete), host selector dropdown for automatic start/stop with sessions, or manual toggle with live listener status and byte counters. |
| **Multi-Tab & Split Terminals** | Mix **SSH**, **Telnet**, **Serial**, and **Local Shell** tabs with horizontally scrollable tabs, quick `+` Local Shell button, non-occluding connection pills, and drag/keyboard tab reordering; arbitrary horizontal/vertical splits; in-terminal search (`⌘F`); lightweight SSH server monitor bar; exponential-backoff auto-reconnect; layout restore on launch; curated terminal themes and scrollback control. |
| **Broadcast Input & Snippets** | **Broadcast Input** reveals a workspace bar (`#1..N` pane chips + select all) to mirror keystrokes across chosen panes (with mandatory confirmation for Production panes); **Snippets** have a dedicated sidebar entry, one-click row execution, a More menu, `{{variable}}` template prompts, and multi-line confirmation. |
| **Dual-Pane SFTP Drawer** | Toggle with `⌘J` (auto-swaps with AI drawer on narrow windows): side-by-side Local and Remote panes with click-to-edit path bars, saved remote directory bookmarks, draggable drawer width and split divider, recursive folder drag-and-drop, local pane context actions (mkdir, rename, upload, remove empty dir), queue retry/remove/clear-completed controls, and conflict-checked **remote UTF-8 text editing**. |
| **ZMODEM (`rz` / `sz`) & Logs** | Clean-room pure-Go ZMODEM stack: run `rz` or `sz` directly inside any remote shell to open native file/folder pickers with live progress, speed, and cancellation; optional plain-text remote session logging. |
| **AI Agent** | Toggle docked right-hand AI Agent (`⌘I`) with multi-profile switching (DeepSeek, OpenAI, Qwen, Kimi, Ollama, or any OpenAI-compatible API) and one-click pane error analysis (`⌘E`); SSE streaming, collapsible thinking & tool cards, multi-turn `ssh_exec` / SFTP / terminal inspection, 3 permission levels (Auto with dangerous-command guard / Confirm / Observer), native Markdown tables, `@` mentions, and `/` slash commands. |

## Installation

### Homebrew (macOS)

```bash
# Desktop app (macOS 13+) — installs Serval.app, links `serval`, and clears quarantine automatically
brew install --cask jhzhang09/tap/serval
```

### Manual download

Download the latest release from [**GitHub Releases**](https://github.com/jhzhang09/serval/releases).

| Platform | Package |
|---|---|
| macOS 13+ (Apple Silicon & Intel Universal) | `serval-<version>.dmg` |
| Windows 10 1809+ / Windows 11 | `serval-setup-<version>-<arch>.exe` (installer) or `serval-<version>-windows-<arch>-portable.zip` (portable) |
| Linux (glibc 2.28+, GTK 3) | `serval_<version>_<arch>.deb` or `serval-<version>-linux-<arch>.tar.gz` |

Every release includes `SHA256SUMS.txt` and a [GitHub build provenance attestation](https://docs.github.com/actions/security-for-github-actions/using-artifact-attestations) verifiable via:

```bash
gh attestation verify <file> --repo jhzhang09/serval
```

<a id="macos-gatekeeper"></a>
<details>
<summary><b>macOS (manual <code>.dmg</code> download): "Serval can't be opened"</b></summary>

Release builds are ad-hoc signed (Homebrew Cask clears quarantine automatically during installation). If you downloaded the `.dmg` manually in a browser, clear the quarantine attribute once with:

```bash
xattr -dr com.apple.quarantine /Applications/Serval.app
```

or attempt to open the app, then go to **System Settings → Privacy & Security → Open Anyway**.
</details>

<details>
<summary><b>Windows: SmartScreen warning</b></summary>

Choose **More info → Run anyway**.
</details>

<details>
<summary><b>Linux</b></summary>

```bash
sudo apt install ./serval_<version>_amd64.deb
# or run from the archive without installing (or run ./install.sh to install under ~/.local):
tar xzf serval-<version>-linux-amd64.tar.gz && ./serval
```
</details>

### Build from source

Requires Go 1.27.3 or newer on macOS 13+, Windows 10 1809+, or Linux (glibc 2.28+, GTK 3).

```bash
git clone https://github.com/jhzhang09/serval.git
cd serval
go tool mygo dev      # run in development mode
go test ./...         # run the full test suite
go tool mygo build    # package native desktop app into build/ (.app and .dmg on macOS)
go run ./cmd/genicon  # regenerate application icon
```

## Themes, Appearance & Shortcuts

Open **Settings** from the bottom of the sidebar, press `⌘,` (`Ctrl+Shift+,` on Windows/Linux), or use the Command Palette (`⌘K`) to configure **General**, **Security**, **Terminal**, **Sync**, and **AI** preferences:

- **Appearance & Languages**: **Light**, **Dark**, or **System** appearance (with translucent vibrancy), plus **English** and **简体中文** interface localization matching OS language preferences.
- **Terminal Themes**: System default plus **Dracula**, **Nord**, **Tokyo Night**, **Catppuccin Mocha**, **Catppuccin Latte**, **GitHub**, and **One Light**, applied live with customizable font size, line height, cursor style, copy-on-select, right-click behavior, and scrollback limit.

macOS uses `⌘`; Windows and Linux use `Ctrl+Shift` so terminal control keys (`Ctrl+D`, `Ctrl+W`, `Ctrl+K`) remain untouched:

| Action | macOS | Windows / Linux |
|---|---|---|
| Open Settings | `⌘,` | `Ctrl+Shift+,` |
| Command Palette (search hosts, quick connect, actions) | `⌘K` | `Ctrl+Shift+K` |
| Find in active terminal pane | `⌘F` | `Ctrl+Shift+F` |
| Split pane right | `⌘D` | `Ctrl+Shift+D` |
| Split pane down | `⌘⌥D` | `Ctrl+Shift+Alt+D` |
| Close active pane | `⌘W` | `Ctrl+Shift+W` |
| Switch previous / next tab | `⌘[` / `⌘]` | `Ctrl+Shift+[` / `]` |
| Move tab left / right | `⌘⌥[` / `⌘⌥]` | `Ctrl+Shift+Alt+[` / `]` |
| Toggle dual-pane SFTP drawer | `⌘J` | `Ctrl+Shift+J` |
| Toggle AI Agent drawer | `⌘I` | `Ctrl+Shift+I` |
| Analyze terminal output with AI | `⌘E` | `Ctrl+Shift+E` |
| Toggle host sidebar | `⌘\` | `Ctrl+Shift+\` |
| Save remote text editor buffer | `⌘S` | `Ctrl+S` |

## Security & Sync Guarantees

All hosts, private keys, passwords, passphrases, TOTP secrets, proxies, port-forwarding rules, and snippets live in Serval's cross-platform encrypted `vault.json`. See [docs/storage.md](docs/storage.md) for the complete specification.

- **Per-record encryption.** Your master password derives a wrapping key via **Argon2id** to protect a random vault key, and every record is encrypted independently with **XChaCha20-Poly1305**. On disk, only record IDs, types, and versions appear in plaintext — hostnames, IP addresses, usernames, passwords, and private keys are never exposed.
- **Zero-compromise host verification.** Serval consults `~/.ssh/known_hosts` read-only and stores trusted host keys encrypted in the vault so verified fingerprints roam across devices. Changed host keys are unconditionally rejected.
- **Opt-in OS keychain unlock.** By default, "Remember on this device" stores only the local vault key (never your data) inside macOS Keychain, Windows Credential Manager, or Linux Secret Service, and gracefully falls back to master-password prompts on headless Linux setups.
- **Conflict-free multi-device merge.** Every record carries a Hybrid Logical Clock (HLC) version with 90-day tombstones so concurrent edits on different devices or app versions merge safely without dropping newer fields. Sync supports **WebDAV**, **S3-compatible object storage** (AWS S3, Cloudflare R2, MinIO, Alibaba OSS, Tencent COS via pure-Go AWS SigV4), and **local sync folders** (iCloud Drive, OneDrive, Syncthing).
- Serval intentionally does not read `~/.ssh/config` or import credentials from external clients.

## Verification Results (Apple M2 Max, MyGo v0.8.1)

| Metric | Result |
|---|---|
| Bundle size | `Serval.app` 18.0 MB, `.dmg` disk image 5.7 MB |
| 2 sessions, 3 terminals | ~116 MB RSS; ~0.1% idle CPU (settled), 0.15% with no active sessions |
| 10 idle sessions | ~100 MB RSS, ~0% CPU |
| 1M lines (~66 MB output) | 1.62 s (loopback throughput benchmark) |
| 6 repeated open/close cycles | Flat memory footprint, all TCP sockets released cleanly |
| Terminal rendering | 24-bit true color, CJK wide glyphs, `vim` splits, full-screen `top` |
| Vault & key security | Encrypted key import, Argon2id KDF, XChaCha20-Poly1305 storage; zero plaintext hosts/IPs/secrets in `vault.json` |
| Jump hosts, proxies & tunnels | Verified multi-hop prompts, SOCKS5/HTTP proxies, and Local/Remote/Dynamic forwarding round-trips |

## Documentation

| Document / Package | Description |
|---|---|
| [AGENTS.md](AGENTS.md) | Engineering conventions, commands, hard constraints, and testing rules |
| [docs/architecture.md](docs/architecture.md) | Package boundaries, threading model, data flow, and extension points |
| [docs/storage.md](docs/storage.md) | Encrypted vault format, HLC versioning, and record merge specification |
| [docs/roadmap.md](docs/roadmap.md) | Feature roadmap, completed milestones, and acceptance criteria |
| [docs/releasing.md](docs/releasing.md) | GitHub Actions release pipeline and Homebrew Cask workflow |
| [docs/review-checklist.md](docs/review-checklist.md) | Pre-delivery self-check and review criteria |
| `core/sshconn` | SSH transport, multi-hop jump hosts, proxies, host-key verification, shell, and SFTP (UI-free) |
| `core/vault` | Encrypted vault, Argon2id key derivation, HLC versioning, and per-record merge (UI-free) |
| `core/syncer` | WebDAV, S3-compatible (SigV4), and sync-folder engines (UI-free) |
| `core/keys` | Private-key parsing, Ed25519/RSA generation, and fingerprints (UI-free) |
| `core/hosts` | Host, proxy, port-forward models, OS/distro detection, and route resolution (UI-free) |
| `core/totp` | RFC 6238 TOTP code generation (UI-free) |
| `core/zmodem` | Clean-room pure-Go ZMODEM (`rz` / `sz`) protocol implementation (UI-free) |
| `core/ai` | OpenAI SSE streaming client, tool-calling Agent loop, and Markdown parser (UI-free) |
| `core/telnet` / `core/serial` | Telnet protocol and cross-platform Serial port transports (UI-free) |
| `design/` | Design tokens, colors, typography, Lucide icons, animations, and host coats |

## Contributing

Contributions, bug reports, and feature requests are welcome! Please review [AGENTS.md](AGENTS.md) and [docs/review-checklist.md](docs/review-checklist.md) before opening a pull request, and verify all checks pass locally:

```bash
go vet ./... && gofmt -l .                      # gofmt must produce no output
go tool mygo vet .
go test -race -count=1 -timeout 20m ./...       # full race & UI test suite
```

## Acknowledgements

Special thanks to the open-source projects and authors that make Serval possible:

- **[MyGo](https://github.com/egoist/mygo)** by [@egoist](https://github.com/egoist) — the pure-Go native desktop UI framework powering Serval's GPU-rendered windows, virtual lists, terminal views, and glass vibrancy across macOS, Windows, and Linux.
- **[Netcatty](https://github.com/binaricat/Netcatty)** and **[Kero](https://github.com/egoist/kero)** for workflow and visual design inspiration (Serval is written from scratch under the MIT license without any GPL code).
- **[golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto)** and **[pkg/sftp](https://github.com/pkg/sftp)** for SSH, Argon2id, XChaCha20-Poly1305, and SFTP protocol support.
- **[JetBrains Mono](https://www.jetbrains.com/lp/mono/)** (SIL OFL 1.1) and **[Lucide Icons](https://lucide.dev/)** (ISC).

## Author & License

Created and maintained by **[jhzhang09](https://github.com/jhzhang09)** ([github.com/jhzhang09/serval](https://github.com/jhzhang09/serval)).

Licensed under the [MIT License](LICENSE).
