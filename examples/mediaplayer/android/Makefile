APP_NAME ?= Avebi Media Player
APP_ID ?= dev.bstk.avebi.mobileplayer
GO_PKG ?= mobile
GO_SRC ?= $(abspath .)
ANDROID_TARGET ?= android/arm64
ANDROID_MIN_SDK ?= 33

BUILDER_DIR ?= .build/apk-ebiten-builder
BUILDER_REPO ?= https://github.com/bstkhq/apk-ebiten-builder
BUILDER_REF ?= main
ROOT_DIR ?= $(abspath $(BUILDER_DIR))
INCLUDE_PATH ?= $(BUILDER_DIR)/Include.mk

# Oto v3.4.0's vendored Oboe header uses memset without including <cstring>.
CGO_CXXFLAGS := $(strip $(CGO_CXXFLAGS) -include cstring)
# gomobile builds from generated sources that do not have meaningful VCS data.
GOFLAGS := $(strip $(GOFLAGS) -buildvcs=false)

export APP_ID CGO_CXXFLAGS GOFLAGS

$(INCLUDE_PATH):
	git clone --no-checkout $(BUILDER_REPO) $(BUILDER_DIR)
	git -C $(BUILDER_DIR) checkout --detach $(BUILDER_REF)

include $(INCLUDE_PATH)

ifeq ($(ANDROID_TARGET),android/arm64)
  ANDROID_ABI := arm64-v8a
  FFMPEG_GO_ARCH := arm64
else ifeq ($(ANDROID_TARGET),android/amd64)
  ANDROID_ABI := x86_64
  FFMPEG_GO_ARCH := amd64
else
  $(error ANDROID_TARGET must be android/arm64 or android/amd64)
endif

FFMPEG_FFI_DIR ?= .build/go-ffmpeg-ffi
FFMPEG_FFI_REPO ?= https://github.com/bstkhq/go-ffmpeg-ffi.git
FFMPEG_FFI_REF ?= v1.1.0
ifeq ($(origin FFMPEG_ANDROID_LIB_DIR),undefined)
  FFMPEG_ANDROID_LIB_DIR := $(FFMPEG_FFI_DIR)/.build/ffmpeg-android/install/$(ANDROID_ABI)/lib
  FFMPEG_BUILD_DEPENDENCY := $(FFMPEG_ANDROID_LIB_DIR)/libavcodec.so
endif
FFMPEG_BUILD_SCRIPT := $(FFMPEG_FFI_DIR)/scripts/build-ffmpeg-android.sh
FFMPEG_LIBRARIES := avutil avcodec avformat swscale swresample
JNI_LIB_DIR := $(ANDROID_DIR)/app/src/main/jniLibs/$(ANDROID_ABI)

$(FFMPEG_BUILD_SCRIPT):
	$(call LOG,Cloning go-ffmpeg-ffi $(FFMPEG_FFI_REF))
	$(Q)mkdir -p "$(dir $(FFMPEG_FFI_DIR))"
	$(Q)git clone --branch "$(FFMPEG_FFI_REF)" --depth 1 \
		"$(FFMPEG_FFI_REPO)" "$(FFMPEG_FFI_DIR)"

ifneq ($(strip $(FFMPEG_BUILD_DEPENDENCY)),)
$(FFMPEG_BUILD_DEPENDENCY): | $(FFMPEG_BUILD_SCRIPT)
	$(call LOG,Building FFmpeg for $(ANDROID_ABI))
	$(Q)ANDROID_API="$(ANDROID_MIN_SDK)" \
		"$(FFMPEG_BUILD_SCRIPT)" "$(FFMPEG_GO_ARCH)"
endif

ffmpeg: $(FFMPEG_BUILD_DEPENDENCY)
	$(Q)for library in $(FFMPEG_LIBRARIES); do \
		test -f "$(FFMPEG_ANDROID_LIB_DIR)/lib$$library.so" || { \
			echo "lib$$library.so not found in $(FFMPEG_ANDROID_LIB_DIR)"; \
			exit 1; \
		}; \
	done

configure_android_floor: generate
	$(call LOG,Setting Android minimum SDK to API $(ANDROID_MIN_SDK))
	$(Q)perl -pi -e 's/^([[:space:]]*)minSdk[[:space:]]+[0-9]+/$$1minSdk $(ANDROID_MIN_SDK)/' \
		"$(ANDROID_DIR)/app/build.gradle"

package_ffmpeg: generate ffmpeg
	$(call LOG,Packaging FFmpeg shared libraries for $(ANDROID_ABI))
	$(Q)mkdir -p "$(JNI_LIB_DIR)"
	$(Q)install -m 755 "$(FFMPEG_ANDROID_LIB_DIR)"/*.so "$(JNI_LIB_DIR)/"

apk: configure_android_floor compile package_ffmpeg
	$(call LOG,Building debug APK with FFmpeg for $(ANDROID_ABI))
	$(call GRADLE_RUN,assembleDebug)
	@echo -n "    Output File: "
	@realpath -m --relative-to="$(ROOT_DIR)" "$(APK_DEBUG)"

.PHONY: ffmpeg configure_android_floor package_ffmpeg apk
