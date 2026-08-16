APP_NAME ?= Avebi Mobile Player
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
else ifeq ($(ANDROID_TARGET),android/amd64)
  ANDROID_ABI := x86_64
else
  $(error ANDROID_TARGET must be android/arm64 or android/amd64)
endif

FFMPEG_ANDROID_LIB_DIR ?=
JNI_LIB_DIR := $(ANDROID_DIR)/app/src/main/jniLibs/$(ANDROID_ABI)

configure_android_floor: generate
	$(call LOG,Setting Android minimum SDK to API $(ANDROID_MIN_SDK))
	$(Q)perl -pi -e 's/^([[:space:]]*)minSdk[[:space:]]+[0-9]+/$$1minSdk $(ANDROID_MIN_SDK)/' \
		"$(ANDROID_DIR)/app/build.gradle"

package_ffmpeg: generate
	$(call LOG,Packaging FFmpeg shared libraries for $(ANDROID_ABI))
	$(Q)test -n "$(FFMPEG_ANDROID_LIB_DIR)" || { \
		echo "FFMPEG_ANDROID_LIB_DIR must point to the FFmpeg libraries for $(ANDROID_ABI)"; \
		exit 1; \
	}
	$(Q)test -f "$(FFMPEG_ANDROID_LIB_DIR)/libavcodec.so" || { \
		echo "libavcodec.so not found in $(FFMPEG_ANDROID_LIB_DIR)"; \
		exit 1; \
	}
	$(Q)mkdir -p "$(JNI_LIB_DIR)"
	$(Q)install -m 755 "$(FFMPEG_ANDROID_LIB_DIR)"/*.so "$(JNI_LIB_DIR)/"

apk: configure_android_floor compile package_ffmpeg
	$(call LOG,Building debug APK with FFmpeg for $(ANDROID_ABI))
	$(call GRADLE_RUN,assembleDebug)
	@echo -n "    Output File: "
	@realpath -m --relative-to="$(ROOT_DIR)" "$(APK_DEBUG)"

.PHONY: configure_android_floor package_ffmpeg apk
