# Rendered by scripts/pkgrender in quanticstudios/pitwall for each release.
class Pitwall < Formula
  desc "Terminal multiplexer for coding agents"
  homepage "https://github.com/quanticstudios/pitwall"
  version "{{.Version}}"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/quanticstudios/pitwall/releases/download/{{.Tag}}/pitwall_darwin_arm64.tar.gz"
      sha256 "{{sum "pitwall_darwin_arm64.tar.gz"}}"
    end
    on_intel do
      url "https://github.com/quanticstudios/pitwall/releases/download/{{.Tag}}/pitwall_darwin_amd64.tar.gz"
      sha256 "{{sum "pitwall_darwin_amd64.tar.gz"}}"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/quanticstudios/pitwall/releases/download/{{.Tag}}/pitwall_linux_arm64.tar.gz"
      sha256 "{{sum "pitwall_linux_arm64.tar.gz"}}"
    end
    on_intel do
      url "https://github.com/quanticstudios/pitwall/releases/download/{{.Tag}}/pitwall_linux_amd64.tar.gz"
      sha256 "{{sum "pitwall_linux_amd64.tar.gz"}}"
    end
  end

  def install
    bin.install "pitwall"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/pitwall --version")
  end
end
