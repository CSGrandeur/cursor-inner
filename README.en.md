<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/banner-dark.svg">
    <img src="docs/images/banner-light.svg" alt="cursor-inner: bring your own models into Cursor" width="100%">
  </picture>
</p>

<p align="center">
  <a href="https://github.com/CSGrandeur/cursor-inner/releases/latest"><img src="https://img.shields.io/github/v/release/CSGrandeur/cursor-inner?style=flat-square&color=ff5a2b&label=release" alt="Release"></a>
  <a href="#installation"><img src="https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-151514?style=flat-square" alt="Platform"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-151514?style=flat-square&logo=go&logoColor=white" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/CSGrandeur/cursor-inner?style=flat-square&color=151514" alt="License"></a>
</p>

<p align="center">
  <a href="README.md">简体中文</a> · <b>English</b>
  <br>
  <a href="#installation">Installation</a> · <a href="#features">Features</a> · <a href="#how-it-works">How it works</a> · <a href="#faq">FAQ</a>
</p>

<br>

cursor-inner is a small local tool that adds the OpenAI Chat or Anthropic compatible endpoints you configure to Cursor's model list. When you pick one of them, your machine calls your endpoint directly; when you pick an official model, the request goes to Cursor unchanged. It works with Cursor on Windows, macOS and Linux, and the settings page is available in English and Chinese.

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/window-en-dark.png">
    <img src="docs/images/window-en-light.png" alt="cursor-inner settings page" width="92%">
  </picture>
</p>

## Features

<table>
  <tr>
    <td width="33%" valign="top">
      <b>Custom models</b><br>
      OpenAI Chat Completions and Anthropic Messages endpoints, added, tested and removed on the settings page, and appended to Cursor's model list.
    </td>
    <td width="33%" valign="top">
      <b>Official models untouched</b><br>
      The catalog is only appended to, never replaced. Official-model requests are not modified, apart from going through your outbound proxy.
    </td>
    <td width="33%" valign="top">
      <b>Connectivity tests</b><br>
      Streams the numbers 1 to 120 and records tokens/s, time to first token, total duration and output tokens. Results are saved with the model.
    </td>
  </tr>
  <tr>
    <td width="33%" valign="top">
      <b>Outbound proxy</b><br>
      Route all of Cursor's traffic through a socks5 or http proxy. Each custom model decides on its own whether to use it.
    </td>
    <td width="33%" valign="top">
      <b>Reliable restore</b><br>
      On exit, closing the terminal, a crash or a forced kill, the takeover settings are removed and your own <code>http.proxy</code> and related settings are written back.
    </td>
    <td width="33%" valign="top">
      <b>Three platforms</b><br>
      Takeover, certificate trust and start at login on Windows, macOS and Linux. Single instance, with an English or Chinese settings page.
    </td>
  </tr>
</table>

## Installation

**macOS / Linux**

```bash
curl -fsSL https://raw.githubusercontent.com/CSGrandeur/cursor-inner/main/install.sh | sh
```

**Windows** (PowerShell)

```powershell
irm https://raw.githubusercontent.com/CSGrandeur/cursor-inner/main/install.ps1 | iex
```

The installer detects your OS and CPU, downloads the latest release, verifies it against the release's `SHA256SUMS.txt`, and installs it for the current user without administrator rights. Files fetched this way carry no browser download mark, so SmartScreen and Gatekeeper do not block them. Set `CURSOR_INNER_VERSION=v0.1.0` to install a specific version.

<details>
<summary><b>Where it is installed</b></summary>

