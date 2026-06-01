class Omni < Formula
  desc "AI agent orchestration daemon"
  homepage "https://github.com/Shaik-Sirajuddin/memory"
  version "1.3.0"

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/Shaik-Sirajuddin/memory/releases/download/v#{version}/omni-linux-arm64.tar.gz"
      sha256 "" # filled at release time
    else
      url "https://github.com/Shaik-Sirajuddin/memory/releases/download/v#{version}/omni-linux-amd64.tar.gz"
      sha256 "" # filled at release time
    end
  end

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/Shaik-Sirajuddin/memory/releases/download/v#{version}/omni-darwin-arm64.tar.gz"
      sha256 "" # filled at release time
    else
      url "https://github.com/Shaik-Sirajuddin/memory/releases/download/v#{version}/omni-darwin-amd64.tar.gz"
      sha256 "" # filled at release time
    end
  end

  def install
    bin.install "omni"
    bin.install "omni-server"
  end

  service do
    run [opt_bin/"omni-server"]
    keep_alive crashed: true
    log_path var/"log/omni.log"
    error_log_path var/"log/omni-error.log"
    # On macOS: brew services → launchd plist under ~/Library/LaunchAgents/
    # On Linux:  brew services → systemd user unit
  end

  def caveats
    <<~EOS
      Agent binaries (claude, codex, agy) must be installed separately:
        Claude Code: curl -fsSL https://claude.ai/install.sh | bash
        Codex:       https://github.com/openai/codex/releases
        Agy:         curl -fsSL https://antigravity.google/cli/install.sh | bash

      Start the daemon:
        brew services start omni

      Socket paths are resolved automatically via $TMPDIR on macOS.
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/omni --version")
  end
end
