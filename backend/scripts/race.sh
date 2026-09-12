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
# ⚠️ Windows 上必须用 **Git Bash** 的 bash 来跑，不能是 PATH 上那个。
#    本机 `Get-Command bash` → `C:\WINDOWS\system32\bash.exe`，那是 **WSL 启动器**，
#    而机器上没装 WSL 发行版，直接报
#    `WSL ERROR: CreateExecutableCommon: execvpe(/bin/bash) failed`。
#    可用的那个在 `C:\Program Files\Git\bin\bash.exe`。
#
# ⚠️ `nub run backend:race` **不要采信它的结果**（详见文件末尾的兜底逻辑）。
#    nub 执行 package.json 脚本时用的不是 Git Bash（实测那里 `uname -s` 返回
#    `Windows_NT`、`pwd` 返回 `D:/...`），实测该路径下 docker 产出零行、
#    1 秒返回、退出码 0 —— 35 秒的 -race 根本没跑，而调用方收到的是"成功"。
#    脚本末尾的输出兜底会把它变成 exit 1，但**结论仍以 Git Bash 直跑为准**。
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

# 把 Windows 路径统一转成 Docker 能吃的 POSIX 形式：
#   D:\a\b   ->  /d/a/b
#   D:/a/b   ->  /d/a/b
#   /d/a/b   ->  /d/a/b   （已经是了，原样）
#
# ⚠️ 这里**刻意不按 `uname -s` 分支**，尽管那样看起来更"正规"。
#
# 原来的写法是 `case "$(uname -s)" in MINGW*|MSYS*|CYGWIN*) ... *) 原样 ;;`，
# 在 `nub run backend:race` 下**静默失效**：nub 执行 package.json 脚本时用的 shell
# 报出 `uname -s = Windows_NT`（不是 MSYS 系），于是走了 `*)` 分支，
# 把 `D:/Hider/...` 与 `C:\Users\...\pkg\mod` 原样交给 docker。
# 结果 docker 退出码 0、但输出了零行、耗时 1 秒 —— 测试一次都没跑，
# 而 nub 忠实地把这个 0 传给了调用方。
# 也就是说：**"数据竞争检测通过"是假的，而且看起来跟真的一模一样。**
# （用 `bash -x` 才看出来：`+ docker run ... -v 'C:\Users\...' ...` 之后直接结束。）
#
# 转换本身是幂等的，所以无条件执行没有任何代价。
#
# 另外**刻意不用 GNU sed 的 `\L`**（`s|^([a-zA-Z]):|/\L\1|`）：它是 GNU 扩展，
# 在 nub 那个环境里被当成字面量 `L`，产出了 `/LD/Hider/...` 这种路径。
# 这里换成 `tr` + 参数展开，只小写盘符，不碰路径其余部分的大小写
# （整条 `tr 'A-Z' 'a-z'` 会破坏 `C:\Users\...` 里的目录名大小写）。
to_posix() {
  p=$(printf '%s' "$1" | tr '\\' '/')
  case "$p" in
    [A-Za-z]:/*)
      drive=$(printf '%s' "${p%%:*}" | tr '[:upper:]' '[:lower:]')
      p="/${drive}${p#*:}"
      ;;
  esac
  printf '%s' "$p"
}
host_repo=$(to_posix "$(pwd)")
host_gomod=$(to_posix "$GOMODCACHE")

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

# ---------------------------------------------------------------------------
# 预检：确认"容器真的起来了、挂载真的可见"
# ---------------------------------------------------------------------------
# 为什么非要这一步：上面那段注释讲的静默失效 —— docker 曾经**退出码 0、
# 输出零行、耗时 1 秒**，脚本于是报了成功，而测试一次都没跑。
#
# 修好某一条具体的路径不是终点：下次环境变化（换 shell、换 nub 版本、
# 换 Docker 后端）还会以别的形式踩同一个坑。**把"什么都没做"变成"大声失败"
# 才是终点**，代价是一次额外的容器启动（约 1 秒）。
#
# 判据刻意选"内容"而不是"退出码"：走不通时 docker 恰恰返回 0 且无输出，
# 只看退出码是拦不住的。go.mod 的第一行必然是 module 声明。
if ! probe=$(docker run --rm \
      -v "${host_repo}:/app" \
      -w /app \
      "$GO_IMAGE" \
      sh -c 'head -n 1 go.mod 2>/dev/null' 2>&1); then
  echo "[race.sh] 预检失败：容器无法启动（镜像 $GO_IMAGE 是否已在本机？）。输出：" >&2
  echo "$probe" >&2
  exit 1
fi

case "$probe" in
  module*)
    ;;
  *)
    {
      echo "[race.sh] 预检失败：容器内看不到 ${host_repo}:/app 下的 go.mod。"
      echo "[race.sh] 实际输出：[${probe}]"
      echo "[race.sh] 这通常意味着挂载路径没被 docker 正确解析。"
      echo "[race.sh] ⛔ 不要放过这个错误：一个静默通过、其实什么都没跑的 -race，"
      echo "[race.sh]    比跑失败危险得多 —— 它会让人以为并发问题已经排查过了。"
    } >&2
    exit 1
    ;;
esac

docker_log=$(mktemp)

run_code=0
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
  ' sh "$TARGET" "$@" 2>&1 | tee "$docker_log" || run_code=$?

# ---------------------------------------------------------------------------
# 兜底：容器"成功"但什么都没做时，必须失败
# ---------------------------------------------------------------------------
# 预检只能证明"容器起得来、挂载看得见"。它拦不住**主运行被静默吞掉** ——
# 实测就出现过：预检通过（拿到了 `module github.com/...`），
# 紧接着主 `docker run` 输出零行、耗时 1 秒、退出码 0，而 35 秒的 -race 根本
# 没跑。那种情况下调用方收到的是"通过"。
#
# 所以判据不能是退出码，而是**有没有产出**：`go test` 至少会为每个包打一行
# （`ok` 或 `?`），一行都没有就说明它压根没运行。
# 用 tee 而不是把输出收进变量：既保住 35 秒里能实时看到进度，又能事后检查。
if [ ! -s "$docker_log" ]; then
  {
    echo
    echo "[race.sh] ⛔ 容器返回码为 0，但没有任何输出 —— 测试实际上没有运行。"
    echo "[race.sh]    这**不是**通过。静默的 -race 比失败的 -race 危险得多，"
    echo "[race.sh]    因为它会让人以为并发问题已经排查过了。"
    echo "[race.sh]    排查方向：换一个真正的 bash 运行本脚本（见文件顶部用法），"
    echo "[race.sh]    或在容器能正常输出前不要采信任何 -race 结论。"
  } >&2
  rm -f "$docker_log"
  exit 1
fi

rm -f "$docker_log"
exit "$run_code"
