# Makefile — complete Telepresence Windows installer
# Run: make bundle          → produces TelepresenceInstall.exe
#      make clean           → remove everything

SHELL:=$(shell which bash)

.PHONY: FORCE

# VERSION is the telepresence version to bundle. Use TELEPRESENCE_VERSION if set (for CI).
VERSION          ?= $(if $(TELEPRESENCE_VERSION),$(patsubst v%,%,$(TELEPRESENCE_VERSION)),2.26.0)
ARCH             ?= amd64
WIX              := wix
GO               := go
CURL             := curl
UNZIP            := unzip
SEVENZ           := 7z      # install: sudo apt install p7zip-full

WINFSP_VERSION   := 2.1.25156
SSHFS_VERSION    := 3.7.21011
WINTUN_VERSION   := 0.14.1
WINTUN_SUM       := 07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51  # update if version changes

BUILD            := ../../build-output
BINDIR           := $(BUILD)/bin
MSIDIR           := $(BUILD)/msi
ZIPDIR           := $(BUILD)/zip
EXE              := telepresence.exe
BUNDLE           := TelepresenceInstall.exe
MAIN_MSI         := MainPackage.msi
LICENSE          := License.rtf
DAEMON_EXE       := TelepresenceDaemon.exe
BUNDLE_VERSION   := $(word 1, $(subst -, ,$(TELEPRESENCE_VERSION:v%=%))).0
WIXDEFS          := -d BundleVersion=$(BUNDLE_VERSION)

.PHONY: all bundle msi deps clean

all: bundle

# Final single-click installer
bundle: $(BINDIR)/$(BUNDLE)
	@echo "SUCCESS! Installer ready: $<"
	@du -h $<

$(BINDIR)/$(BUNDLE): Telepresence.wxs $(MSIDIR)/$(MAIN_MSI) $(MSIDIR)/winfsp.msi $(MSIDIR)/sshfs-win.msi | $(BINDIR)
	$(WIX) build $(WIXDEFS) \
		-ext WixToolset.BootstrapperApplications.wixext \
		-ext WixToolset.UI.wixext \
		-arch x64 \
		-src Telepresence.wxs \
		-bindpath . \
		-bindpath "$(MSIDIR)" \
		-o $@

# Main MSI (binary, service daemon, and wintun.dll)
$(MSIDIR)/$(MAIN_MSI): MainPackage.wxs Dialogs_en-us.wxl $(BINDIR)/$(EXE) $(BINDIR)/$(DAEMON_EXE) $(BINDIR)/wintun.dll $(BUILD)/$(LICENSE) | $(MSIDIR)
	$(WIX) build $(WIXDEFS) \
		-ext WixToolset.UI.wixext \
		-ext WixToolset.Util.wixext \
		-arch x64 \
		-loc Dialogs_en-us.wxl \
		-bindpath . \
		-bindpath "$(BUILD)" \
		-bindpath "$(BINDIR)" \
		-o $@ $<

$(ZIPDIR):
	mkdir -p $@

$(BINDIR):
	mkdir -p $@

$(MSIDIR):
	mkdir -p $@

$(BUILD):
	mkdir -p $@

# Dependencies
deps: $(MSIDIR)/winfsp.msi $(MSIDIR)/sshfs-win.msi $(BINDIR)/wintun.dll $(BINDIR)/$(EXE) $(BINDIR)/$(DAEMON_EXE) $(BUILD)/$(LICENSE)

WF_URL := https://github.com/winfsp/winfsp/releases/download/v2.1/winfsp-$(WINFSP_VERSION).msi

$(MSIDIR)/winfsp.msi: | $(MSIDIR)
	@if [ -f "$@" ]; then \
		$(CURL) -L --fail -z "$@" -R -o "$@" "$(WF_URL)"; \
	else \
		$(CURL) -L --fail -R -o "$@" "$(WF_URL)"; \
	fi

SSHFS_URL := https://github.com/winfsp/sshfs-win/releases/download/v$(SSHFS_VERSION)/sshfs-win-$(SSHFS_VERSION)-x64.msi

$(MSIDIR)/sshfs-win.msi: | $(MSIDIR)
	@if [ -f "$@" ]; then \
		$(CURL) -L --fail -z "$@" -R -o "$@" "$(SSHFS_URL)"; \
	else \
		$(CURL) -L --fail -R -o "$@" "$(SSHFS_URL)"; \
	fi

