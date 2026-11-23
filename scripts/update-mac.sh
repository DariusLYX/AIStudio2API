#!/usr/bin/env bash
# ==============================================================================
# AIStudio2API macOS 一键升级脚本（支持云端预编译极速拉取 & 本地源码全自动编译）
#
# 用法：
#   1. 极速升级（默认推荐，3 秒完成，直接从 DariusLYX/AIStudio2API Releases 拉取云端编译好的 Mac 二进制）：
#      ./scripts/update-mac.sh
#
#   2. 源码编译升级（当 Mac 本地有未推送的临时代码改动时使用，自动对齐 SpiderMonkey/libwebp 静态库 + 打包前端 + CGO 编译）：
#      ./scripts/update-mac.sh --source [--no-pull] [--skip-web]
#
# 可选环境变量：
#   BIN_PATH=/path/to/aistudio2api   指定输出/替换的可执行文件路径（默认自动探测 ./aistudio2api 或 ./bin/aistudio2api）
#   RESTART_CMD="..."                升级完成后自动执行的重启命令
# ==============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

REPO_SLUG="DariusLYX/AIStudio2API"
MODE="binary"
DO_PULL=1
SKIP_WEB=0
TARGET_TAG=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --source|-s)
      MODE="source"
      shift
      ;;
    --binary|-b)
      MODE="binary"
      shift
      ;;
    --no-pull)
      DO_PULL=0
      shift
      ;;
    --skip-web)
      SKIP_WEB=1
      shift
      ;;
    --tag)
      TARGET_TAG="${2:-}"
      shift 2
      ;;
    -h|--help)
      sed -n '2,15p' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "[ERROR] 未知参数: $1" >&2
      exit 1
      ;;
  esac
done

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
  arm64|aarch64) GOARCH="arm64" ;;
  x86_64|amd64)  GOARCH="amd64" ;;
  *)
    echo "[ERROR] 不支持的 macOS 架构: $ARCH_RAW" >&2
    exit 1
    ;;
esac
TARGET="darwin-${GOARCH}"

# 自动探测目标可执行文件位置
if [[ -z "${BIN_PATH:-}" ]]; then
  if [[ -f "$REPO_ROOT/aistudio2api" ]]; then
    BIN_PATH="$REPO_ROOT/aistudio2api"
  elif [[ -f "$REPO_ROOT/bin/aistudio2api" ]]; then
    BIN_PATH="$REPO_ROOT/bin/aistudio2api"
  else
    BIN_PATH="$REPO_ROOT/aistudio2api"
  fi
fi

if [[ "$DO_PULL" -eq 1 ]] && git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "==> [1/3] 同步 Git 仓库 (origin/main)..."
  git fetch origin main --tags
  if [[ -z "$(git status --porcelain)" ]]; then
    git merge --ff-only origin/main
  else
    echo "    [提示] 本地工作区存在未提交改动，跳过自动 git merge（保留本地改动）"
  fi
fi

install_from_release() {
  local api_url="https://api.github.com/repos/${REPO_SLUG}/releases/latest"
  if [[ -n "$TARGET_TAG" ]]; then
    api_url="https://api.github.com/repos/${REPO_SLUG}/releases/tags/${TARGET_TAG}"
  fi

  echo "==> [2/3] 查询 ${REPO_SLUG} 云端预编译发行版 (${TARGET})..."
  local release_json
  release_json="$(curl -fsSL -H "Accept: application/vnd.github+json" "$api_url")"

  local tag
  tag="$(printf '%s' "$release_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("tag_name",""))')"
  if [[ -z "$tag" ]]; then
    echo "[ERROR] 未找到可用的 GitHub Release，请改用 --source 源码编译或先推送版本 Tag" >&2
    exit 1
  fi

  local asset_name="aistudio2api-${tag}-${TARGET}.tar.gz"
  local asset_info
  asset_info="$(printf '%s' "$release_json" | python3 -c '
import json, sys
name = sys.argv[1]
data = json.load(sys.stdin)
for a in data.get("assets", []):
    if a.get("name") == name:
        print(a.get("browser_download_url", ""))
        print(a.get("digest", ""))
        break
