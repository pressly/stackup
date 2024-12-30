require "language/go"

class Sup < Formula
  desc "Stack Up. Super simple deployment tool - think of it like 'make' for a network of servers."
  homepage "https://github.com/NovikovRoman/sup"
  url "https://github.com/NovikovRoman/sup/archive/refs/tags/v0.5.3.zip"
  version "0.5.3"
  sha256 "6e7922eb5371eec6a2d089811829f13049a67cfd485e2153f1a5a8e54702ff57"

  depends_on "go"  => :build

  def install
    ENV["GOBIN"] = bin
    ENV["GOPATH"] = buildpath
    ENV["GOHOME"] = buildpath

    mkdir_p buildpath/"src/github.com/NovikovRoman/"
    ln_sf buildpath, buildpath/"src/github.com/NovikovRoman/sup"
    Language::Go.stage_deps resources, buildpath/"src"

    system "go", "build", "-o", bin/"sup", "./cmd/sup"
  end

  test do
    assert_equal "0.5", shell_output("#{bin}/bin/sup")
  end
end
