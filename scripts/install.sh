#!/usr/bin/env bash
set -euo pipefail
if [[ $(id -u) != 0 ]]; then
  echo '请使用 sudo scripts/install.sh dist/baoctl' >&2
  exit 1
fi
if [[ $(uname -s) != Linux || $(uname -m) != x86_64 ]]; then
  echo '仅支持 Linux amd64' >&2
  exit 1
fi
binary=${1:-dist/baoctl}
if [[ ! -f "$binary" || ! -x "$binary" ]]; then
  echo '未找到可执行文件；请先 make build' >&2
  exit 1
fi
install -o root -g root -m 0755 "$binary" /usr/local/bin/baoctl
echo '已安装 /usr/local/bin/baoctl；运行 sudo baoctl init 接入本机。'
