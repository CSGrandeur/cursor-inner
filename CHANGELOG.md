# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.4.0] - 2026-10-11

### Added

- Qwen family: inline reasoning tags (`<think>…</think>` / `<thinking>…</thinking>`) streamed inside `content` (common on vLLM/Ollama serving Qwen3 and QwQ) are now split out of the visible answer into the thinking channel, buffered safely across stream chunks. This keeps chain-of-thought out of the reply and out of the text tool-call parser. A no-op for endpoints that already use `reasoning_content`.

- Settings page: the model capability probe now opens a clean, aligned result panel (tool calls native/text, reasoning, image input, cache hit, speed) with consistent ok / degraded / unsupported icons and a short explanation for degraded items, in Chinese and English — e.g. a model that fell back to text tool mode reads "Native tool calls not supported; using text tool mode". The model list also shows a small "text tool" badge for such models, and the speed badge reopens the panel.

- The real-model eval runner (`cmd/qweneval`) and the offline multi-turn eval now check fuzzy edits *behaviorally*: the edited file plus a probe test are compiled and run with `go test` (new `internal/behav` helper), so any correct empty-slice guard passes and a guard that still panics fails with the toolchain reason surfaced in the note — instead of matching one hard-coded string.

- Text tool mode for endpoints that do not accept native/function tool calls (e.g. vLLM started without `--enable-auto-tool-choice` / `--tool-call-parser`): on a 400 whose message mentions those flags, cursor-inner remembers it per endpoint+model, stops sending `tools`/`tool_choice`, writes the tool specs into the system prompt as `<tool_call>{json}</tool_call>` blocks that the existing text tool-call rescue parser reads back, and retries once immediately. It is also a `text_tool_mode` capability that the capability probe can set and that a session honours from saved capabilities. Verified on a vLLM OpenAI-compatible gateway: a Qwen3-VL model that returned 0/3 because it rejected native tools now scores 2/3 through text tool mode.
- Upstream `error.message` is now surfaced (whitespace-collapsed, secret-redacted, capped at ~200 characters) in the model error log and the harness/eval notes, instead of a bare "请求被拒绝" / "request rejected", so a rejected request says why.
- Qwen3 with reasoning off now also sends `chat_template_kwargs: {"enable_thinking": false}` next to the top-level `enable_thinking: false`, because vLLM only honours the former. Verified on the gateway: qwen3-32b stopped emitting hidden reasoning, dropping a representative task from ~2533 output tokens / ~61s to ~100 output tokens / a few seconds.

