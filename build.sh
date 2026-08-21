#!/usr/bin/env bash
# ARTEX 单二进制构建脚本：静态导出前端并嵌入 Go 二进制。
#
# 可选环境变量：
#   ARTEX_TARGET_OS=linux             目标系统，默认当前系统
#   ARTEX_TARGET_ARCH=amd64           目标架构，默认当前架构
#   ARTEX_BUILD_VERSION=0.3.3         构建版本，默认使用当前应用版本
#   ARTEX_OUTPUT=/path/to/artex       输出路径
#   ARTEX_SKIP_NPM_CI=1               跳过 npm ci，复用现有依赖
#   ARTEX_GOSUMDB=sum.golang.org      Go checksum database
set -euo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok() { printf '\033[32m[+]\033[0m %s\n' "$*"; }
die() { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "未检测到 Go（项目需要 Go 1.26 或更高版本）"
command -v npm >/dev/null 2>&1 || die "未检测到 npm（前端静态构建需要 Node.js/npm）"
command -v rsync >/dev/null 2>&1 || die "未检测到 rsync"

ARTEX_GOSUMDB="${ARTEX_GOSUMDB:-sum.golang.org}"
ARTEX_TARGET_OS="${ARTEX_TARGET_OS:-$(GOSUMDB="$ARTEX_GOSUMDB" go env GOOS)}"
ARTEX_TARGET_ARCH="${ARTEX_TARGET_ARCH:-$(GOSUMDB="$ARTEX_GOSUMDB" go env GOARCH)}"

ARTEX_BUILD_VERSION="${ARTEX_BUILD_VERSION:-0.3.3}"

ARTEX_BINARY_NAME="artex"
[ "$ARTEX_TARGET_OS" = "windows" ] && ARTEX_BINARY_NAME="artex.exe"
ARTEX_OUTPUT="${ARTEX_OUTPUT:-dist/artex-${ARTEX_TARGET_OS}-${ARTEX_TARGET_ARCH}/${ARTEX_BINARY_NAME}}"

info "构建前端静态资源"
if [ "${ARTEX_SKIP_NPM_CI:-0}" != "1" ]; then
  (cd web && npm ci)
fi
(cd web && npm run build:static)

info "同步前端资源到 server/webui/dist"
mkdir -p server/webui/dist
rsync -a --delete web/out/ server/webui/dist/

info "编译 ${ARTEX_TARGET_OS}/${ARTEX_TARGET_ARCH}，版本 ${ARTEX_BUILD_VERSION}"
mkdir -p "$(dirname "$ARTEX_OUTPUT")"
GOSUMDB="$ARTEX_GOSUMDB" \
CGO_ENABLED=0 \
GOOS="$ARTEX_TARGET_OS" \
GOARCH="$ARTEX_TARGET_ARCH" \
go build \
  -tags embedui \
  -trimpath \
  -ldflags "-s -w -X main.version=${ARTEX_BUILD_VERSION}" \
  -o "$ARTEX_OUTPUT" \
  ./cmd/artex

ok "编译完成：$ARTEX_OUTPUT"
if command -v file >/dev/null 2>&1; then
  file "$ARTEX_OUTPUT"
fi
