# This script is generated automatically by the release automation code in the
# Telepresence repository:
class __FORMULA_NAME__ < Formula
  desc "Local dev environment attached to a remote Kubernetes cluster"
  homepage "https://telepresence.io"
  version "__NEW_VERSION__"

  BASE_URL = "https://app.getambassador.io/download/tel2oss/releases/download"
  ARCH = Hardware::CPU.arm? ? "arm64" : "amd64"
  OPERATING_SYSTEM = OS.mac? ? "darwin" : "linux"
  PACKAGE_NAME = "telepresence-#{OPERATING_SYSTEM}-#{ARCH}"

  url "#{BASE_URL}/v#{version}/#{PACKAGE_NAME}"

  sha256 "__TARBALL_HASH_DARWIN_AMD64__" if OS.mac? && Hardware::CPU.intel?
  sha256 "__TARBALL_HASH_DARWIN_ARM64__" if OS.mac? && Hardware::CPU.arm?
  sha256 "__TARBALL_HASH_LINUX_AMD64__" if OS.linux? && Hardware::CPU.intel?
  # TODO support linux arm64
  #sha256 "__TARBALL_HASH_LINUX_ARM64__" if OS.linux? && Hardware::CPU.arm?

  conflicts_with "telepresence"

  def install
      bin.install "#{PACKAGE_NAME}" => "telepresence"
  end

  test do
      system "#{bin}/telepresence", "--help"
  end
end
