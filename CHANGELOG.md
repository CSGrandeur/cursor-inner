# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.1] - 2026-10-08

### Changed

- Build output uses the release asset name. `VERSION=v0.3.1 ./build.sh windows` writes `cursor-inner-v0.3.1-windows-amd64.exe`.

### Fixed

- The settings page keeps context-window fields inside the panel, keeps the last-test heading on one line, and shows each section as one line of explanation plus one status line.

## [0.3.0] - 2026-10-08

### Added

- The settings page has an Open Cursor button.
- A message sent while a custom-model tool is running joins the current turn after that tool finishes. The next model call sees it. Tools from the same model step that have not started are skipped.

### Fixed

- On Windows, the console close button hides cursor-inner to the notification area. The process keeps running until Quit is chosen from the icon menu or the settings page.

## [0.2.0] - 2026-10-08

### Added

- Custom models in Agent mode can read, search, edit and delete files, run shell commands, edit notebooks, search or open the web after approval, call MCP tools, ask questions, switch mode, present a plan, start a subagent, and generate images once an image endpoint is saved.
- Thinking text is sent back on later turns for DeepSeek, Kimi and MiMo. Other OpenAI-compatible endpoints do not receive that field. Anthropic replays a signed thinking block.
- Conversation checkpoints keep custom-model history across follow-ups, restarts and switching to an official model. Tool cards reopen as the original tool, not as a fake shell command.
- Settings page: context window, max output tokens, and an optional image endpoint.
- The console pins catalog status, local and official request counts, and the last error, and writes a line at the start and end of each custom-model turn.
- On Windows, cursor-inner stays in the notification area. The close button hides the window; quit from the icon menu or the settings page.
- Inline edit and terminal Cmd+K stay on this machine when a custom model is selected.
- `/summarize`, automatic compaction near the context-window limit, retries before any text is shown, and repair of common broken tool arguments.

### Changed

- Cursor's agent protocol is handled with generated code from `proto/cursor/agent_v1.proto`.

### Fixed

- Double-clicking the Windows executable no longer flashes and exits. The classic console is started detached from Windows Terminal; if that relaunch dies immediately, cursor-inner stays in the current window.

## [0.1.0] - 2026-10-04

First public release.

### Added

- Custom models in Cursor: OpenAI Chat Completions and Anthropic Messages endpoints are appended to Cursor's model list and answered locally; official models keep going to Cursor unchanged.
- Local takeover proxy that decrypts only `*.cursor.sh` and routes chats by model id.
- Takeover and restore on Windows, macOS and Linux. Cursor's own proxy settings are backed up and written back; a watchdog restores them after a crash or forced kill.
- Trust for the local root certificate: Windows user root store, macOS login keychain, Linux system store and NSS user database (one privileged prompt, `certutil` installed automatically when missing).
- Start at login: Windows logon task, macOS LaunchAgent, Linux XDG autostart.
- Settings page in Chinese and English, with connectivity tests that record tokens/s, time to first token and output tokens.
- Outbound socks5 / http proxy for Cursor, with a per-model switch for custom models.
- Console window with a pinned header showing the settings page address; press Enter to open it.
- On Windows, launching from Explorer, the Start menu or at login opens a classic console window so the taskbar shows the cursor-inner icon.
- One-line installers: `install.sh` for macOS and Linux, `install.ps1` for Windows, both verifying SHA-256 checksums.

[Unreleased]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/CSGrandeur/cursor-inner/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/CSGrandeur/cursor-inner/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/CSGrandeur/cursor-inner/releases/tag/v0.1.0
