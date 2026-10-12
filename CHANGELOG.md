# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.4] - 2026-10-12

### Added

- Settings switch Take over Codex, on when `takeover_codex` is missing. On Windows a running Codex desktop app is restarted with the configured proxy (`--proxy-server` for the window, `HTTPS_PROXY` / `ALL_PROXY` / `WS_PROXY` on that process for the app-server). It is not started if it is not already running, and a Codex CLI started in a terminal is left alone. Checked on a Microsoft Store install: the window is `ChatGPT.exe`, the app-server is a child `codex.exe`, and with a SOCKS proxy both use the local HTTP bridge. Turning the switch off restarts the window without those arguments.
- Self-update progress bar in the terminal and on the settings page, both fed by one progress source so the numbers always match. The terminal shows a single in-place line with a bar, percentage, downloaded/total size, speed, time left and the network path, plus a timestamped status line at each step (check → manifest signature → download → sha256 → self-test → hot handover, route switches and failures). It works in the Windows classic console, Windows Terminal and Unix terminals; when the output is not a terminal it prints a plain line every 10% or 15 s instead. Ordinary log lines are printed above the live line without breaking it, and the progress itself never goes into `inner.log`. The settings page shows a step list and a real progress bar pushed over Server-Sent Events (`GET /api/update/events`, with polling as a fallback); after the hot handover it reconnects, reloads and says "Updated from A to B". Checked on 2026-10-11 in an isolated `--classic-console --debug` Windows instance against a local signed release throttled to 96 KB/s with a 1 MB/s proxy: default network → also trying proxy → switched to proxy and resumed → 15.5 MB in 20.6 s → sha256 → self-test → handover; a tampered package stopped at "Verify" and the old version kept running.
- Windows notification-area icon and background mode. The icon shows the state (normal, orange dot for warnings, blue dot while updating) and a tooltip with the version and update progress; a click opens the settings page and the right-click menu has Open settings, Show/Hide console window, Check for updates and Quit. New settings under General: "Run in background" (off by default; start without a console window, icon only; the console can be shown from the menu and stays a classic console window) and "Notifications" (on by default; balloons when an update finishes or fails and when a request for a custom model may have gone to the official service; clicking one opens the matching settings tab). During a hot handover there is only ever one icon: the new process adds its icon only after the handover is confirmed, and the old one removes its icon while draining; in background mode the new process also starts without a console. The icon is recreated when Explorer restarts. macOS and Linux are unchanged.
- Settings page tabs: Models (default), Network, General and Update, so the model list is no longer pushed down by network and update settings. The tab is kept in the URL (`#models`, `#network`, `#update`, `#general`) and survives reloads and hot handovers; tabs support arrow keys, Home and End. A narrow status bar at the top shows version, takeover, proxy, TUN (when on) and update state, and small dots on the tabs point to where a problem is. Each setting keeps a one-line description, with details behind a "?" (hover, focus or click; Esc closes).

### Changed

- Official-only routing follows the hostname. OpenAI's own domains (`openai.com`, `chatgpt.com`, `oaistatic.com`, `oaiusercontent.com`, `oaistatsig.com`) go through the proxy with the Cursor and Grok names. A TLS SNI or HTTP Host on that list does too. When the first bytes have no hostname (SSH, no SNI, no Host), the dialed name is used, so a host that is not on the list connects directly. A public IP still has no name and goes through the proxy. Waiting out the first bytes does not change a name that is already known. Custom models still use their own proxy switch.
- Settings page: Open Codex uses the app icon; custom models are marked Cursor only; the network tab puts the proxy above the takeover switches; the Update tab is last.
- The terminal now prints self-update progress line by line by default (one line per step, one every 10% or 15 s while downloading); the full bar lives on the settings page. `CURSOR_INNER_LIVE_PROGRESS=1` brings back the in-place progress line.
- Tray "Check for updates" now checks in the background and opens the Update tab instead of showing native confirmation dialogs.
- Release workflow: pin the GitHub Actions runner to `ubuntu-24.04` instead of `ubuntu-latest`, matching the image used by the last successful release (run 38146445808 / Ubuntu2404), so the scheduled `ubuntu-latest` cutover around 2026-10-19 does not change the build environment without notice.

### Fixed

