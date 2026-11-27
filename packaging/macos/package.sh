#!/usr/bin/env bash
# 构建 macOS 应用包与 DMG，并在提供证书时执行代码签名与公证。
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "$SCRIPT_DIR/../.." && pwd)"
cd "$ROOT_DIR"

APP_NAME="Euler"
BINARY="${BINARY:-bin/euler}"
ARCH="${ARCH:-$(uname -m)}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || printf '0.0.0')}"
OUT_DIR="${OUT_DIR:-packaging/out}"

usage() {
	cat <<'USAGE'
用法: packaging/macos/package.sh [选项]

选项:
  --binary PATH     待打包的二进制（默认 bin/euler）
  --arch ARCH       arm64 或 amd64（默认当前机器架构）
  --version VER     版本号（默认取 git describe 结果）
  --out DIR         产物目录（默认 packaging/out）
  -h, --help        显示帮助

可选环境变量（用于代码签名与公证）:
  MACOS_SIGN_IDENTITY      Keychain 中已有的证书名称，与 MACOS_SIGN_P12 二选一
  MACOS_SIGN_P12           .p12 证书的 base64 内容（CI 使用）
  MACOS_SIGN_PASSWORD      .p12 证书密码
  MACOS_NOTARY_KEY         App Store Connect API Key（.p8 文件路径或 base64 内容）
  MACOS_NOTARY_KEY_ID      API Key ID
  MACOS_NOTARY_ISSUER_ID   Issuer UUID
USAGE
}

while (( $# > 0 )); do
	case "$1" in
		--binary)
			BINARY="$2"
			shift 2
			;;
		--arch)
			ARCH="$2"
			shift 2
			;;
		--version)
			VERSION="$2"
			shift 2
			;;
		--out)
			OUT_DIR="$2"
			shift 2
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			printf '错误：未知参数 %s\n' "$1" >&2
			usage >&2
			exit 1
			;;
	esac
done

case "$ARCH" in
	amd64|x86_64) arch="amd64" ;;
	arm64|aarch64) arch="arm64" ;;
	*)
		printf '错误：不支持的架构 %s\n' "$ARCH" >&2
		exit 1
		;;
esac

if [[ ! -f "$BINARY" ]]; then
	printf '错误：找不到二进制 %s，请先执行 make build。\n' "$BINARY" >&2
	exit 1
fi