' "$asset_name")"

  local download_url
  download_url="$(printf '%s\n' "$asset_info" | sed -n '1p')"
  local digest
  digest="$(printf '%s\n' "$asset_info" | sed -n '2p')"

  if [[ -z "$download_url" ]]; then
    echo "[ERROR] 发行版 ${tag} 中未找到 ${asset_name}" >&2
    exit 1
  fi

  local tmp_dir
  tmp_dir="$(mktemp -d)"
  trap 'rm -rf "$tmp_dir"' EXIT

  echo "==> 下载 ${asset_name} (${tag})..."
  curl -fL --progress-bar "$download_url" -o "$tmp_dir/$asset_name"

  if [[ "$digest" == sha256:* ]]; then
    local expected="${digest#sha256:}"
    local actual
    actual="$(shasum -a 256 "$tmp_dir/$asset_name" | awk '{print $1}')"
    if [[ "$actual" != "$expected" ]]; then
      echo "[ERROR] SHA-256 校验失败: 期望 $expected, 实际 $actual" >&2
      exit 1
    fi
    echo "    SHA-256 校验通过 ($actual)"
  fi

  tar -xzf "$tmp_dir/$asset_name" -C "$tmp_dir"
  local extracted="$tmp_dir/aistudio2api-${tag}-${TARGET}/aistudio2api"
  if [[ ! -f "$extracted" ]]; then
    extracted="$(find "$tmp_dir" -type f -name aistudio2api | head -n 1)"
  fi

  mkdir -p "$(dirname "$BIN_PATH")"
  chmod +x "$extracted"
  mv -f "$extracted" "$BIN_PATH"
  xattr -d com.apple.quarantine "$BIN_PATH" 2>/dev/null || true
  echo "==> [3/3] 升级完成: $BIN_PATH (版本 ${tag})"
}

build_from_source() {
  local action_file="$REPO_ROOT/.github/actions/static-libraries/action.yml"
  local sm_ver webp_ver
  sm_ver="$(awk -F': ' '/SPIDERMONKEY:/ {print $2}' "$action_file" | tr -d ' "\r')"
  webp_ver="$(awk -F': ' '/LIBWEBP:/ {print $2}' "$action_file" | tr -d ' "\r')"

  local cache_root="${HOME}/.cache/aistudio2api-static-libs/${TARGET}"
  local sm_dir="${cache_root}/spidermonkey-${sm_ver}"
  local webp_dir="${cache_root}/libwebp-${webp_ver}"

  echo "==> [2/4] 检查 C++ 静态库缓存 (${TARGET}: SpiderMonkey=${sm_ver}, libwebp=${webp_ver})..."
  if [[ ! -d "$sm_dir/lib" ]]; then
    mkdir -p "$sm_dir"
    echo "    下载 spidermonkey-${sm_ver}-${TARGET}.tar.gz..."
    curl -fsSL "https://github.com/Mag1cFall/firefox/releases/download/${sm_ver}/spidermonkey-${sm_ver}-${TARGET}.tar.gz" | tar -xz -C "$sm_dir"
  fi
  if [[ ! -d "$webp_dir/lib" ]]; then
    mkdir -p "$webp_dir"
    echo "    下载 libwebp-${webp_ver}-${TARGET}.tar.gz..."
    curl -fsSL "https://github.com/Mag1cFall/libwebp/releases/download/${webp_ver}/libwebp-${webp_ver}-${TARGET}.tar.gz" | tar -xz -C "$webp_dir"
  fi

  if [[ "$SKIP_WEB" -eq 0 ]]; then
    echo "==> [3/4] 构建 Vue 管理前端 -> internal/webui/dist..."
    (cd "$REPO_ROOT/web" && npm ci && npm run build)
  else
    echo "==> [3/4] 跳过前端构建 (--skip-web)"
  fi

  local ver_tag
  ver_tag="$(git describe --tags --abbrev=0 2>/dev/null || echo "v0.3.14")"
  echo "==> [4/4] 编译 macOS (${GOARCH}) 可执行文件 (version=${ver_tag})..."
  mkdir -p "$(dirname "$BIN_PATH")"
  CGO_ENABLED=1 \
  GOOS=darwin \
  GOARCH="$GOARCH" \
  MACOSX_DEPLOYMENT_TARGET=13.0 \
  CGO_CPPFLAGS="-mmacosx-version-min=13.0 -I${sm_dir}/include -I${webp_dir}/include" \
  CGO_LDFLAGS="-mmacosx-version-min=13.0 -L${sm_dir}/lib -L${webp_dir}/lib" \
  go build -mod=readonly -trimpath \
    -ldflags="-s -w -extldflags -Wl,-dead_strip -X github.com/Mag1cFall/AIStudio2API/internal/app.version=${ver_tag}" \
    -o "$BIN_PATH.tmp" ./cmd/aistudio2api

  mv -f "$BIN_PATH.tmp" "$BIN_PATH"
  echo "==> 源码编译完成: $BIN_PATH (版本 ${ver_tag})"
}

if [[ "$MODE" == "source" ]]; then
  build_from_source
else
  install_from_release
fi

if [[ -n "${RESTART_CMD:-}" ]]; then
  echo "==> 执行重启命令: $RESTART_CMD"
  eval "$RESTART_CMD"
fi