- Grok Bot no longer restarts every few seconds when another cursor-inner is running with Grok takeover off. That copy was clearing the proxy arguments the main one had just applied, and the main one put them back. A data directory only removes those arguments after it has applied them itself.
- Custom models in Cursor's Agents window. The client now waits for a server hello on the agent websocket before it sends the run, so the local model id was forwarded to the official service and rejected (`AI Model Not Found`). The hello is completed first. The pooled connection stays open until the user sends, and a custom model is answered locally. Reply frames on that socket start at sequence 0; starting at 1 was dropped as out of order, so the window stayed on Reconnecting. A resume of that same run stays on the local model. Forwarding it made the official service answer `AI Model Not Found` again after the reply was already on screen.
- Windows: the console close-button hook only reacts to cursor-inner's own console window; before, closing another classic console window could be caught as well.
- Self-update download: when the parallel route tried for a slow download failed straight away (for example the proxy refused the connection), it was restarted every second for the rest of the download, flooding the log. A route that fails is now tried once.
- `inner.log` after a hot handover: the old process opened the log without append mode, so its last lines could overwrite the start of the new process's first line. Both processes now append.

## [0.5.3] - 2026-10-11

### Added

- Proxy scope on the settings page: "Global" (default, unchanged behaviour) or "Official only". With Official only, Cursor and Grok Bot send only official endpoints (grok.com, x.ai, spacexai.com, cursor.com, cursor.sh, cursorapi.com, cursor-cdn.com, cursorvm.com, anysphere.co) through the proxy and connect directly to everything else. Cursor's local proxy chooses the route on every dial; Grok Bot always goes through the loopback proxy bridge, which does the same split. Official domains, the TUN fake-ip ranges, hard-coded public IPs and the DoH resolvers used for official names always use the proxy; loopback and LAN/WSL ranges are direct; other domains are decided from the client's TLS SNI or HTTP Host, and a missing SNI or unknown protocol goes through the proxy. A failed direct connection is never retried through the proxy and an official connection never falls back to direct. Switching is immediate; only a plain HTTP proxy needs one automatic Grok Bot restart. TUN and Strict mode rules are the same in both scopes. Custom models' "Use proxy" switch is unchanged. Checked on 2026-10-11 with an isolated instance and a logging upstream proxy: in Global, a third-party HTTPS site, `api.x.ai` and a bare IP all reached the upstream proxy from both the Cursor and Grok paths; in Official only, the third-party host was connected directly by cursor-inner and never reached the proxy, while `api.x.ai`, the bare IP and `cloudflare-dns.com` still did.
- `scripts/grok-leak-check.ps1` probes the routes: through each local proxy address Grok Bot and Cursor use, it connects to an official and a third-party host and checks which new connections cursor-inner opens. An official host reached directly is FAIL in either scope; a third-party host must use the proxy with Global and connect directly with Official only. The scope is read from `config.json` or given with `-Scope`; `-Via`, `-ProbeOfficial`, `-ProbeThirdParty`, `-RouteOnly` and `-NoRouteProbe` control the probe.

### Changed

- The settings page shows download speed, time left and the network path in use while an update downloads, for example "Downloading 42% · 1.3 MB/s · about 9 s left · manifest GitHub · proxy · package Gitee · signature verified". The update line now says "manifest …" and "package …" instead of "from …" and "package from …", which read as two conflicting sources.
- A `--debug` instance no longer closes or restarts the running Grok Bot when its proxy or takeover settings change; it only prepares its own proxy bridge.

### Fixed

- Self-update downloads no longer stay on a slow network path. Before, the package was downloaded only over the path that answered the update check, with no fallback, no resume and no stall timeout: when the default network was slow but kept sending data, the update simply waited. Now the download measures its speed over a sliding 8 s window. If, after a 4 s warm-up, the estimated time left is over 45 s, the same file is also requested over the other path (the configured proxy, or the default network) from the current offset with an HTTP Range request. The two run side by side for 6 s, and the new path is kept only if it was at least 1.5 times faster. A path that sends nothing for 20 s (including no response headers) or drops the connection is resumed from the last written byte, preferring the other path, up to 4 times. A server without Range support is downloaded again from the start. Each address gives up after 15 minutes. However many paths were used, the finished file is re-read and checked byte for byte against the size and sha256 in the signed manifest. If a file pieced together from two paths does not match, each path is tried again on its own, so one bad path cannot spoil a good one. Measured on 2026-10-11 against the real v0.5.2 release (14.9 MB Linux package) with the default path limited to 64 KB/s: 0.4.0 took 232.5 s; the new code switched to the proxy after about 7 s and finished in 11.7 s.
- After a self-update handover, the new process appends to `inner.log` instead of truncating it, so the old process's record of the download, verification and handover is kept.
- Rejecting or undoing an `apply_patch` or `Write` that created a file (an `Add File`, the new path of a `Move to`, or a `Write` to a missing path) now deletes the new file in Cursor instead of leaving an empty 0-byte file. The edit card for a file that did not exist before no longer carries an empty "before" content, which Cursor took as "restore this file to empty". Checked in an isolated Cursor window on 2026-10-11, together with CRLF line endings, which stay CRLF after StrReplace, multi-file `apply_patch` and `Write`.

