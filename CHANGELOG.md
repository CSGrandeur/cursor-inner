# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.3.6] - 2026-10-10

### Changed

- The README notes that Open Cursor and Open Grok sit in the settings page top bar with their app icons.
- A source comment no longer points at a file that is not in this repository.

## [0.3.5] - 2026-10-09

### Added

- The settings page has an Open Grok button next to Open Cursor, each with a small inline app mark. On Windows it finds Grok Bot from a running process, the App Paths / uninstall registry, or the usual per-user and Program Files locations. If Grok takeover is on, the process is started with the same proxy arguments and environment SyncGrok uses (`NODE_USE_ENV_PROXY=1`, `--use-env-proxy`, `--proxy-server`, `--disable-quic`). A running Grok Bot is not started a second time.

### Fixed

- On Windows, taking over Grok Bot also sets `NODE_USE_ENV_PROXY=1` and passes `--use-env-proxy`, so the Electron main-process undici client (including auth poll) uses the configured HTTP proxy instead of dialing the origin on port 443 directly. A process that still has only the older proxy arguments is restarted once so the new environment applies. Login-return processes still do not force a restart when an already-routed process is present.

## [0.3.4] - 2026-10-09

### Changed

- The settings page tightens the Cursor / Grok / proxy copy, lets long explanations wrap to two lines, and softens the page background and motion without adding a UI toolkit.

### Fixed

- On Windows, editing the system hosts file no longer creates a temporary file in `drivers\etc` and then replaces the original. That replacement is denied even for an elevated process. The existing file is updated in place. If it is still denied, the message says whether the process is elevated. This path stays in `procfwd` for cleanup and future use; current takeover does not call it.
- The system hosts file is not modified, and local port 443 is not used to redirect Cursor host names. Cursor keeps using its own proxy setting, which forwards through the configured proxy.
- The settings page has separate switches for taking over Cursor and Grok Bot. Both default to on. Cursor's proxy is a settings file, so turning that switch on or off closes a running Cursor and leaves it for you to open again. Grok Bot's proxy is a launch argument, so turning that switch on or off closes a running Grok Bot and opens it again with the current choice. Grok Bot is not started if it is not already running. While Grok takeover is on and a proxy is configured, that process uses the proxy as an HTTP proxy on the same host and port, with Quic disabled.
- While Grok takeover is on, a Grok Bot that already has the proxy arguments is left running. A second Grok Bot process without those arguments, such as the one opened from the login page, does not close the first. Closing it drops the login poll, so the browser can say it is done while the window stays on the sign-in screen.
- Analytics, nudge, and similar telemetry requests that still carry a local custom-model id are scrubbed with a same-length placeholder before they leave for Cursor. The selected model is remembered locally first, so Cmd+K can keep using it.

## [0.3.2] - 2026-10-09

### Added

- A saved custom model can be edited. Copy fills the add form, and that form has a clear button. The endpoint column shows the type above the URL; clicking the type copies the URL. Clicking a model name copies it, and hovering a truncated name shows the full text. Every column heading explains itself on hover.

### Changed

- The settings page keeps each control in its own column. A model row is one line.

### Fixed

- A WebSocket on a Cursor host is forwarded with its upgrade headers intact. Stripping them turned the request into a plain GET, and the client retried it continuously. The Agents window speaks this WebSocket. A custom model on it is answered locally; an official model is forwarded unchanged.
- One proxy address is enough. A bare host:port is detected as socks5 or http. An address that already says `socks5://` or `http://` is used as written.
- A browser call aimed at a `file://` URL fails immediately. The browser only opens `http://` and `https://` pages. Local files stay on the file-reading tools. The “Navigated to” label is the requested address, not a successful open.
- A shell command is no longer killed after 24 hours. The foreground wait is capped at 10 minutes, then the command keeps running in the background. A subagent wait is also capped at 10 minutes, and a question wait at 10 minutes. When the cap is reached, the model is told and the turn continues. A tool that stays silent otherwise is stopped after 60 seconds.
- Reading a PNG, JPEG, GIF, or WebP now sends the image to the model with the tool result. This covers Read, MCP tool images, MCP resource blobs, and GenerateImage. The image stays on that result for later turns. Earlier images are dropped only when the conversation is compacted.
- The system prompt no longer includes the date or git status. Git status is attached only to the new user message, so an edit does not invalidate the cached prefix of the rules and the earlier turns.
- While takeover is on and the configured proxy is enabled, Cursor child processes that connect directly to `api3.cursor.sh`, `api4.cursor.sh`, `repo42.cursor.sh`, and the `gcpp.cursor.sh` hosts now leave through that proxy. The protocol is left unchanged. This needs permission to edit the system hosts file and to listen on local port 443. On exit, those hosts entries are removed before the port closes. If they cannot be removed, the port stays open so the names do not go dead.
- A custom-model turn that has not streamed any text or thinking for a few seconds now opens a thinking indicator. Cursor otherwise shows “Taking longer than expected” after about 15 seconds of silence. Heartbeats do not clear that message. The indicator is not sent back to the model.
- A turn that stops on an error, including the 50-step tool limit, still sends the conversation checkpoint. The next turn can see the edits from the stopped turn.
- On Windows, clicking the address in the classic console opened by a double-click opens the settings page.
- On Windows, clicking the console window's close button hides it to the notification area. The process keeps running, and Cursor stays open.

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

[Unreleased]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.6...HEAD
[0.3.6]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.2...v0.3.4
[0.3.2]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/CSGrandeur/cursor-inner/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/CSGrandeur/cursor-inner/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/CSGrandeur/cursor-inner/releases/tag/v0.1.0
