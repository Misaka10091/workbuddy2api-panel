#!/usr/bin/env bash
# ============================================================================
# workbuddy2api-panel · Docker 构建 / 运行 / 发布封装
#
# 默认命名（本脚本与 docker-compose.yml 共用同一套默认值）：
#   镜像名 IMAGE_NAME = misaka10092/workbuddy2api-panel   ← Docker Hub 命名空间/仓库
#   标签   IMAGE_TAG  = latest
#   完整引用          = misaka10092/workbuddy2api-panel:latest
#
# 注：docker 要求仓库名全小写，输入大写（Misaka10092/…）会被自动转小写并按小写构建；
#     标签（tag）保留大小写。
#
# 覆盖优先级：命令行参数 > 环境变量 > 内置默认
#   IMAGE_TAG=v1 ./scripts/docker.sh build
#   ./scripts/docker.sh build --tag=v1.2.3 --image=my/api --registry=ghcr.io
#   也可写进仓库根目录 .env（compose 会自动读取 IMAGE_NAME / IMAGE_TAG / PUID / PGID）
# ============================================================================
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]:-$0}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)

# 默认值必须与 docker-compose.yml 的 image: 默认值保持一致
DEFAULT_IMAGE_NAME="misaka10092/workbuddy2api-panel"
DEFAULT_IMAGE_TAG="latest"
DEFAULT_PORT="7863"
DEFAULT_CONTAINER_NAME="workbuddy2api"
DEFAULT_TZ="Asia/Shanghai"
DEFAULT_UID="10001"

info() { printf '\033[36m▸ %s\033[0m\n' "$*"; }
warn() { printf '\033[33m! %s\033[0m\n' "$*" >&2; }
die()  { printf '\033[31m✗ %s\033[0m\n' "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
用法：./scripts/docker.sh <命令> [参数...]

  env          打印解析后的镜像命名（不执行任何 docker 命令）
  build        构建镜像并打标签 $IMAGE_NAME:$IMAGE_TAG
  up           docker compose up -d --build（自动带入 IMAGE_NAME / IMAGE_TAG）
  run          以 compose 同款参数 docker run 起一个容器
  push         推送 [$REGISTRY/]$IMAGE_NAME:$IMAGE_TAG（需先 docker login）
  down|stop|restart|logs|ps   容器运维（compose 透传）
  compose …    任意 compose 子命令透传，例：./scripts/docker.sh compose config
  help         本帮助

通用参数：--image=NAME  --tag=TAG  --registry=HOST
默认命名：IMAGE_NAME=misaka10092/workbuddy2api-panel，IMAGE_TAG=latest
EOF
}

# 完整镜像名：允许多级命名空间（my/api、team/sub/api），只保留 docker 合法字符
sanitize_image_name() {
  printf '%s' "$1" \
    | tr '[:upper:]' '[:lower:]' \
    | sed -e 's/[^a-z0-9._\/-]\{1,\}/-/g' -e 's/\/\{1,\}/\//g' -e 's/^[-._\/]*//' -e 's/[-._\/]*$//'
}

compose_file() {
  local f
  for f in docker-compose.yml docker-compose.yaml compose.yml compose.yaml; do
    if [ -f "$REPO_ROOT/$f" ]; then printf '%s' "$REPO_ROOT/$f"; return 0; fi
  done
  return 1
}

need_docker() {
  command -v docker >/dev/null 2>&1 \
    || die "本机没有 docker 命令（脚本只负责拼装并执行 docker，构建需在装有 Docker 的机器上跑）"
}

# ---------------------------------------------------------------- 参数解析 ---
CMD=""
ARGS=()
for arg in "$@"; do
  case "$arg" in
    --tag=*|-t=*)    IMAGE_TAG=${arg#*=} ;;
    --image=*|-i=*)  IMAGE_NAME=${arg#*=} ;;
    --registry=*)    REGISTRY=${arg#*=} ;;
    -h|--help|help)  CMD=help ;;
    *)
      if [ -z "$CMD" ]; then CMD=$arg; else ARGS+=("$arg"); fi
      ;;
  esac
done
[ -n "$CMD" ] || CMD=help

IMAGE_TAG=${IMAGE_TAG:-$DEFAULT_IMAGE_TAG}
REGISTRY=${REGISTRY:-}
IMAGE_NAME=${IMAGE_NAME:-$DEFAULT_IMAGE_NAME}

RAW_IMAGE_NAME=$IMAGE_NAME
IMAGE_NAME=$(sanitize_image_name "$IMAGE_NAME")
REGISTRY=${REGISTRY%/}
if [ -n "$REGISTRY" ]; then REGISTRY=$(sanitize_image_name "$REGISTRY"); fi