- Self-update with hot handover. The settings page checks once at startup (and on demand) for a new release, shows the changelog, and offers an "Update" button; the tray menu gains "Check for updates" with a confirmation dialog. Each release ships a `manifest.json` (version, notes, per-platform file name, size, sha256), optionally signed with ed25519 (`manifest.json.sig`); when a public key is built in, unsigned or tampered manifests are rejected. The check tries the default network path first and, if that fails or times out and a proxy is configured, retries through the proxy; the path that succeeded is used for the download and logged. The download is verified against the manifest size and sha256, then the new binary is pre-flighted (`--version`) before anything changes. The update waits while a custom-model turn is running. The running executable is renamed aside (`.old`) and replaced atomically; the new process is started independently, takes over the same local ports (Cursor proxy, Grok bridge, settings page) and the takeover state without rewriting Cursor settings or restarting Grok or Cursor, and is health-checked by the old process. On any failure the old process kills the new one, restores its executable, rebinds the same ports and keeps serving; it never falls back to direct connections. After a successful handover the old process finishes in-flight connections, then exits without undoing the takeover.
- Settings page gains an "Open logs" button (`POST /api/logs`) that opens the log folder (`<data>/logs` in the developer build, otherwise `<data>`), plus a "Leak check" button (`POST /api/leakcheck`) that triggers the developer build's in-process proxy-leak check (release builds report it is developer-only). A new `logx.AddSink` hook lets extra recorders mirror every log record; release builds register no sink and stay minimal.
- Verbose behavior logging — full HTTP/WS capture, custom-model harness traces, and an events log, split into `grok/` and `cursor/` folders under `<data>/logs/` with redacted headers, capped bodies, size-based rotation and explicit-offset (local +08:00) timestamps — is recorded only by the internal developer build; release builds stay minimal and write just `<data>/inner.log`. Secrets are never logged (redaction is unit-tested), and Grok launch logs strip any proxy credentials. The developer build also runs the proxy-leak check in process (shortly after takeover, every 10 minutes, and on demand from the settings page), writing results to its `grok/` events log with official-endpoint direct connections flagged as errors.
- apply_patch support: an `apply_patch` tool call or a Codex-style `apply_patch <<'EOF'` Shell command is turned into a single-file edit. Update hunks use the same tolerant matching as StrReplace; Add File writes a new file. Multi-file, Delete and Move patches get a clear message asking the model to split them or use the matching tool.
- Grok takeover with a proxy that needs a username and password, or with a socks proxy: Grok Bot cannot use these itself, so cursor-inner runs a loopback-only HTTP CONNECT bridge and dials through your configured proxy, including its authentication. If the upstream proxy fails, the bridge answers 502 and never connects directly. The bridge port is derived from the proxy address, so it stays the same across restarts. While cursor-inner is not running, such a Grok Bot cannot connect at all instead of going direct.
- Best-in-class Qwen family support, informed by a study of qwen-code (QwenLM, Apache-2.0; credited in THIRD_PARTY_NOTICES): the tool-call rescue parser now also reads the `<invoke name="..."><parameter name="...">` dialect alongside `<function=...><parameter=...>`, decodes XML entities (`&lt;` `&gt;` `&amp;` ...) in XML tool arguments so edit/write payloads match the file, and skips tool-call-shaped text sitting inside Markdown code fences (format examples the model echoes). Qwen defaults are raised to a 256K context / 32K output window, QwQ is detected as the Qwen family, Qwen3 hybrid-thinking chat requests (excluding always-thinking QwQ) carry an `enable_thinking` flag mirroring the reasoning toggle, and the Qwen model note now warns against printing tool-call tags, fencing arguments, or XML-escaping code. Rescue eval rose from 17/21 to 21/21.
- Grok proxy takeover now bypasses LAN/WSL private ranges for direct connections: `--proxy-bypass-list` and `NO_PROXY`/`no_proxy` add `<local>`, `*.local`, `wsl.localhost`, the machine hostname, and the RFC1918/link-local/ULA ranges (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16, fc00::/7, fe80::/10) alongside loopback, so local services, the LAN and WSL stay reachable without the proxy. The TUN fake-ip range (198.18.0.0/15) and the official domains are never bypassed and no catch-all wildcard is used; the TUN keeps include-only routing (only the fake-ip range and real DNS enter it, never the private ranges), so the two mechanisms stay consistent. New tests assert the private ranges are bypassed while official domains and the fake-ip range never are.
- Loop detection hardened, informed by a study of qwen-code's loopDetectionService: repeated-tool-call stopping now counts *consecutive* identical calls (warn at 3, stop at 5 — kept below DashScope's server-side repeat threshold so the client breaks the loop before the server rejects the conversation with a 400) with a cumulative backstop (stop at 8 identical calls overall) that still catches A/B oscillation, so productive repeats interleaved with other work are no longer falsely stopped. A new streamed-content repetition guard ends a reply early when the model chants the same text, skipping punctuation-only runs (Markdown tables, rules) to avoid false positives. Measured on the harness evals: loop detection 4/5 → 5/5, plus a new content-loop eval (6/6) and a new compaction regression eval (5/5).
- Deeper Qwen family hardening, continuing the qwen-code study (QwenLM, Apache-2.0): OpenAI-compatible chat requests to Qwen now send `parallel_tool_calls` (when tools are present) and, for Qwen3 hybrid-thinking with an explicit effort, a `thinking_budget` (low 1024 / medium 8192 / high 24576) alongside `enable_thinking`; `stream_options.include_usage` is already always set. Context/output windows are now resolved per Qwen variant (qwen3-coder-plus/flash and qwen3.x → 1M/64K, qwen3-coder → 256K/64K, qwen3-max and qwen2.5 → 256K/32K, QwQ → 128K/32K) instead of one blanket default, and are only applied when the user has not set their own. DashScope error bodies are now classified by their `code`/`message` (Throttling → retryable rate-limit even on a 400, Arrearage/quota → non-retryable auth, DataInspection/content-filter → non-retryable, "Range of input length" → context-overflow so the harness compacts and retries, InternalError → retryable server). The streaming accumulator now splits Ollama's index-less multi-tool-call deltas (same index, different call id) into separate calls instead of concatenating their arguments. Qwen-VL image input already uses the `image_url` data-URI form. New unit/eval cases cover each behavior.
- New developer CLI `cmd/qweneval`: runs the multi-turn harness task suite against any OpenAI-compatible or Anthropic endpoint using real model calls and prints a pass/fail scorecard (turns, tool calls, token usage). Endpoint, key and model come from `QWEN_EVAL_*` environment variables (with optional proxy, reasoning/effort and JSON output), so no secret is committed; it is ready to run against DashScope or a local Qwen (Ollama/vLLM).
- Per-family model notes in the system prompt (DeepSeek, Qwen, Kimi, GLM, other open models, Gemini, GPT); none for Claude.
- `scripts/grok-leak-check.ps1` checks Grok Bot and Cursor: the official domain list is resolved at run time with DoH through the proxy, plus TLS certificate and reverse DNS checks. Direct connections to official endpoints (grok, x.ai, Cursor, cursorvm) are FAIL; third-party ones are INFO.
- Optional Strict mode (settings page button with a UAC prompt, or `scripts/strict-egress.ps1`): Windows Firewall blocks Grok Bot.exe and Cursor.exe from connecting directly to the resolved official IPs. A scheduled task refreshes them at startup and every 30 minutes, and keeps the old rules if resolving fails. Third-party direct connections are left alone. Blocking is by IP, so other sites sharing a CDN IP are also blocked for direct connections from these two apps.
- Investigated the remaining direct connections to official endpoints and could not force them through the proxy from outside the apps. With `cursor.general.disableHttp2` on (set by Cursor takeover) Cursor's main API traffic already goes through the proxy; the leftover direct `api3.cursor.sh` connection is Cursor's metrics/telemetry client, and Grok's leftover `us*.cursorvm.com` connection is its cloud-agent gateway probe. Both are hardcoded HTTP/2 clients with no proxy support, running in isolated Electron Node processes: a bundled UI extension loads into a different extension host than the one that makes these calls, and `NODE_OPTIONS=--require` is stripped by Electron's `nodeOptions` fuse, so neither a `tls.connect` shim nor `HTTPS_PROXY`/`NODE_USE_ENV_PROXY` reaches them (verified against test instances). Routing them would require patching the vendor bundles. Strict mode (the official-IP firewall from the previous change) stays the zero-leak guarantee for these two.
- Optional TUN mode (settings page button with a UAC prompt): cursor-inner manages a minimal bundled sing-box TUN that captures only the official domains (grok, x.ai, Cursor, cursorvm, etc.) at the network layer and forwards them to your configured proxy, so even the apps' hardcoded HTTP/2 and the leftover `api3.cursor.sh` / `cursorvm` connections are proxied with no leaks. All other traffic is untouched: only the fake-ip range and your real DNS servers are routed into the TUN, so every other domain keeps resolving through your real DNS to real IPs and connects directly (LAN, WSL and v2rayN unaffected). Admin rights are requested only after you confirm, via UAC. If you decline, UAC fails, sing-box fails to start, or the TUN dies, cursor-inner keeps working with the normal proxy takeover (env, flags, bridge), shows a "TUN off, official endpoints may leak" status, and still offers Strict mode: it never blocks startup or breaks networking. The config is generated by `internal/tunnel` and validated against sing-box 1.14.3; the binary is downloaded (sha256-checked) into the cursor-inner data dir on first use.
- Tool-call rescue understands the native text formats of Hermes / Qwen2.5 (`<tool_call>{json}</tool_call>`), Qwen3-Coder (`<function=…><parameter=…>`), GLM (`<arg_key>/<arg_value>`), Kimi K2 (`<|tool_call_begin|>functions.X:0…`) and DeepSeek V3 / V3.1 (`<｜tool▁call▁begin｜>…`), plus nested inline JSON. XML parameter values are typed from the tool schema. The local rescue eval went from 4/17 to 17/17.
- When a fallback model takes over, the conversation shows a one-line notice naming both models. The notice is not added to the model history.
- Anthropic streaming responses report cache reads: prompt tokens now include cached and cache-creation input, so the console cache-hit percentage matches the OpenAI figure.
- Grok takeover adds `--force-webrtc-ip-handling-policy=disable_non_proxied_udp`, so WebRTC cannot send UDP around the HTTP proxy. A Grok Bot started by an older version is restarted once to pick it up.
- A regression test (`TestProxyEnvForcesCONNECTThroughProxy`) proves Grok takeover env still sends HTTPS through the configured proxy when the process inherited `NO_PROXY=*`, which is the failure mode under system PAC.
- Capability probe on Test: tool round-trip, reasoning (auto-off on 400), image input, cache hit, plus the existing speed numbers. Results can fill reasoning / context / max-output defaults. Family profile table covers deepseek, qwen, kimi, glm, gemini, gpt, claude (and mimo).
- Per-model fallback chain (other saved model ids). Before any text has streamed, auth / persistent 5xx / rate-limit timeout / context overflow switches to the next model. Shown in the add/edit form.
- OpenAI Responses API endpoint type (`openai-responses`), including encrypted reasoning replay via `include=reasoning.encrypted_content`.
- Optional `prompt_cache_key` on each model (sent on OpenAI Chat / Responses). Cache-hit % stays on the console end line; the test hover shows a caps summary.
- Optional models.dev metadata defaults (context / max output / reasoning), cached under the config directory and fetched through the outbound proxy when possible.
- A local eval suite that runs against a mock provider.

