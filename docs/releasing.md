# 发版流程

适用于维护者。

## 产物与流水线

推送 `v<主>.<次>.<修订>` 标签后，[release.yml](../.github/workflows/release.yml) 依次执行：

| 任务 | Runner | 产物 |
|---|---|---|
| Verify | ubuntu | `gofmt`、`go vet`、`go tool mygo vet` 与核心包单元测试，失败则不构建 |
| App (macOS) | macos | `go tool mygo build -platform darwin/universal`：Universal `.dmg`（临时签名） |
| App (Linux) | ubuntu | amd64／arm64 的 `.deb` 与 `.tar.gz` |
| App (Windows) | windows | amd64／arm64 的 NSIS 安装程序与免安装 `.zip` |
| Publish release | ubuntu | 汇总产物，生成 `SHA256SUMS.txt` 与构建来源证明，`gh release create` 发布；正式版随后更新 Homebrew tap |

Release 正文由 [.github/release-notes.md](../.github/release-notes.md)（下载说明、签名提示）加上 GitHub 自动生成的变更说明组成，分类规则在 [.github/release.yml](../.github/release.yml)。

## Homebrew tap

正式版发布后，[scripts/update-homebrew-tap.sh](../scripts/update-homebrew-tap.sh) 按 `SHA256SUMS.txt` 生成 `Casks/serval.rb`，提交到 [jhzhang09/homebrew-tap](https://github.com/jhzhang09/homebrew-tap)：

| 文件 | 安装命令 | 内容 |
|---|---|---|
| `Casks/serval.rb` | `brew install --cask jhzhang09/tap/serval` | Universal dmg 中的 `Serval.app`，把 `serval` 命令链接到应用内可执行文件，并自动移除隔离属性；要求 macOS 13+ |

- CI 自动更新需要仓库密钥 `TAP_GITHUB_TOKEN`（授权 `jhzhang09/homebrew-tap` Contents 读写）；若未配置该密钥，可在 Release 发布后于本地运行：

  ```bash
  gh release download v0.1.0 -p SHA256SUMS.txt -D /tmp/serval-release
  scripts/update-homebrew-tap.sh v0.1.0 /tmp/serval-release/SHA256SUMS.txt
  ```

- 本地预览生成的 Cask：

  ```bash
  TAP_DRY_RUN=1 TAP_OUT=/tmp/serval-tap scripts/update-homebrew-tap.sh v0.1.0 /tmp/serval-release/SHA256SUMS.txt
  ```

## 发版步骤

1. 确认 `main` 上的检查全部通过。
2. 更新 [CHANGELOG.md](../CHANGELOG.md) 与 `mygo.json` 中的版本号。
3. 打标签并推送：

   ```bash
   git tag -a v0.1.0 -m "Serval v0.1.0"
   git push origin main
   git push origin v0.1.0
   ```
