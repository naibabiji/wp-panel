#!/bin/bash
# 离线签名 GitHub Release 的 sha256 文件
# 用法: ./sign-release.sh <release-tag>
# 示例: ./sign-release.sh v1.2.0-rc3
#
# 前置条件:
#   1. 本地有 release-key.pem（私钥文件，与代码中公钥配对）
#   2. 已安装 openssl 和 gh CLI
#   3. gh CLI 已认证

set -euo pipefail

TAG="${1:-}"
if [ -z "$TAG" ]; then
    echo "用法: $0 <release-tag>"
    echo "示例: $0 v1.2.0-rc3"
    exit 1
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
KEY_FILE="/e/SynologyDrive/ai/wp-panel-release-key.pem"
SHA256_FILE="/tmp/wp-panel-${TAG}.sha256"
SIG_FILE="/tmp/wp-panel.sha256.sig"

if [ ! -f "$KEY_FILE" ]; then
    echo "错误: 找不到私钥文件 $KEY_FILE"
    echo "私钥由开发者本地保管，不存储在 GitHub / CI 上"
    exit 1
fi

echo ">>> 下载 ${TAG} 的 sha256 文件..."
gh release download "$TAG" -p "wp-panel.sha256" -O "$SHA256_FILE" --clobber

echo ">>> 签名 sha256 文件..."
openssl pkeyutl -sign -inkey "$KEY_FILE" -in "$SHA256_FILE" -out "$SIG_FILE" -rawin

echo ">>> 上传签名到 ${TAG}..."
gh release upload "$TAG" "$SIG_FILE" --clobber

echo ">>> 完成: ${TAG} 已签名"
rm -f "$SHA256_FILE" "$SIG_FILE"