### Changed

- Open Grok with takeover on refuses to start Grok Bot when no usable proxy can be worked out, instead of starting it without a proxy.
- StrReplace keeps a CRLF file CRLF (Write too, when it replaces a CRLF file), converts indentation between tabs and spaces level by level, and on a miss also retries with Read line-number prefixes removed, extra trailing blank lines dropped, and curly quotes straightened, before trying the fuzzy match. A miss on a large file no longer spends seconds in character-level diffs. The local edit eval went from 6/13 to 13/13.
- Quitting cursor-inner no longer restarts Grok Bot without its proxy arguments. Grok Bot keeps using your configured proxy, which does not depend on cursor-inner running. To remove the proxy, turn off Take over Grok. A proxy address that is temporarily invalid while you edit it also leaves Grok Bot unchanged instead of switching it to a direct connection.
- When Grok takeover starts Grok Bot, `NO_PROXY`/`no_proxy` and `--proxy-bypass-list` are loopback only. Process and system `no_proxy` are no longer merged in: under v2rayN PAC mode those inherited entries made undici dial origins directly, so cloud features could fail with a misleading usage-exhausted error while global mode still worked. The launch also sets `GRPC_PROXY`/`grpc_proxy`, always passes an explicit loopback-only `--proxy-bypass-list`, and treats `--no-proxy-server`, `--proxy-auto-detect`, `--proxy-pac-url`, or a `direct://` proxy fallback as not routed so an older process is restarted. README documents how to verify egress with PAC mode.
- A `--debug` instance with its own data directory uses the proxy from your normal settings when its own config has none, so official traffic does not go out directly. Only the proxy setting is copied, read-only. The console header shows the outbound as `代理 <address>`, or `直连` when no proxy is configured anywhere.
- The console window title and the settings page title show the version, for example `cursor-inner v0.3.7`.
- StrReplace: after an exact miss, try whitespace/indent-insensitive match, then a unique high-similarity fuzzy block.
- Assistant text that embeds `<tool_call>`, DSML tool markup, or JSON fences is rescued into real tool calls; tool names are fuzzy-matched to the catalog.
- Identical tool name+arguments three times warns the model; the fourth stops the turn.
- Compaction summaries use fixed sections (Goal / Done / Files / Decisions / Next) and ask the model to re-read edited files afterwards.

