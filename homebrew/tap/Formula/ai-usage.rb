# typed: false
# frozen_string_literal: true

class AiUsage < Formula
  homepage "https://github.com/saaskit-dev/ai-usage"
  url "https://github.com/saaskit-dev/ai-usage.git"
  version "v0.1.0"
  license "MIT"

  depends_on "go" => :build

  def install
    system "go", "build", "-o", bin/"ai-usage", "./cmd/ai-usage"

    # Install example config
    etc.install "config.example.yaml" => "ai-usage.example.yaml"

    # Create data directory
    (var/"ai-usage").mkpath
  end

  def post_install
    # Create default config if not exists
    config_path = HOMEBREW_PREFIX/"etc/ai-usage.yaml"
    unless config_path.exist?
      cp etc/"ai-usage.example.yaml", config_path
      ohai "Created default config at #{config_path}"
    end
  end

  def caveats
    <<~EOS
      Configuration file created at:
        #{HOMEBREW_PREFIX}/etc/ai-usage.yaml

      To enable auto-start on macOS:
        ai-usage daemon install

      To enable auto-start on Linux:
        sudo ai-usage daemon install

      Or for user-level service (Linux):
        ai-usage daemon install --user

      Sample configuration at:
        #{HOMEBREW_PREFIX}/etc/ai-usage.example.yaml
    EOS
  end

  service do
    run [bin/"ai-usage"]
    keep_alive true
    log_path var/"log/ai-usage.log"
    error_log_path var/"log/ai-usage.error.log"
  end

  test do
    assert_version_match version, shell_output("#{bin}/ai-usage --version").strip
  end
end
