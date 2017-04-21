# This script is generated automatically by the release automation code in the
# Telepresence repository:
class Telepresence < Formula
  desc "Local development environment attached to a remote Kubernetes cluster"
  homepage "http://telepresence.io"
  url "https://github.com/datawire/telepresence/archive/__NEW_VERSION__.tar.gz"
  sha256 "__TARBALL_HASH__"

  depends_on "python3"
  depends_on "torsocks"
  depends_on "sshfs"

  def install
    bin.install "cli/telepresence"
  end

  test do
    system "telepresence", "--help"
  end
end