# wintun.dll
# Paths
WT_ZIP         := $(ZIPDIR)/wintun-$(WINTUN_VERSION).zip
WT_URL         := https://www.wintun.net/builds/wintun-$(WINTUN_VERSION).zip
WT_FILE        := wintun/bin/$(ARCH)/wintun.dll

$(BINDIR)/wintun.dll: FORCE | $(BINDIR) $(ZIPDIR)
	@echo "Checking for newer $@..."
	@if [ -f "$@" -a -f "$(WT_ZIP)" ]; then \
		# Try conditional request first \
		http_code=$$($(CURL) -L --fail -z "$@" -R -o "$(WT_ZIP)" --write-out '%{http_code}' "$(WT_URL)"); \
		if [ "$$http_code" = "200" ]; then \
			echo "Downloaded updated wintun $(WINTUN_VERSION). Extracting $@..."; \
			$(UNZIP) -op "$(WT_ZIP)" "$(WT_FILE)" > "$@" && touch -r "$(WT_ZIP)" "$@"; \
		elif [ "$$http_code" = "304" ]; then \
			echo "$@ already up to date (not modified)"; \
		else \
			echo "Download failed (status $$?)"; \
			exit 1; \
		fi \
	else \
		# File doesn't exist -> full download \
		echo "Downloading wintun $(WINTUN_VERSION) (first time)..."; \
		$(CURL) -L --fail -R -o "$(WT_ZIP)" "$(WT_URL)" || { \
			rm -f "$@"; exit 1; \
		}; \
		echo "Extracting $@..."; \
		$(UNZIP) -op "$(WT_ZIP)" "$(WT_FILE)" > "$@" && touch -r "$(WT_ZIP)" "$@"; \
	fi

# Telepresence binary
# Paths
TP_ZIP         := $(ZIPDIR)/telepresence-windows-$(ARCH)-v$(VERSION).zip
TP_URL         := https://github.com/telepresenceio/telepresence/releases/download/v$(VERSION)/telepresence-windows-$(ARCH).zip

# For CI builds, the binary may already exist from the main build process.
# Check if it exists and is a valid Windows executable (has MZ header).
$(BINDIR)/$(EXE): FORCE | $(BINDIR) $(ZIPDIR)
	@if [ -f "$@" ] && head -c2 "$@" 2>/dev/null | grep -q "MZ"; then \
		echo "$@ already exists and is valid (using local build)"; \
	elif [ -f "$@" -a -f "$(TP_ZIP)" ]; then \
		echo "Checking for newer Telepresence v$(VERSION)..."; \
		http_code=$$($(CURL) -L --fail -z "$@" -R -o "$(TP_ZIP)" --write-out '%{http_code}' "$(TP_URL)"); \
		if [ "$$http_code" = "200" ]; then \
			echo "Downloaded updated telepresence v$(VERSION). Extracting $@..."; \
			$(UNZIP) -o "$(TP_ZIP)" $(EXE) -d $(dir $@); \
			touch -r "$(TP_ZIP)" "$@"; \
		elif [ "$$http_code" = "304" ]; then \
			echo "$@ already up to date (not modified)"; \
		else \
			echo "Download failed (status $$http_code)"; \
			exit 1; \
		fi; \
	else \
		echo "Downloading telepresence v$(VERSION) (first time)..."; \
		$(CURL) -L --fail -R -o "$(TP_ZIP)" "$(TP_URL)" || { \
			rm -f "$@"; exit 1; \
		}; \
		echo "Extracting $@..."; \
		$(UNZIP) -o "$(TP_ZIP)" $(EXE) -d $(dir $@); \
		touch -r "$(TP_ZIP)" "$@"; \
	fi

# Build the TelepresenceDaemon service wrapper
$(BINDIR)/$(DAEMON_EXE): tpwrapper.go | $(BINDIR)
	GOOS=windows GOARCH=$(ARCH) $(GO) build --trimpath -ldflags="-s -w" -o $@ $<

$(BUILD)/$(LICENSE): ../../LICENSE | $(BUILD)
	pwsh -File txt2rtf.ps1 "$<" "$@"

# First-time setup
init:
	$(GO) mod init telepresence-wix-customaction
	$(GO) get golang.org/x/sys@v0.25.0
	$(GO) mod tidy

clean:
	rm -rf $(BUILD) $(BINDIR) $(MSI) $(BUNDLE) *.wixobj *.wixpdb
