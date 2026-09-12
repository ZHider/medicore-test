#!/usr/bin/env bash
#
# 用 Docker 里的 Linux Go 工具链跑数据竞争检测。
#
# 为什么需要它：`go test -race` 要求 cgo，而本机（Windows）没有 C 编译器
# （实测 `cgo: C compiler "gcc" not found`）。CI 用的是 ubuntu runner，
# 自带 gcc，所以 -race 在 CI 上是免费的 —— 但在本地就完全跑不了。
# 这个脚本把本地这一环补上，顺便让检测环境与部署环境（Linux）一致。
#
# 用法：
#   bash backend/scripts/race.sh                    # ./internal/... 全部
#   bash backend/scripts/race.sh ./internal/service/...
#   bash backend/scripts/race.sh -tags=integration ./test/integration/...
#
# 两个必须注意的点（都是实测踩出来的）：
#
#   1. golang:*-alpine 镜像**不带 gcc**，且 CGO_ENABLED=0。要先 apk add gcc musl-dev。
#   2. 容器内**连不上 proxy.golang.org**（本机网络策略所致），
#      所以必须把宿主机的模块缓存挂进去，并设 GOPROXY=off。
#      否则会看到一堆 "dial tcp ... connection refused"，看起来像代码问题，其实是网络。

set -euo pipefail

# 切到 Go module 根目录（backend/），因为 -w /app 挂的就是它。
# 挂仓库根会让 ./internal/... 这类模式找不到模块。
cd "$(dirname "$0")/.." || exit 1

GO_IMAGE="${GO_IMAGE:-golang:1.27.1-alpine}"
TARGET="${1:-./internal/...}"
shift || true

GOMODCACHE="$(go env GOMODCACHE)"
if [ -z "$GOMODCACHE" ]; then
  echo "无法确定 GOMODCACHE，请确认 go 已在 PATH 中" >&2
  exit 1
fi

# 把 Windows 路径转成 Docker 能接受的挂载形式
case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*)
    # D:\a\b  ->  /d/a/b
    host_repo=$(pwd | sed -E 's|^/([a-zA-Z])/|/\L\1/|')
    host_gomod=$(echo "$GOMODCACHE" | sed -E 's|\\|/|g; s|^([a-zA-Z]):|/\L\1|')
    ;;
  *)
    host_repo=$(pwd)
    host_gomod="$GOMODCACHE"
    ;;
esac

echo "镜像:        $GO_IMAGE"
echo "代码:        $host_repo -> /app"
echo "模块缓存:    $host_gomod -> /go/pkg/mod"
echo "目标:        $TARGET $*"
echo

# MSYS_NO_PATHCONV=1 是必须的：Git Bash 会把 `-w /app` 这类参数"翻译"成
# Windows 路径（变成 C:/Program Files/Git/app），docker 直接报 working directory 无效。
# 这个变量只关闭参数改写，不影响上面手工换算好的 -v 挂载路径。
export MSYS_NO_PATHCONV=1

docker run --rm \
  -v "${host_repo}:/app" \
  -v "${host_gomod}:/go/pkg/mod" \
  -v medicore-gocache:/root/.cache/go-build \
  -w /app \
  -e GOPROXY=off \
  -e GOFLAGS=-mod=mod \
  -e GOTELEMETRY=off \
  -e CGO_ENABLED=1 \
  "$GO_IMAGE" \
  sh -c "apk add --no-cache gcc musl-dev >/dev/null 2>&1 && go test -race -count=1 $TARGET $*"