| Platform | Installed to | How to start |
| --- | --- | --- |
| Windows | `%LOCALAPPDATA%\Programs\cursor-inner\` | cursor-inner in the Start menu |
| macOS | `~/.local/bin/cursor-inner` | Run `cursor-inner` in Terminal |
| Linux | `~/.local/bin/cursor-inner` | cursor-inner in the app menu, or run it in a terminal |

</details>

<details>
<summary><b>First run: trusting the local certificate</b></summary>

On first takeover, cursor-inner generates a root certificate, "cursor-inner Local CA", that exists only on your machine, and asks the system to trust it. Without that trust, Cursor cannot connect through the local proxy.

| Platform | What you will see |
| --- | --- |
| Windows | A prompt asking whether to install the certificate; choose Yes |
| macOS | A request for your login password to add the certificate to the login keychain as trusted |
| Linux | A polkit dialog; after you authorize, the certificate goes into the system store, and `certutil` is installed in the same step if it is missing |

On Linux the certificate goes into two places: the system store and the NSS user database `~/.pki/nssdb`. The system store is supported on Debian / Ubuntu, Fedora / RHEL, openSUSE and Arch and other p11-kit distributions. Without a desktop session or `pkexec`, the settings page and the console show a `sudo` command you can copy and run. Closing Cursor uses `pgrep` / `pkill` (procps, normally preinstalled).

</details>

<details>
<summary><b>Manual download</b></summary>

Download from [Releases](https://github.com/CSGrandeur/cursor-inner/releases/latest):

| Platform | File |
| --- | --- |
| Windows 10 / 11 | `cursor-inner-<version>-windows-amd64.exe` |
| macOS (Apple silicon / Intel) | `cursor-inner-<version>-darwin-arm64.tar.gz` / `darwin-amd64.tar.gz` |
| Linux (x86_64 / ARM64) | `cursor-inner-<version>-linux-amd64.tar.gz` / `linux-arm64.tar.gz` |

Files downloaded in a browser carry a download mark. On Windows, when SmartScreen appears, click "More info" → "Run anyway"; on macOS, run `xattr -d com.apple.quarantine cursor-inner` before starting it. Every release includes `SHA256SUMS.txt` and an SPDX SBOM.

</details>

## Usage

1. Run cursor-inner. It closes any running Cursor and opens the settings page in your browser.
2. Add a model: display name, type, model name, endpoint URL and API key. Click Test to check that it works.
3. Reopen Cursor, start a new chat, and pick the model you added from the model list.
4. When you are done, close the terminal window or click Quit on the settings page. Cursor's settings are restored automatically.

> [!IMPORTANT]
> With **Auto** selected, Cursor only uses official models. Select a custom model explicitly to use it.

## How it works

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/flow-en-dark.svg">
    <img src="docs/images/flow-en-light.svg" alt="Cursor goes through the local cursor-inner proxy: custom models go to your endpoint, official models go to Cursor unchanged, other hosts are tunneled" width="100%">
  </picture>
</p>

On takeover, cursor-inner edits Cursor's `settings.json`: `http.proxy` points at the local proxy, `http.proxySupport` is set to `override`, and HTTP/2 is disabled. Existing values of these keys are backed up first and written back on restore.

The local proxy decrypts only connections to `*.cursor.sh`; everything else is tunneled directly. Chat requests are routed by model id: ids that belong to a custom model are handled locally, everything else is forwarded to Cursor unchanged.

## Console window

The settings page URL stays pinned at the top of the console window and is never scrolled away by logs. Press Enter to open the settings page; in terminals that support hyperlinks (Windows Terminal, iTerm2, GNOME Terminal and others) you can also Ctrl+click or Cmd+click the URL.

```text
 ▌▐ cursor-inner v0.1.0   http://127.0.0.1:52341   按回车打开配置页
    接管 ● 接管中    出站 socks5://127.0.0.1:1080    自定义模型 3
