## Downloads · 下载

| Platform · 平台 | Desktop App · 桌面应用 |
|---|---|
| macOS 13+ (Apple Silicon & Intel) | `serval-<version>.dmg` |
| Windows 10 / 11 | Installer · 安装版<br>`serval-setup-<version>-<arch>.exe`<br><br>Portable · 免安装版<br>`serval-<version>-windows-<arch>-portable.zip` |
| Linux | Debian / Ubuntu<br>`serval_<version>_<arch>.deb`<br><br>Other distributions · 其他发行版<br>`serval-<version>-linux-<arch>.tar.gz` |

Homebrew (macOS): `brew install --cask jhzhang09/tap/serval`

Verify downloads with `SHA256SUMS.txt`, or with the build provenance attestation: `gh attestation verify <file> --repo jhzhang09/serval`.
可用 `SHA256SUMS.txt` 或构建来源证明校验下载文件：`gh attestation verify <文件> --repo jhzhang09/serval`。

> **macOS:** the app is ad-hoc signed, not notarized. Installing via Homebrew Cask clears quarantine automatically; if downloading the `.dmg` manually, run `xattr -dr com.apple.quarantine /Applications/Serval.app` once, or choose **System Settings → Privacy & Security → Open Anyway** after the first launch attempt.
> **macOS：** 应用使用临时签名，尚未经过公证。通过 Homebrew Cask 安装会自动移除隔离属性；若手动下载 `.dmg`，请执行一次 `xattr -dr com.apple.quarantine /Applications/Serval.app`，或在首次尝试打开后到**系统设置 → 隐私与安全性**点击**仍要打开**。
>
> **Windows:** the binaries are not code-signed, so SmartScreen may warn; choose **More info → Run anyway**.
> **Windows：** 程序未做代码签名，SmartScreen 可能提示风险，请选择**更多信息 → 仍要运行**。
