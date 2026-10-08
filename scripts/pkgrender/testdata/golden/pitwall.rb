# Rendered by scripts/pkgrender in quanticstudios/pitwall for each release.
class Pitwall < Formula
  desc "Terminal multiplexer for coding agents"
  homepage "https://github.com/quanticstudios/pitwall"
  version "0.1.0-alpha.23"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/quanticstudios/pitwall/releases/download/v0.1.0-alpha.23/pitwall_darwin_arm64.tar.gz"
      sha256 "3c95b1e453e8ef36f44d874c113eef6521e186dc173fa4162b49b9bd58f3e6a8"
    end
    on_intel do
      url "https://github.com/quanticstudios/pitwall/releases/download/v0.1.0-alpha.23/pitwall_darwin_amd64.tar.gz"
      sha256 "4c339714846bf1b34451a67afa3c3d23c43a204681cd5215ada0da01fa6bf128"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/quanticstudios/pitwall/releases/download/v0.1.0-alpha.23/pitwall_linux_arm64.tar.gz"
      sha256 "4b614bfd9b6086784094fe31d2fb9a3ed5276a597b063e1e11f1f5162a1297c3"
    end
    on_intel do
      url "https://github.com/quanticstudios/pitwall/releases/download/v0.1.0-alpha.23/pitwall_linux_amd64.tar.gz"
      sha256 "03543c4f7f827342b6268a0a5a0fe74c2c33889bdbfc11b54965dc590f9e699d"
    end
  end

  def install
    bin.install "pitwall"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/pitwall --version")
  end
end