──────────────────────────────────────────────────────────────────────
20:31:07  接管代理 http://127.0.0.1:61022
20:31:07  已结束 Cursor，重新打开后生效
20:32:15  BidiAppend 3f2a9c… 模型 a1b2c3d4e5f60718 -> 本地
20:32:15  RunSSE 3f2a9c… 由本地模型 DeepSeek V4 Flash 回答
```

On Windows, when started from Explorer, the Start menu or at login, cursor-inner uses a classic console window so the taskbar shows its own icon. When started by typing a command in an existing terminal, it keeps running in that terminal. Start it with `--no-takeover` to open only the settings page, without touching Cursor's settings or closing Cursor; this is meant for debugging.

## FAQ

<details>
<summary><b>I selected Auto. Why isn't my model used?</b></summary>

Auto lets Cursor's servers choose a model, and they only choose among official models. Select your custom model in the model list to use it.

</details>

<details>
<summary><b>Can custom models edit files or run commands?</b></summary>

Not yet. Custom models return text only and do not run tool calls. Use an official model when Agent mode needs to edit files or run commands. Tab completion and other features are still served by Cursor.

</details>

<details>
<summary><b>Where are my API keys and other data stored?</b></summary>

| Platform | Data directory |
| --- | --- |
| Windows | `%APPDATA%\cursor-inner\` |
| macOS | `~/Library/Application Support/cursor-inner/` |
| Linux | `~/.config/cursor-inner/` (or `$XDG_CONFIG_HOME/cursor-inner/`) |

| File | Contents |
| --- | --- |
| `config.json` | Toggles, proxy address, custom models and their last test results. **API keys are stored in plain text**; the file is readable only by the current user. |
| `ca/` | Root certificate and private key generated on this machine |
| `cursor-settings.backup.json` | Backup of Cursor's original proxy settings during takeover; removed after restore |
| `inner.log` | Routing log for the current run, rewritten on every start; contains no chat content or keys |
| `watch.log` | Record of automatic restores after an abnormal exit |
| `takeover.on`, `listen.url`, `instance.lock` | Takeover marker, current settings page URL, single-instance lock |

cursor-inner collects no data: no telemetry, analytics or update checks. The settings page and the local proxy listen on `127.0.0.1` only. Chat content travels only between Cursor, your machine and the model endpoints you configure.

</details>

<details>
<summary><b>If cursor-inner crashes, will Cursor lose its connection?</b></summary>

No. During takeover a separate watchdog process watches the main process. If the main process crashes or is killed, the watchdog removes the takeover settings, writes back your original proxy settings and closes Cursor. Reopen Cursor and it works normally.

</details>

<details>
<summary><b>Will it keep working after Cursor updates?</b></summary>

Official models are not affected. If a Cursor update changes its internal protocol, custom models may stop working until cursor-inner is updated.

</details>

<details>
<summary><b>How do I uninstall it?</b></summary>

1. If you enabled start at login, turn it off on the settings page. Then click Quit; Cursor's settings are restored automatically.
2. Remove the root certificate:

   | Platform | Command |
   | --- | --- |
   | Windows | `certutil -user -delstore Root "cursor-inner Local CA"` |
   | macOS | `security delete-certificate -c "cursor-inner Local CA" ~/Library/Keychains/login.keychain-db` |
   | Linux (NSS) | `certutil -d sql:$HOME/.pki/nssdb -D -n "cursor-inner Local CA"` |
   | Debian / Ubuntu | `sudo rm /usr/local/share/ca-certificates/cursor-inner.crt && sudo update-ca-certificates --fresh` |
   | Fedora / RHEL | `sudo rm /etc/pki/ca-trust/source/anchors/cursor-inner.pem && sudo update-ca-trust` |
   | Arch and others | `sudo trust anchor --remove ~/.config/cursor-inner/ca/ca.crt` |

3. Delete the data directory and the program file. If you used the installer, also delete `cursor-inner.lnk` from the Start menu (Windows), or `~/.local/share/applications/cursor-inner.desktop` and `~/.local/share/icons/hicolor/scalable/apps/cursor-inner.svg` (Linux).

</details>

<details>
<summary><b>Are the binaries code-signed?</b></summary>

No. Installing with the installer avoids system prompts; browser downloads need to be allowed as described under "Manual download". Console messages are currently Chinese only.

</details>

## Development

Requires Go 1.26 or later.

```bash
go test ./...
./build.sh windows                   # writes dist/cursor-inner.exe
./build.sh darwin arm64              # writes dist/cursor-inner-darwin-arm64
VERSION=v0.1.0 ./build.sh linux      # stamps a version
```

For Windows amd64 builds, `build.sh` installs [rsrc](https://github.com/akavel/rsrc) to embed the icon into the exe. Pushing a `v*.*.*` tag makes GitHub Actions test, build all five platform targets and publish a release from the matching entry in [CHANGELOG.md](CHANGELOG.md).

<details>
<summary><b>Project layout</b></summary>

```text
cmd/cursor-inner/      Entry point: single instance, flags, dialogs, window icon, exit cleanup
internal/
├── agent/             Decides per model id whether a chat runs locally or upstream
├── app/               Operations behind the settings page
├── autostart/         Start at login: Windows logon task, macOS LaunchAgent, Linux XDG autostart
├── catalog/           Entries appended to Cursor's model catalog
├── config/            Reads and writes config.json
├── console/           Pinned console header and log output
├── cursorsettings/    Edits and restores Cursor's settings.json
├── dialer/            Direct or socks5 / http proxied dialing
├── i18n/              Bilingual (Chinese / English) text for the settings page
├── mitm/              Local proxy: decrypts *.cursor.sh and routes requests
├── protox/            Connect frames and protobuf field encoding
├── provider/          Calls OpenAI Chat / Anthropic endpoints
├── takeover/          Takeover and restore, root certificate, closing Cursor, exit watchdog
└── web/               Settings page and HTTP API
assets/                Icon
docs/images/           Images used by the README
install.sh             macOS / Linux installer
install.ps1            Windows installer
scripts/
├── make_icon.py       Generates assets/icon.svg and icon.ico (needs Pillow, ImageMagick)
├── make_readme_art.py Generates the README banner, diagram and window frames (needs Pillow)
└── gen_notices.sh     Generates THIRD_PARTY_NOTICES.md from the built binary
```

</details>

## Acknowledgements

- [cursor-byok](https://github.com/leookun/cursor-byok): cursor-inner's handling of the Cursor protocol is based on its design.
- [goproxy](https://github.com/elazarl/goproxy), [gjson / sjson](https://github.com/tidwall/sjson) and the Go extended libraries. License texts of all dependencies are in [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).

## Disclaimer

cursor-inner is an independent third-party project. It is not affiliated with, endorsed by, or authorized by Anysphere, Inc., the maker of Cursor. "Cursor" is a trademark of its owner.

This tool uses a local proxy to modify the model list received by the Cursor client and to intercept chat requests when a custom model is selected. This may not comply with Cursor's Terms of Service. You are responsible for reading and following the terms of Cursor and of the model providers you use, and for any consequences such as account restrictions. The software is provided "as is", without warranty of any kind.

---

<p align="center">
  <img src="assets/icon.svg" width="28" alt=""><br>
  <sub><a href="LICENSE">MIT</a> © 2026 CSGrandeur · <a href="CHANGELOG.md">Changelog</a> · <a href="SECURITY.md">Security</a></sub>
</p>