[ "$RAW_IMAGE_NAME" = "$IMAGE_NAME" ] || warn "docker 仓库名必须全小写，已转为：$IMAGE_NAME"
[ -n "$IMAGE_NAME" ] || die "解析出的镜像名为空，请显式传 IMAGE_NAME=owner/repo 或 --image=owner/repo"
[ -n "$IMAGE_TAG" ]  || die "标签为空，请传 --tag=latest（默认 latest）"

# compose 通过环境变量读取同一套命名
export IMAGE_NAME IMAGE_TAG

image_ref() {
  if [ -n "$REGISTRY" ]; then printf '%s/%s' "$REGISTRY" "$IMAGE_NAME"; else printf '%s' "$IMAGE_NAME"; fi
}

full_ref() { printf '%s:%s' "$(image_ref)" "$IMAGE_TAG"; }

compose() {
  local file
  file=$(compose_file) || die "仓库根目录找不到 docker-compose.yml"
  ( cd "$REPO_ROOT" && docker compose -f "$file" "$@" )
}

# -------------------------------------------------------------------- 命令 ---
cmd_env() {
  printf 'REPO_ROOT      = %s\n' "$REPO_ROOT"
  printf 'IMAGE_NAME     = %s\n' "$IMAGE_NAME"
  printf 'IMAGE_TAG      = %s\n' "$IMAGE_TAG"
  printf 'REGISTRY       = %s\n' "${REGISTRY:-<无，按 Docker Hub / 本地镜像处理>}"
  printf 'FULL_REF       = %s\n' "$(full_ref)"
  printf 'CONTAINER_NAME = %s\n' "${CONTAINER_NAME:-$DEFAULT_CONTAINER_NAME}"
  printf 'PORT           = %s\n' "${PORT:-$DEFAULT_PORT}"
}

cmd_build() {
  need_docker
  local ref; ref=$(full_ref)
  info "docker build → $ref"
  if [ -n "${PLATFORMS:-}" ]; then
    docker build --platform "$PLATFORMS" -t "$ref" "$REPO_ROOT" ${ARGS[@]+"${ARGS[@]}"}
  else
    docker build -t "$ref" "$REPO_ROOT" ${ARGS[@]+"${ARGS[@]}"}
  fi
  info "镜像就绪：$ref"
}

cmd_up() {
  need_docker
  info "docker compose up -d --build（镜像 $(full_ref)）"
  compose up -d --build ${ARGS[@]+"${ARGS[@]}"}
}

cmd_run() {
  need_docker
  local ref name user
  ref=$(full_ref)
  name="${CONTAINER_NAME:-$DEFAULT_CONTAINER_NAME}"
  user="${PUID:-$DEFAULT_UID}:${PGID:-$DEFAULT_UID}"
  if docker ps -a --format '{{.Names}}' | grep -qx "$name"; then
    die "容器 $name 已存在：先 docker rm -f $name，或 CONTAINER_NAME=别的名字 再跑"
  fi
  [ -f "$REPO_ROOT/config.json" ] \
    || warn "缺少 $REPO_ROOT/config.json（cp config.example.json config.json），容器将用镜像内置空配置"
  mkdir -p "$REPO_ROOT/auths" "$REPO_ROOT/data"
  info "docker run → $ref（容器 $name，端口 ${PORT:-$DEFAULT_PORT}）"
  docker run -d \
    --name "$name" \
    --restart unless-stopped \
    --user "$user" \
    -e "TZ=${TZ:-$DEFAULT_TZ}" \
    -p "${PORT:-$DEFAULT_PORT}:7863" \
    -v "$REPO_ROOT/auths:/app/auths" \
    -v "$REPO_ROOT/data:/app/data" \
    -v "$REPO_ROOT/config.json:/app/config.json" \
    "$ref" ${ARGS[@]+"${ARGS[@]}"}
}

cmd_push() {
  need_docker
  if [ -z "$REGISTRY" ]; then
    case "$IMAGE_NAME" in
      */*) : ;;   # 已含 Docker Hub 命名空间，直接推
      *)   die "push 需要带命名空间的镜像名或 REGISTRY，例：--image=<owner>/workbuddy2api-panel 或 --registry=ghcr.io" ;;
    esac
  fi
  local ref; ref=$(full_ref)
  info "docker push $ref"
  docker push "$ref"
}

case "$CMD" in
  env)      cmd_env ;;
  build)    cmd_build ;;
  up)       cmd_up ;;
  run)      cmd_run ;;
  push)     cmd_push ;;
  down|stop|restart|logs|ps|pull)
            need_docker; compose "$CMD" ${ARGS[@]+"${ARGS[@]}"} ;;
  compose)  need_docker; compose ${ARGS[@]+"${ARGS[@]}"} ;;
  help)     usage; printf '\n当前解析结果：\n'; cmd_env ;;
  *)
    printf '\033[31m✗ 未知命令：%s\033[0m\n\n' "$CMD" >&2
    usage >&2
    exit 2
    ;;
esac
