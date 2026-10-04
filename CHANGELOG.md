# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/CSGrandeur/cursor-inner/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/CSGrandeur/cursor-inner/releases/tag/v0.1.0
