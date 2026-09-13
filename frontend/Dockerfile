# 前端镜像 = nginx + 一个**在宿主机上构建好的** dist。
#
# 为什么不在容器里构建前端（这是一个刻意的决定，不是偷懒）：
#   1. `nub` 是独立二进制（~/.nub/bin/nub-*），**不在 npm 上**，容器里拿不到；
#   2. 退而用 pnpm/npm 在容器内装依赖，等于把几百个 npm 包重新下载一遍 ——
#      容器内的 store 与宿主机的 nub store 不共享，而本机 HTTPS_PROXY 指向计费代理；
#   3. 这条路径同时会违背项目"容器一律不出网"的硬约束。
#
# 代价：构建镜像前必须先跑 `nub run frontend:build`（`nub run stack:up` 会自动做）。
# 好处：整个镜像构建零网络、零额外成本，且产物与开发时看到的完全一致。

# 构建上下文是**仓库根目录**（见 deploy/compose.yml），
# 这样才能同时引用 frontend/dist 与 deploy/nginx/default.conf。
# 根目录的 .dockerignore 用白名单把上下文压到几 MB。

FROM nginx:alpine AS runtime

# 站点配置：SPA history 回退 + /api 反代 + SSE 关闭缓冲（spec §11.2）
COPY deploy/nginx/default.conf /etc/nginx/conf.d/default.conf

# 公共安全响应头。必须单独放 /etc/nginx/snippets/ 下并由 default.conf include ——
# nginx 的 add_header 在 location 里是替换语义，见该文件头部说明。
COPY deploy/nginx/security-headers.conf /etc/nginx/snippets/security-headers.conf

# ⚠️ 若这里报 "COPY failed: ... frontend/dist: not found"，
#    说明还没构建前端产物 —— 先执行 `nub run frontend:build`。
COPY frontend/dist /usr/share/nginx/html

# nginx 官方镜像自带 /docker-entrypoint.d/ 下的模板处理与健康检查脚本，
# 这里不覆盖 ENTRYPOINT，保持官方行为（envsubst 等能力仍可用）。

EXPOSE 80