# 归一化版本号：去掉 v 前缀与预发布后缀，只给 CFBundle 保留点分数字。
version_core="${VERSION#v}"
version_core="${version_core%%-*}"
version_core="$(printf '%s' "$version_core" | tr -cd '0-9.')"
# 未打 tag 时 git describe 给出提交哈希，无法作为 CFBundle 版本，回退到 0.0.0。
if [[ "$version_core" != *.* && ${#version_core} -ge 7 ]]; then
	version_core="0.0.0"
fi
IFS='.' read -r major minor patch <<< "$version_core"
bundle_version="${major:-0}.${minor:-0}.${patch:-0}"

temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/euler-macos.XXXXXX")"
keychain_path=""

cleanup() {
	if [[ -n "$keychain_path" && -f "$keychain_path" ]]; then
		security delete-keychain "$keychain_path" >/dev/null 2>&1 || true
	fi
	rm -rf "$temp_dir"
}
trap cleanup EXIT

stage_dir="$OUT_DIR/dmg-$arch"
app_dir="$stage_dir/$APP_NAME.app"
rm -rf "$stage_dir"
mkdir -p "$app_dir/Contents/MacOS" "$app_dir/Contents/Resources"
install -m 0755 "$BINARY" "$app_dir/Contents/MacOS/euler"
cp packaging/macos/Info.plist "$app_dir/Contents/Info.plist"
cp packaging/assets/euler.icns "$app_dir/Contents/Resources/AppIcon.icns"
plutil -replace CFBundleShortVersionString -string "$bundle_version" \
	"$app_dir/Contents/Info.plist"
plutil -replace CFBundleVersion -string "$bundle_version" "$app_dir/Contents/Info.plist"
plutil -lint "$app_dir/Contents/Info.plist" >/dev/null

sign_identity="${MACOS_SIGN_IDENTITY:-}"
if [[ -n "${MACOS_SIGN_P12:-}" ]]; then
	if [[ -z "${MACOS_SIGN_PASSWORD:-}" ]]; then
		printf '错误：设置了 MACOS_SIGN_P12 但没有 MACOS_SIGN_PASSWORD。\n' >&2
		exit 1
	fi
	cert_path="$temp_dir/signing.p12"
	printf '%s' "$MACOS_SIGN_P12" | base64 -D > "$cert_path"
	keychain_password="$(uuidgen)"
	keychain_path="$temp_dir/euler-signing.keychain-db"
	security create-keychain -p "$keychain_password" "$keychain_path"
	security set-keychain-settings -lut 21600 "$keychain_path"
	security unlock-keychain -p "$keychain_password" "$keychain_path"
	security import "$cert_path" -P "$MACOS_SIGN_PASSWORD" -A -t cert -f pkcs12 \
		-k "$keychain_path" >/dev/null
	security set-key-partition-list -S apple-tool:,apple: \
		-k "$keychain_password" "$keychain_path" >/dev/null
	if [[ -z "$sign_identity" ]]; then
		sign_identity="$(security find-identity -v -p codesigning "$keychain_path" \
			| sed -n 's/.*"\(Developer ID Application[^"]*\)".*/\1/p' | head -n 1)"
	fi
fi

codesign_args=(--force --timestamp)
if [[ -n "$keychain_path" ]]; then
	codesign_args+=(--keychain "$keychain_path")
fi

if [[ -n "$sign_identity" ]]; then
	printf '使用证书签名：%s\n' "$sign_identity"
	codesign "${codesign_args[@]}" --options runtime --sign "$sign_identity" \
		"$app_dir/Contents/MacOS/euler"
	codesign "${codesign_args[@]}" --sign "$sign_identity" "$app_dir"
	codesign --verify --deep --strict --verbose=2 "$app_dir"
else
	printf '警告：未提供签名证书，跳过代码签名，产物会被 Gatekeeper 拦截。\n' >&2
fi

mkdir -p "$OUT_DIR"
dmg_path="$OUT_DIR/euler_${bundle_version}_macos_${arch}.dmg"
ln -s /Applications "$stage_dir/Applications"
rm -f "$dmg_path"
hdiutil create -volname "$APP_NAME $bundle_version" -srcfolder "$stage_dir" \
	-ov -format UDZO -quiet "$dmg_path"

notary_key="${MACOS_NOTARY_KEY:-}"
if [[ -n "$sign_identity" && -n "$notary_key" ]]; then
	if [[ -z "${MACOS_NOTARY_KEY_ID:-}" || -z "${MACOS_NOTARY_ISSUER_ID:-}" ]]; then
		printf '错误：公证需要同时设置 MACOS_NOTARY_KEY_ID 与 MACOS_NOTARY_ISSUER_ID。\n' >&2
		exit 1
	fi
	if [[ -f "$notary_key" ]]; then
		notary_key_path="$notary_key"
	else
		notary_key_path="$temp_dir/ApiKey.p8"
		printf '%s' "$notary_key" | base64 -D > "$notary_key_path"
	fi
	codesign "${codesign_args[@]}" --sign "$sign_identity" "$dmg_path"
	xcrun notarytool submit "$dmg_path" --key "$notary_key_path" \
		--key-id "$MACOS_NOTARY_KEY_ID" --issuer "$MACOS_NOTARY_ISSUER_ID" --wait
	xcrun stapler staple "$dmg_path"
	xcrun stapler validate "$dmg_path"
else
	printf '提示：未提供公证凭据，跳过公证。\n' >&2
fi

printf '完成：%s\n' "$dmg_path"
