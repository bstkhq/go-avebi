#!/usr/bin/env bash

set -euo pipefail

readonly android_arch="${1:-}"
readonly android_api="${ANDROID_API:-33}"
readonly ndk_version="${ANDROID_NDK_VERSION:-26.3.11579264}"
readonly sdk_root="${ANDROID_SDK_ROOT:-${ANDROID_HOME:-}}"
readonly project_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if [[ -z "${sdk_root}" ]]; then
	echo "ANDROID_SDK_ROOT or ANDROID_HOME must point to an Android SDK" >&2
	exit 2
fi

case "${android_arch}" in
	arm64)
		readonly goarch="arm64"
		readonly compiler_prefix="aarch64-linux-android"
		;;
	amd64)
		readonly goarch="amd64"
		readonly compiler_prefix="x86_64-linux-android"
		;;
	*)
		echo "usage: $0 {arm64|amd64}" >&2
		exit 2
		;;
esac

case "$(uname -s)" in
	Linux)
		readonly ndk_host="linux-x86_64"
		;;
	Darwin)
		readonly ndk_host="darwin-x86_64"
		;;
	*)
		echo "unsupported NDK host: $(uname -s)" >&2
		exit 2
		;;
esac

readonly toolchain="${sdk_root}/ndk/${ndk_version}/toolchains/llvm/prebuilt/${ndk_host}/bin"
readonly cc="${toolchain}/${compiler_prefix}${android_api}-clang"
readonly cxx="${toolchain}/${compiler_prefix}${android_api}-clang++"

if [[ ! -x "${cc}" || ! -x "${cxx}" ]]; then
	echo "Android compiler not found in: ${toolchain}" >&2
	exit 2
fi

export GOOS=android
export GOARCH="${goarch}"
export CGO_ENABLED=1
export CC="${cc}"
export CXX="${cxx}"
# Oto v3.4.0's vendored Oboe header uses memset without including <cstring>.
export CGO_CXXFLAGS="${CGO_CXXFLAGS:+${CGO_CXXFLAGS} }-include cstring"

cd "${project_root}"
echo "Verifying avebi for Android ${GOARCH}, API ${android_api}, NDK ${ndk_version}"

readonly root_go_files="$(go list -f '{{range .GoFiles}}{{println .}}{{end}}' .)"
for required_file in audio_context_ffi.go backend_ffi.go controller_ffi.go player_ffi.go; do
	if ! grep -Fxq "${required_file}" <<<"${root_go_files}"; then
		echo "Android source set is missing ${required_file}" >&2
		exit 1
	fi
done

go test -exec /bin/true -run '^$' .
