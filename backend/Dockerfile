# syntax=docker/dockerfile:1.7
#
# 该 syntax 指令不能删：下面依赖 BuildKit 的命名构建上下文（gomodcache）
# 与 RUN --mount。用旧版构建器会直接报语法错误。
#
# 设计要点：**构建期零网络**。
# Go 依赖全部来自宿主机已下载好的 GOMODCACHE（由 compose 的
# additional_contexts 以只读方式挂进来），配合 GOPROXY=off 编译。
# 好处不只是"不出网"：依赖解析完全不经过网络，构建快且结果可复现。
#
# 若确实没有 GOMODCACHE，会自动回退到网络下载（见下面的分支），
# 但那条路径本项目从未依赖，只作为兜底。

# ============================================================================
# 构建阶段
# ============================================================================
FROM golang:1.27.1 AS build

WORKDIR /src

# 先只拷贝依赖清单：只要 go.mod/go.sum 没变，下面这层就能命中缓存。
COPY go.mod go.sum ./

# 依赖准备。
# 判据用**内容**（cache/download 非空）而不是目录是否存在 —— 目录存在但为空
# 是很容易出现的情况（空目录也会挂成功），那时 GOPROXY=off 会立刻报
# "module lookup disabled"，报错信息指向 GOPROXY 而不是真实原因。
RUN --mount=type=bind,from=gomodcache,source=.,target=/gomodcache,ro \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ -n "$(ls -A /gomodcache/cache/download 2>/dev/null)" ]; then \
        echo "==> 使用宿主机 GOMODCACHE 离线准备依赖"; \
        GOMODCACHE=/gomodcache GOFLAGS=-mod=mod GOPROXY=off go mod download; \
    else \
        echo "==> 未提供 GOMODCACHE，回退到网络下载"; \
        go mod download; \
    fi

COPY . .

# CGO_ENABLED=0：本项目没有 cgo 依赖（MySQL/Redis/bcrypt 全是纯 Go 实现），
# 静态链接后运行镜像不需要 glibc/musl 兼容层。
# -trimpath 去掉构建机的绝对路径，避免把本机目录结构带进产物。
# 同时产出 server 与 seed 两个二进制：seed 是灌演示数据用的，
# 在容器编排里由 compose 的 tools profile 一次性执行。
RUN --mount=type=bind,from=gomodcache,source=.,target=/gomodcache,ro \
    --mount=type=cache,target=/root/.cache/go-build \
    if [ -n "$(ls -A /gomodcache/cache/download 2>/dev/null)" ]; then \
        export GOMODCACHE=/gomodcache GOFLAGS=-mod=mod GOPROXY=off; \
    fi; \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/seed ./cmd/seed

# ============================================================================
# 运行阶段
# ============================================================================
FROM alpine:latest AS runtime

# 只装两个包，都很小（合计约 2MB），但各自解决一个真实问题：
#   tzdata          —— 容器默认是 UTC。本项目 sched_date 全部按**本地时区**计算
#                      （seed 的"今天起 7 天"、DSN 的 loc=Local），
#                      时区不对会让排班日期整体偏移一天。
#   ca-certificates —— 让将来接真实 LLM 上游时 HTTPS 可用。
# 注意：这里需要构建期网络拉取 alpine 的包。若要在完全离线的环境构建，
# 请预先把这两个包烘进自建基础镜像，或改用带 tzdata 的发行版基镜像。
RUN apk add --no-cache tzdata ca-certificates \
    && addgroup -S medicore \
    && adduser -S -G medicore -H -s /sbin/nologin medicore

ENV TZ=Asia/Shanghai

WORKDIR /app

COPY --from=build /out/server /app/server
COPY --from=build /out/seed /app/seed

# 运行镜像里必须带兜底配置：main.go 默认读 configs/config.yaml。
# 密钥与环境相关的项全部由环境变量覆盖（见 internal/config 的 envBindings），
# 所以这里**只**放非敏感默认值，绝不拷贝 config.local.yaml。
COPY configs/config.yaml /app/configs/config.yaml

USER medicore

EXPOSE 8080

ENTRYPOINT ["/app/server"]
