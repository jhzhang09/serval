<div align="center">

<img src="resources/icon.png" width="96" alt="Serval icon">

# Serval

**A fast, lightweight, native cross-platform SSH and SFTP workbench written in Go.**

Multi-tab and split-pane terminals, multi-hop jump hosts, SOCKS5 & HTTP proxies, local/remote/dynamic port forwarding, a resizable dual-pane SFTP drawer, native in-terminal `rz` / `sz` (ZMODEM) transfers, RFC 6238 TOTP autofill, multi-pane broadcast input, and parameterized snippets — all backed by an end-to-end encrypted vault with conflict-free sync over WebDAV or shared folders.

[![Checks](https://github.com/jhzhang09/serval/actions/workflows/check.yml/badge.svg)](https://github.com/jhzhang09/serval/actions/workflows/check.yml)
[![Release](https://img.shields.io/github/v/release/jhzhang09/serval?include_prereleases&sort=semver)](https://github.com/jhzhang09/serval/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/jhzhang09/serval)](go.mod)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
![Platforms](https://img.shields.io/badge/platform-macOS%20%7C%20Windows%20%7C%20Linux-lightgrey)
[![Listed on Awesome MyGo](https://awesomemygo.dev/badge.svg)](https://awesomemygo.dev/apps/serval)

[简体中文](README.md) · **English**

</div>

![Serval Workspace](docs/screenshots/welcome.png)

## Why Serval

- **True native desktop UI, zero web view.** Built with [MyGo](https://github.com/egoist/mygo): pure Go, GPU-rendered native controls, no Electron, no WebView, no Node.js, and no external runtime dependencies. Ships as a ~5 MB installer and idles at ~100 MB RAM across multiple live sessions with near-0% CPU.
- **End-to-end encrypted vault with conflict-free sync.** Every host, private key, password, passphrase, TOTP secret, proxy, port-forwarding rule, and snippet is encrypted per record with **XChaCha20-Poly1305** under a vault key wrapped by **Argon2id** (even hostnames and IP addresses are never stored in plaintext on disk). Built-in Hybrid Logical Clock (HLC) per-record merging syncs seamlessly across devices via **WebDAV** or a **local sync folder** (iCloud Drive, Dropbox, Syncthing, etc.).
- **Cool glass aesthetic & unique host patterns.** Translucent native sidebar and titlebar vibrancy (including macOS Liquid Glass), deterministic geometric host badges, explicit red borders and confirmation guards for **Production** hosts, and a subtle dim veil on inactive split panes.
- **Jitter-free terminal and file workflow.** Opening or closing the sidebar or dual-pane SFTP drawer sets geometry in a single step — never triggering dozens of `SIGWINCH` reflows on the remote server. The resizable dual-pane SFTP drawer tracks the terminal's current working directory (`OSC 7`), supports drag-and-drop file/folder transfers, inline remote UTF-8 text editing, and native in-terminal `rz` / `sz` (ZMODEM).
- **Strict host-key verification.** Unknown host fingerprints require explicit user confirmation (and are stored encrypted in the vault so trusted fingerprints sync across devices, while `~/.ssh/known_hosts` is consulted read-only). Changed host keys are always rejected without an insecure "continue anyway" bypass.

## Features

| Area | Capabilities |
|---|---|
| **Encrypted Vault & Sync** | Per-record XChaCha20-Poly1305 encryption with Argon2id key derivation; optional OS keychain integration (macOS Keychain, Windows Credential Manager, Linux Secret Service) to remember the vault key locally; idle auto-lock and manual lock; conflict-free per-record sync via WebDAV or local sync folder. |
| **Hosts & Command Palette** | Add, edit, duplicate, group, and filter hosts by search or multi-tags (`#tag`); async host connectivity & RTT latency probe; right-click split open, direct SFTP, SSH command copy, and public-key deploy; mark hosts as **Production** (red border badge + broadcast safety confirmation); renaming a host automatically updates jump-host chains; quick-connect to `user@host[:port]` from the Command Palette (`⌘K`). |
| **Authentication, MFA & Keys** | Auto (private key → `ssh-agent` → password), Password-only, or Key-only modes; built-in Key Manager to import/generate **Ed25519** / **RSA 4096** keys, import keys directly inside the host form, and idempotently deploy public keys (`ssh-copy-id`) to `~/.ssh/authorized_keys`; `keyboard-interactive` (PAM / OTP) + **RFC 6238 TOTP**; optional per-host `ssh-agent` forwarding and automatic logon scripts. |
| **Multi-Hop Jump Hosts & Proxies** | Chain up to **8 jump hosts** with rich target previews & automatic cycle detection, plus per-hop fingerprint & credential prompts; manage **SOCKS5** and **HTTP** proxies (with encrypted proxy passwords) reusable across hosts. |
| **Port Forwarding** | Manage **Local**, **Remote**, and **Dynamic (SOCKS5)** port forwards; bind rules to a host for automatic start/stop with sessions, or toggle them manually with live listener status and byte counters. |
| **Tabs & Split Panes** | Mix SSH, Telnet, Serial, and **Local Shell** tabs with drag/keyboard tab reordering; split panes horizontally or vertically; in-terminal search (`⌘F`); exponential-backoff auto-reconnect; session & split-layout restore on launch; instant font, cursor, copy-on-select, right-click behavior, scrollback, and light/dark terminal themes. |
| **Broadcast Input & Snippets** | **Broadcast Input** mirrors keystrokes from the focused pane to selected panes across tabs (with mandatory confirmation before including Production panes); **Snippets** support `{{variable}}` placeholders prompted before sending and confirmation before multi-line execution. |
| **Dual-Pane SFTP Drawer** | Toggle with `⌘J`: side-by-side Local and Remote panes with click-to-edit path bars, saved remote directory bookmarks, draggable drawer width, and internal split ratio; drag files or folders between panes or from Finder/Explorer to upload/download; transfer queue with progress/cancellation; mkdir, rename, chmod, delete, copy path, and conflict-checked **remote UTF-8 text editing**. |
| **ZMODEM (`rz` / `sz`) & Logs** | Clean-room pure-Go ZMODEM implementation: run `rz` or `sz` inside any remote shell to open native file/folder pickers with live progress and cancellation; optional plain-text remote session logging. |
| **AI Agent** | Toggle docked right-hand AI Agent (`⌘I`) with multi-profile switching (DeepSeek, OpenAI, Qwen, Kimi, Ollama, or any OpenAI-compatible API) and one-click pane error analysis (`⌘E`); SSE streaming, collapsible thinking blocks & tool cards, multi-turn `ssh_exec` / SFTP / terminal inspection, 3 permission modes (Auto with dangerous-command guard / Confirm / Observer), native Markdown tables, `@` mentions & `/` slash commands. |

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
# or run from the archive without installing:
tar xzf serval-<version>-linux-amd64.tar.gz && ./serval
```
</details>

### Build from source

Requires Go 1.27.3 or newer.

```bash
git clone https://github.com/jhzhang09/serval.git
cd serval
go tool mygo dev      # run in development mode
go test ./...         # run test suite
go tool mygo build    # package native desktop app into build/ (.app and .dmg on macOS)
go run ./cmd/genicon  # regenerate application icon
```

## Keyboard Shortcuts

macOS uses `⌘`; Windows and Linux use `Ctrl+Shift` so terminal control keys (`Ctrl+D`, `Ctrl+W`, `Ctrl+K`) remain untouched:

| Action | macOS | Windows / Linux |
|---|---|---|
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

## Documentation

| Document | Description |
|---|---|
| [AGENTS.md](AGENTS.md) | Engineering rules, commands, and testing requirements |
| [docs/architecture.md](docs/architecture.md) | Layering, threading model, data flow, and extension points |
| [docs/storage.md](docs/storage.md) | Encrypted vault format, HLC versioning, and merge specification |
| [docs/roadmap.md](docs/roadmap.md) | Feature roadmap and acceptance criteria |
| [docs/releasing.md](docs/releasing.md) | GitHub Actions release pipeline and Homebrew Cask workflow |
| [docs/review-checklist.md](docs/review-checklist.md) | Pre-delivery verification checklist |

## Acknowledgements

- **[MyGo](https://github.com/egoist/mygo)** by [@egoist](https://github.com/egoist) — the pure-Go native desktop UI framework powering Serval's GPU-rendered windows, virtual lists, terminal views, and glass vibrancy across macOS, Windows, and Linux.
- **[Netcatty](https://github.com/binaricat/Netcatty)** and **[Kero](https://github.com/egoist/kero)** for workflow and visual design inspiration (Serval is written from scratch under the MIT license without any GPL code).
- **[golang.org/x/crypto](https://pkg.go.dev/golang.org/x/crypto)** and **[pkg/sftp](https://github.com/pkg/sftp)** for SSH, Argon2id, XChaCha20-Poly1305, and SFTP protocol support.
- **[JetBrains Mono](https://www.jetbrains.com/lp/mono/)** (SIL OFL 1.1) and **[Lucide Icons](https://lucide.dev/)** (ISC).

## Author & License

Created and maintained by **[jhzhang09](https://github.com/jhzhang09)** ([github.com/jhzhang09/serval](https://github.com/jhzhang09/serval)).

Licensed under the [MIT License](LICENSE).