## [0.5.2] - 2026-10-11

Self-update from GitHub did not work in 0.4.0 and 0.5.1. **If you are on 0.5.1, download 0.5.2 manually once** (0.5.1 cannot update itself and its settings page wrongly says the update manifest failed signature verification). 0.4.0 can update itself to 0.5.2.

### Fixed

- Self-update from GitHub: `releases/latest/download/manifest.json` redirects to `releases/download/vX.Y.Z/manifest.json` and then to a signed CDN address on another host. The client built the signature and package addresses from that CDN address, so 0.5.1 fetched the manifest itself as its "signature" and reported a bad signature, and 0.4.0 got an error status for the package. Sibling files are now resolved against the last address on the original host (`releases/download/vX.Y.Z/`), without query parameters, which also pins signature and package to the version of the manifest. A new end-to-end test reproduces GitHub's two-hop redirect to a CDN that ignores extra parameters.
- The signed manifest now lists each package's GitHub download address, so 0.4.0 (which only uses listed addresses when present) can download the package. Newer clients try each update source's own release directory first and the listed address after it; the content is always checked against the signed sha256.

## [0.5.1] - 2026-10-11

First published release of these changes. The `v0.5.0` tag was never released: its release build stopped at the new signed-manifest self-check (see Fixed).

### Added

- Self-update now has a second update source: a Gitee mirror of the releases, checked in parallel with GitHub. The client finds Gitee's latest release through its API (`/api/v5/repos/…/releases/latest`; the tag must be a version number) and reads `manifest.json` from that release. Both sources must pass the same signature check. The highest verified version wins, and when both offer the same version the faster one is used. If a package download fails or does not match the signed sha256, the other source is tried. Every request still goes over the default network first and falls back to the configured proxy; manifest, signature and package all use whichever path worked. The settings page shows the source, the network path and "signature verified", plus a line for any source that was ignored and why. The release workflow mirrors the tag and the self-update files to Gitee when a `GITEE_TOKEN` secret is set (`scripts/gitee_release.sh`, idempotent, manifest uploaded last). Without the secret the step is skipped and the GitHub release is unaffected.

- apply_patch now handles multi-file patches, `*** Delete File:` and `*** Move to:` (rename, with or without edits), matching Codex semantics: operations run in patch order, Add overwrites an existing file, and the model gets `Success. Updated the following files:` with `A` / `M` / `D` lines. Unlike Codex, every file to update or delete is read first and the whole patch is computed in memory; if any hunk does not match or a target is missing, nothing is written and the model is told no files were changed. Each file gets its own edit or delete card in Cursor. If Cursor rejects a write midway, the result lists what was already applied and what was not. A single-file Delete patch becomes a plain Delete.
- apply_patch hunks use the `@@` context line to disambiguate: when the hunk's lines occur more than once in the file, matching is retried after the unique line containing that context.

