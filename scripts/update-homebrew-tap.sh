#!/usr/bin/env bash
# 根据发版产物的 SHA256SUMS.txt 生成 Homebrew Cask，并提交到 tap 仓库。
# 用法:
#   scripts/update-homebrew-tap.sh <version> <checksums_file>
# 环境变量:
#   TAP_GITHUB_TOKEN  推送 tap 仓库用的令牌（CI 使用）；本地不设置时通过 SSH 克隆和推送
#   TAP_REPO          目标 tap 仓库，默认 jhzhang09/homebrew-tap
#   TAP_DRY_RUN       设为任意值时只把生成的文件写到 TAP_OUT（默认 ./tap-preview），不克隆、不推送
set -euo pipefail

version_raw="${1:?usage: update-homebrew-tap.sh <version> <checksums_file>}"
checksums="${2:?usage: update-homebrew-tap.sh <version> <checksums_file>}"
version="${version_raw#v}"
tap_repo="${TAP_REPO:-jhzhang09/homebrew-tap}"

if [[ ! -f "$checksums" ]]; then
  echo "Error: checksums file not found at $checksums" >&2
  exit 1
fi

extract_sha() {
  local file="$1" sha
  sha=$(awk -v f="$file" '$2 == f || $2 == "*" f {print $1; exit}' "$checksums")
  if [[ ! "$sha" =~ ^[0-9a-f]{64}$ ]]; then
    echo "Error: no SHA-256 for $file in $checksums" >&2
    exit 1
  fi
  echo "$sha"
}

dmg_sha=$(extract_sha "serval-${version}.dmg")

generate() {
  local dir="$1"
  mkdir -p "$dir/Casks"

  cat <<EOF >"$dir/Casks/serval.rb"
cask "serval" do
  version "${version}"
  sha256 "${dmg_sha}"

  url "https://github.com/jhzhang09/serval/releases/download/v#{version}/serval-#{version}.dmg"
  name "Serval"
  desc "Lightweight native SSH, SFTP and terminal client"
  homepage "https://github.com/jhzhang09/serval"

  livecheck do
    url :url
    strategy :github_latest
  end

  depends_on macos: :ventura

  app "Serval.app"
  binary "#{appdir}/Serval.app/Contents/MacOS/Serval", target: "serval"

  postflight_steps do
    run "/usr/bin/xattr",
        args: ["-dr", "com.apple.quarantine", "{{appdir}}/Serval.app"]
  end

  zap trash: [
    "~/Library/Application Support/Serval",
    "~/Library/Preferences/io.github.jhzhang09.serval.plist",
    "~/Library/Saved Application State/io.github.jhzhang09.serval.savedState",
  ]
end
EOF

  if command -v ruby >/dev/null 2>&1; then
    ruby -c "$dir/Casks/serval.rb" >/dev/null
  fi
}

if [[ -n "${TAP_DRY_RUN:-}" ]]; then
  out="${TAP_OUT:-tap-preview}"
  generate "$out"
  echo "Generated Homebrew Cask for v${version} in $out (dry run, nothing pushed)."
  exit 0
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

if [[ -n "${TAP_GITHUB_TOKEN:-}" ]]; then
  remote="https://x-access-token:${TAP_GITHUB_TOKEN}@github.com/${tap_repo}.git"
else
  remote="git@github.com:${tap_repo}.git"
fi
git clone --quiet --depth 1 "$remote" "$work/tap"
cd "$work/tap"
if [[ -n "${TAP_GITHUB_TOKEN:-}" ]]; then
  git config user.name "github-actions[bot]"
  git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
fi

generate .
git add Casks/serval.rb
if git diff --cached --quiet; then
  echo "$tap_repo is already up to date with v${version}."
  exit 0
fi

git commit --quiet -m "serval ${version}"
git push --quiet "$remote" HEAD:main
echo "Updated $tap_repo to v${version}."
