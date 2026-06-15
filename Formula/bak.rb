class Bak < Formula
  desc "Per-file snapshots, diff, and restore without cwd clutter"
  homepage "https://github.com/rohitshidid/bak"
  url "https://github.com/rohitshidid/bak.git",
      tag: "v0.1.1"
  license "MIT"
  head "https://github.com/rohitshidid/bak.git", branch: "main"

  depends_on "go" => :build

  def install
    ldflags = "-s -w -X main.version=#{version}"
    system "go", "build", *std_go_args(ldflags: ldflags)
  end

  test do
    sample = testpath/"sample.txt"
    sample.write "one\n"

    ENV["BAK_DIR"] = testpath/".bak-store"
    system bin/"bak", sample
    sample.write "two\n"
    system bin/"bak", sample, "-m", "second"

    assert_match "v2", shell_output("#{bin}/bak list #{sample}")
    assert_match "one", shell_output("#{bin}/bak show #{sample} v1")
    system bin/"bak", "restore", sample, "v1", "--yes"
    assert_equal "one\n", sample.read
  end
end