- WebSearch recognizes search engines' bot-check pages instead of silently treating them as "no results": Baidu's security verification redirect (`wappass.baidu.com/static/captcha/…`, title 百度安全验证, also when sent as a 503 with `Location`), DuckDuckGo's HTML anomaly challenge (HTTP 202), and generic captcha / antispider pages by URL path and title. The check only runs on pages that yielded no results, so a query that itself contains words like "captcha" is not misread. An engine that served such a page is skipped for 15 minutes. Engines are now queried in parallel, so one slow or unreachable engine no longer adds its timeout to every search. When the primary engines together give fewer than 3 results, Brave Search is queried as a fallback. The model's tool result ends with a short "Not used:" line naming each skipped engine and why; if nothing usable came back, the search is an error listing every engine's reason and suggesting WebFetch on a known URL.
- WebSearch ranks results by how many of the query words they contain and no longer lets off-topic results crowd out good ones. Measured on 2026-10-11 with 20 real technical queries (English and Chinese, with version numbers and product names) through the proxy: cookie-less Bing returned 10 results for every query but matched only the first word (Go 1.25 release notes → Google, the Wikipedia page for the board game Go; Kubernetes 1.31 sidecar containers → Dutch tyre shops), whatever the UA, market, `form`, cookie jar, `cn.bing.com` or RSS endpoint. Because Bing always filled the list, the Brave fallback never ran. Now a result counts as relevant when it covers at least 60% of the query terms (words and version numbers matched as whole words, so "go" does not match "Google"; Chinese matched by character bigrams). Relevant results come first, and once there are 3 of them, results covering under half the terms are dropped. Brave is queried when there are fewer than 3 relevant results instead of fewer than 3 results. An engine whose results are all off-topic is named in the "Not used:" line, and if every result is off-topic the model is told so. DuckDuckGo now uses its lite page, which served results for all 20 queries through the proxy while the HTML page often returned its bot challenge. Result on the same 20 queries through the proxy: relevant results in the top 5 went from 39 to 100 out of 100 (before, Bing's off-topic results took about 3 of the top 5 slots of every search), at about 1.9 s per search, the same as before once Baidu is in its cooldown.
- WebSearch no longer waits on engines it cannot use: an engine that times out or refuses the connection (common for DuckDuckGo and Brave without a proxy) is skipped for 5 minutes, an HTTP 429 rate limit (Brave starts after a few searches in a row) for 5 minutes, and the per-engine timeout is now 8 s instead of 15 s. Without a proxy, a search took 15 s every time; now the first one takes up to about 16 s and the rest about 0.5 s.

### Security

- Update manifests are now always signed and verified with ed25519 (fail closed). The release public key is compiled into every build from `internal/selfupdate/trust.go` (key id `eb68418a53240b13`) instead of being injected by a build variable, so a build where the variable was forgotten no longer silently skips verification. A manifest that is unsigned, fails verification, is signed by an unknown key, names another product, or is past its optional `expires` time is rejected. Nothing is downloaded, and the settings page and tray say which case it was, in plain words, and that the current version keeps running. `manifest.json.sig` is now `{"signatures":[{"keyid","sig"}]}`, so a manifest can be signed by an old and a new key during key rotation, and `trust.go` can list several keys. The old bare-base64 signature is still accepted. Each package is checked against the size and sha256 in the signed manifest, and the new binary must report the manifest's version before it is swapped in. The highest verified version is recorded in `<data>/update/trust.json`. A source that later offers an older signed manifest while that newer version is still not installed is treated as a replay and ignored. A build with no built-in key (e.g. a fork that removed it) still reports new versions but refuses to install them and points to the Releases page. The release workflow refuses to publish without the `UPDATE_SIGNING_KEY` secret, and before publishing it verifies the signed manifest against the compiled-in key and every package's size and sha256. v0.4.0 has no key built in and ignores the signature, so it can still update to the first signed release on its own.

### Fixed

- Release pipeline: the bare Linux/macOS executables were deleted together with their staging directory, so the update manifest only listed Windows (already the case in 0.4.0) and Linux/macOS copies could not self-update. They are now kept next to `manifest.json`, and the release self-check requires every platform.
- Files edited by Cursor-executed tools (StrReplace, Write, EditNotebook, apply_patch) are now remembered for the turn, so after context compaction the model is reminded to re-read them before editing again. Previously only locally answered tools were recorded, so the reminder relied on the files the summary happened to name.

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

[Unreleased]: https://github.com/CSGrandeur/cursor-inner/compare/v0.5.4...HEAD
[0.5.4]: https://github.com/CSGrandeur/cursor-inner/compare/v0.5.3...v0.5.4
[0.5.3]: https://github.com/CSGrandeur/cursor-inner/compare/v0.5.2...v0.5.3
[0.5.2]: https://github.com/CSGrandeur/cursor-inner/compare/v0.5.1...v0.5.2
[0.5.1]: https://github.com/CSGrandeur/cursor-inner/compare/v0.4.0...v0.5.1
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
