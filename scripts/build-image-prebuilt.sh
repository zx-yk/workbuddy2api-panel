#!/usr/bin/env bash
# 本地交叉编译 → 组装最小构建上下文 → docker build。
#
# 适用：构建机没装 Go 的容器能力、或在 2G 小内存生产机上 docker build 会 OOM 的场景。
# 编译在调用方本机完成（需要 Go ≥ 1.22），容器里只做 COPY，峰值内存 < 100M。
#
#   ./scripts/build-image-prebuilt.sh                              # 默认 tag
#   ./scripts/build-image-prebuilt.sh workbuddy2api-panel:1.14.0   # 自定义 tag
#   GOOS=linux GOARCH=arm64 ./scripts/build-image-prebuilt.sh      # 交叉到 arm64（需本机 gcc 不参与，CGO 已关）
#
# 产物：本地镜像 + 可选 `--save` 导出 tar（拿不到镜像仓库时用 docker load 落地）。
set -euo pipefail

TAG="${1:-workbuddy2api-panel:1.14.0-integrated}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${ROOT}/bin/${GOOS:-linux}-${GOARCH:-amd64}"

echo "==> 交叉编译 (GOOS=${GOOS:-linux} GOARCH=${GOARCH:-amd64})"
export CGO_ENABLED=0 GOOS="${GOOS:-linux}" GOARCH="${GOARCH:-amd64}"
mkdir -p "$OUT"
# 四个二进制：主服务 + 容器内可调用的签到/登录/积分工具（与主 Dockerfile 一致）
build() { go build -trimpath -ldflags="-s -w" -o "$OUT/$2" "./cmd/$1"; echo "    $2  $(du -h "$OUT/$2" | cut -f1)"; }
build server wb2api
build signin signin_bin
build login  login
build credit credit

echo "==> 组装构建上下文"
CTX="$(mktemp -d)"
trap 'rm -rf "$CTX"' EXIT
mkdir -p "$CTX/bin" "$CTX/scripts"
cp "$OUT/wb2api" "$OUT/signin_bin" "$OUT/login" "$OUT/credit" "$CTX/bin/"
cp "$ROOT/login.sh" "$ROOT/signin.sh" "$ROOT/credit.sh" "$CTX/"
cp "$ROOT/scripts/probe_active.py" "$CTX/scripts/"
cp "$ROOT/config.example.json" "$CTX/"
cp "$ROOT/Dockerfile.prebuilt" "$CTX/Dockerfile"

echo "==> docker build -t $TAG"
docker build -t "$TAG" "$CTX"

# 可选导出：./scripts/build-image-prebuilt.sh <tag> --save  →  bin/<tag>.tar
if [[ "${2:-}" == "--save" || "${1:-}" == "--save" ]]; then
  SAFE="${TAG//[:\/]/_}"
  docker save "$TAG" | gzip -1 > "$ROOT/bin/${SAFE}.tar.gz"
  echo "==> 已导出 $ROOT/bin/${SAFE}.tar.gz（$(du -h "$ROOT/bin/${SAFE}.tar.gz" | cut -f1)）"
  echo "    落地: gunzip -c ${SAFE}.tar.gz | docker load"
fi

echo "==> 完成: $TAG"
echo "    验证二进制可执行: docker run --rm --entrypoint /bin/sh $TAG -c '/app/wb2api -h 2>&1 | head -3'"
echo "    看版本:           docker run --rm --entrypoint /bin/sh $TAG -c 'ls -l /app/wb2api'"
