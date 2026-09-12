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
#   GO_IMAGE=golang:1.27.1-alpine bash backend/scripts/race.sh   # 换镜像
#
# ---------------------------------------------------------------------------
# 镜像怎么选（实测数据，不是推测）
# ---------------------------------------------------------------------------
#
#   golang:1.27.1          ← 默认。Debian，**开箱就能跑 -race**
#       gcc (Debian 14.2.0) 自带，CGO_ENABLED 已经是 1，无需任何准备步骤。
#       libc 为 glibc 2.41。镜像 1.31 GB。
#
#   golang:1.27.1-alpine
#       **不带 gcc**，且 CGO_ENABLED=0 —— 必须每次 `apk add gcc musl-dev`。
#       镜像只有 381 MB，但那个 apk 步骤在临时容器里**无法缓存**，
#       每次调用都要重新解析索引 + 下载。
#
# 同一目标、两边 build cache 都已热的情况下：
#
#       golang:1.27.1          29.5 秒
#       golang:1.27.1-alpine   64.2 秒      ← 慢一倍多，全花在那条 apk 上
#
# 所以默认选 Debian。用 alpine 的**唯一**理由是"与部署镜像保持同一 libc"
# （生产用的是 golang:1.27.1-alpine 构建）：musl 与 glibc 在 DNS 解析、
# 线程栈大小等方面确有差异，做端到端验证时值得一致。
# 但本脚本跑的是单元测试（依赖全为内存 fake），这个差异无关紧要。
#
# ---------------------------------------------------------------------------
# 容器**一律不出网**（这是设计，不是变通）
# ---------------------------------------------------------------------------
#
# 依赖早已在宿主机上，所以喂给它，而不是让它去下：
# 挂载 GOMODCACHE + 设 GOPROXY=off。
#
# GOPROXY=off 让"缺依赖"变成一条**立刻失败的明确错误**
# （"go: module lookup disabled by GOPROXY=off"，实测 0.03 秒返回），
# 而不是 15 秒静默超时后报一个误导性的网络错误
# （"dial tcp ... connection refused"，看起来像代码问题）。
#
# ⛔ **不要给容器配代理。** 127.0.0.1 在容器里指向容器自己，连不到宿主机；
#    写 host.docker.internal:PORT 则等于把某个人的个人代理变成所有人与 CI 的
#    构建依赖（不可复制，且若代理计费就是在花机主的钱）。
#    同理：本文件与整个仓库都不得硬编码任何代理地址。
#
# 实测判据：容器内 DNS 能解析外网域名，但 TCP 443 连不上 —— 这是 Docker 的
# NAT/出口问题，不是代码问题，也不该由"配个代理"来解决。
#
# ---------------------------------------------------------------------------
# 另一个坑：Git Bash 的路径改写
# ---------------------------------------------------------------------------
#
#   Git Bash 会把 `-w /app` 改写成 Windows 路径（C:/Program Files/Git/app），
#   docker 直接报 working directory 无效 —— 需要 MSYS_NO_PATHCONV=1（见下）。


set -euo pipefail

# 切到 Go module 根目录（backend/），因为 -w /app 挂的就是它。
# 挂仓库根会让 ./internal/... 这类模式找不到模块。
cd "$(dirname "$0")/.." || exit 1

GO_IMAGE="${GO_IMAGE:-golang:1.27.1}"
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

# build cache 按镜像分开：不同 libc 编译出的产物缓存键不同，
# 混在同一个卷里只会让每次切换都触发大范围重编译（白白多花几十秒）。
cache_vol="medicore-gocache-$(echo "$GO_IMAGE" | tr 'A-Z:/.' 'a-z---')"

echo "镜像:        $GO_IMAGE"
echo "代码:        $host_repo -> /app"
echo "模块缓存:    $host_gomod -> /go/pkg/mod"
echo "build cache: $cache_vol"
echo "目标:        $TARGET $*"
echo

export MSYS_NO_PATHCONV=1

docker run --rm \
  -v "${host_repo}:/app" \
  -v "${host_gomod}:/go/pkg/mod" \
  -v "${cache_vol}:/root/.cache/go-build" \
  -w /app \
  -e GOPROXY=off \
  -e GOFLAGS=-mod=mod \
  -e GOTELEMETRY=off \
  -e CGO_ENABLED=1 \
  "$GO_IMAGE" \
  sh -c '
    # 只有 alpine 系才需要补 gcc；Debian 镜像自带，这段对它是空操作。
    # 用"有没有 apk"来判断，而不是判断镜像名 —— 换个 tag 也不会失效。
    if command -v apk >/dev/null 2>&1 && ! command -v gcc >/dev/null 2>&1; then
      echo "[race.sh] alpine 镜像缺 gcc，安装 gcc musl-dev ..." >&2
      apk add --no-cache gcc musl-dev >/dev/null 2>&1
    fi
    if ! command -v gcc >/dev/null 2>&1; then
      echo "[race.sh] 镜像内没有 gcc，-race 无法工作。请改用 golang:<version>（Debian）" >&2
      exit 1
    fi
    exec go test -race -count=1 "$@"
  ' sh "$TARGET" "$@"
