# This script is generated automatically by the release automation code in the
# Telepresence repository:
class __FORMULA_NAME__ < Formula
  desc "Local dev environment attached to a remote Kubernetes cluster"
  homepage "https://telepresence.io"
  url "https://app.getambassador.io/download/tel2/darwin/__ARCH__/__NEW_VERSION__/telepresence"
  sha256 "__TARBALL_HASH__"

  # macfuse is a cask and formula can't depend on casks, so we can't actually
  # do this. This is probably fine since you don't _need_ macfuse to run
  # the cli, just to do mounts 
  #depends_on "macfuse"

  def install
    bin.install "telepresence"
  end

  test do
    system "#{bin}/telepresence", "--help"
  end
end
