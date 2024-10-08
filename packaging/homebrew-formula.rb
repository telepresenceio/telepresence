# This script is generated automatically by the release automation code in the
# Telepresence repository:
class __FORMULA_NAME__ < Formula
  desc "Local dev environment attached to a remote Kubernetes cluster"
  homepage "https://telepresence.io"

  conflicts_with "telepresence"

  on_macos do
    # macfuse is a cask and formula can't depend on casks, so we can't actually
    # do this. This is probably fine since you don't _need_ macfuse to run
    # the cli, just to do mounts
    #depends_on "macfuse"

    if Hardware::CPU.arm?
      url "https://app.getambassador.io/download/tel2/darwin/arm64/v__NEW_VERSION__/telepresence"
      sha256 "__TARBALL_HASH_DARWIN_ARM64__"

      def install
        bin.install "telepresence"

        # TODO installing completions raises an error
        # # Install bash completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "bash")
        # (bash_completion/"telepresence").write output

        # # Install zsh completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "zsh")
        # (zsh_completion/"_telepresence").write output

        # # Install fish completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "fish")
        # (fish_completion/"telepresence.fish").write output
      end
    end
    if Hardware::CPU.intel?
      url "https://app.getambassador.io/download/tel2/darwin/amd64/v__NEW_VERSION__/telepresence"
      sha256 "__TARBALL_HASH_DARWIN_AMD64__"

      def install
        bin.install "telepresence"

        # TODO installing completions raises an error
        # # Install bash completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "bash")
        # (bash_completion/"telepresence").write output

        # # Install zsh completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "zsh")
        # (zsh_completion/"_telepresence").write output

        # # Install fish completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "fish")
        # (fish_completion/"telepresence.fish").write output
      end
    end
  end

  on_linux do
    # if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
    #   url "https://app.getambassador.io/download/tel2/linux/arm64/v__NEW_VERSION__/telepresence"
    #   sha256 "__TARBALL_HASH_LINUX_ARM64__"

    #   def install
    #     bin.install "telepresence"

    #     # Install bash completion
    #     output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "bash")
    #     (bash_completion/"telepresence").write output

    #     # Install zsh completion
    #     output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "zsh")
    #     (zsh_completion/"_telepresence").write output

    #     # Install fish completion
    #     output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "fish")
    #     (fish_completion/"telepresence.fish").write output
    #   end
    # end
    if Hardware::CPU.intel?
      url "https://app.getambassador.io/download/tel2/linux/amd64/v__NEW_VERSION__/telepresence"
      sha256 "__TARBALL_HASH_LINUX_AMD64__"

      def install
        bin.install "telepresence"

        # TODO installing completions raises an error
        # # Install bash completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "bash")
        # (bash_completion/"telepresence").write output

        # # Install zsh completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "zsh")
        # (zsh_completion/"_telepresence").write output

        # # Install fish completion
        # output = Utils.safe_popen_read("#{bin}/telepresence", "completion", "fish")
        # (fish_completion/"telepresence.fish").write output
      end
    end
  end

  test do
    system "#{bin}/telepresence", "--help"
  end
end