### Fixed

- Existing conversation history files and folders from earlier versions are tightened to owner-only on startup.
- The settings page API only accepts loopback `Host` headers and refuses mutating requests whose `Origin` or `Sec-Fetch-Site` is cross-site. This blocks DNS-rebinding and cross-site requests from web pages, for example ones that add a model or quit the program. Responses also send `X-Frame-Options: DENY` and `nosniff`.
- A saved API key is no longer reused for a different server. Editing a model with an empty key field and a new host now asks for the key again, and Test on such a draft does not send the stored key.
- An IPv6 proxy address keeps its brackets when it is handed to Grok Bot.
- New conversation history files are written readable by the owner only.
- Reasoning echo rules are driven by the family profile table instead of a separate hard-coded list.

## [0.3.7] - 2026-10-10

### Changed

- Open Cursor and Open Grok in the settings page top bar show the apps' own icons instead of drawn marks.

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

[Unreleased]: https://github.com/CSGrandeur/cursor-inner/compare/v0.4.0...HEAD
[0.4.0]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.7...v0.4.0
[0.3.7]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.6...v0.3.7
[0.3.6]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.5...v0.3.6
[0.3.5]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.4...v0.3.5
[0.3.4]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.2...v0.3.4
[0.3.2]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.1...v0.3.2
[0.3.1]: https://github.com/CSGrandeur/cursor-inner/compare/v0.3.0...v0.3.1
[0.3.0]: https://github.com/CSGrandeur/cursor-inner/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/CSGrandeur/cursor-inner/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/CSGrandeur/cursor-inner/releases/tag/v0.1.0
