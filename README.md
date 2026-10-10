<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/banner-dark.svg">
    <img src="docs/images/banner-light.svg" alt="cursor-inner：在 Cursor 里接入自己的模型，官方模型照常使用" width="100%">
  </picture>
</p>

<p align="center"><sub>在 Cursor 里用上你自己的模型 · 官方模型照常 · 代理接管零泄漏 · 自更新热交接</sub></p>

<p align="center">
  <a href="https://github.com/CSGrandeur/cursor-inner/releases/latest"><img src="https://img.shields.io/github/v/release/CSGrandeur/cursor-inner?style=flat-square&color=ff5a2b&label=release" alt="Release"></a>
  <a href="https://github.com/CSGrandeur/cursor-inner/releases"><img src="https://img.shields.io/github/downloads/CSGrandeur/cursor-inner/total?style=flat-square&color=151514&label=downloads" alt="Downloads"></a>
  <a href="https://github.com/CSGrandeur/cursor-inner/stargazers"><img src="https://img.shields.io/github/stars/CSGrandeur/cursor-inner?style=flat-square&color=151514&label=stars" alt="Stars"></a>
  <a href="#安装"><img src="https://img.shields.io/badge/platform-Windows%20%7C%20macOS%20%7C%20Linux-151514?style=flat-square" alt="Platform"></a>
  <a href="go.mod"><img src="https://img.shields.io/badge/Go-1.26-151514?style=flat-square&logo=go&logoColor=white" alt="Go"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/CSGrandeur/cursor-inner?style=flat-square&color=151514" alt="License"></a>
</p>

<p align="center">
  <b>简体中文</b> · <a href="README.en.md">English</a>
  <br>
  <a href="#安装">安装</a> · <a href="#功能">功能</a> · <a href="#工作原理">工作原理</a> · <a href="#常见问题">常见问题</a>
</p>

<br>

cursor-inner 是一个本地小工具。它把你配置的 OpenAI Chat 或 Anthropic 兼容接口加进 Cursor 的模型列表：选中这些模型时，对话由本机直接调用你的接口；选官方模型时，请求原样发往 Cursor。支持 Windows、macOS 和 Linux 版的 Cursor，配置页提供中文和英文界面。

<p align="center">
  <img src="docs/images/window-zh-light.png" alt="cursor-inner 配置页" width="92%">
</p>

## 功能

<table>
  <tr>
    <td width="33%" valign="top">
      <b>🧩 自定义模型</b><br>
      OpenAI Chat Completions 和 Anthropic Messages 两种接口，在网页上添加、测试、删除，追加在 Cursor 模型列表末尾。
    </td>
    <td width="33%" valign="top">
      <b>🛡️ 官方模型照常</b><br>
      模型目录只追加、不替换。官方模型的请求除了经过出站代理，不做任何改写。
    </td>
    <td width="33%" valign="top">
      <b>📊 连通性测试</b><br>
      流式请求 1 到 120 的数字，记录 tokens/s、首字延迟、总耗时和输出 token 数，结果随配置保存。
    </td>
  </tr>
  <tr>
    <td width="33%" valign="top">
      <b>🌐 出站代理</b><br>
      只填一个地址。没写协议时自动识别 socks5 或 http，留空则直连。每个自定义模型单独决定是否走它。
    </td>
    <td width="33%" valign="top">
      <b>♻️ 可靠还原</b><br>
      Windows 上点窗口关闭按钮会把窗口收回通知区域，进程继续运行。右键图标选「退出」，或在配置页点「退出」，才会结束并还原设置。崩溃或被强制结束时，也会撤掉接管设置，并把你原有的 <code>http.proxy</code> 等设置写回。
    </td>
    <td width="33%" valign="top">
      <b>💻 三个平台</b><br>
      Windows、macOS、Linux 都能接管、信任证书和开机启动；单实例运行，配置页可切换中文和英文。
    </td>
  </tr>
  <tr>
    <td width="33%" valign="top">
      <b>⬆️ 自动更新</b><br>
      启动时检查新版本，一键更新：新进程热接过同一组本机端口，Grok 和 Cursor 不重启，按 sha256 校验，失败自动回滚，绝不回退成直连。
    </td>
    <td width="33%" valign="top">
      <b>🔒 零泄漏出口</b><br>
      可选 TUN 模式或严格模式，把官方端点（grok、x.ai、Cursor、cursorvm）在网络层强制走代理，连应用写死的 HTTP/2 直连也拦得住。
    </td>
    <td width="33%" valign="top">
      <b>🛠️ Agent 工具链</b><br>
      自定义模型可读写文件、运行命令、搜索、调用本轮 MCP 与子代理，带 diff 审阅、循环防护、上下文压缩与备用模型切换。
    </td>
  </tr>
</table>

自定义模型做多轮工具调用时，思考内容按接口处理。DeepSeek、Kimi、MiMo 会把思考原文带回下一轮。不接受该字段的 OpenAI 兼容接口会去掉它。Anthropic 回放带签名的思考块。只读工具可以并行，写文件按顺序执行。过长的工具输出会截断。工具参数包在代码块里、末尾多一个逗号，或括号没闭合时，会先修好再执行。限流、5xx 和流中断只在还没有输出文字时重试。系统提示词不写当前时间，时间写在每一轮用户消息里。上下文缓存能否命中，要看接口是否返回缓存命中数。有的 OpenAI 兼容接口只返回输入、输出和总 token。

## 安装

**macOS / Linux**

```bash
curl -fsSL https://raw.githubusercontent.com/CSGrandeur/cursor-inner/main/install.sh | sh
```

**Windows**（PowerShell）

```powershell
irm https://raw.githubusercontent.com/CSGrandeur/cursor-inner/main/install.ps1 | iex
```

安装脚本自动识别系统和 CPU 架构，下载最新版本，按 Release 里的 `SHA256SUMS.txt` 校验后装到当前用户目录，不需要管理员权限。用脚本下载的程序不带浏览器的下载标记，SmartScreen 和 Gatekeeper 不会拦截。指定版本可以设置环境变量 `CURSOR_INNER_VERSION=v0.4.0`。

<details>
<summary><b>装到了哪里</b></summary>

| 平台 | 安装位置 | 启动方式 |
| --- | --- | --- |
| Windows | `%LOCALAPPDATA%\Programs\cursor-inner\` | 开始菜单里的 cursor-inner |
| macOS | `~/.local/bin/cursor-inner` | 在终端运行 `cursor-inner` |
| Linux | `~/.local/bin/cursor-inner` | 应用菜单里的 cursor-inner，或在终端运行 |

</details>

<details>
<summary><b>首次运行：信任本机证书</b></summary>

首次接管时，cursor-inner 会生成一张只属于本机的根证书「cursor-inner Local CA」，并请求系统信任它。不信任这张证书，Cursor 无法连接本机代理。

| 平台 | 会看到什么 |
| --- | --- |
| Windows | 弹窗询问是否安装证书，选「是」 |
| macOS | 要求输入登录密码，把证书写入登录钥匙串并设为受信任 |
| Linux | 弹出 polkit 授权框，授权后写入系统证书库；缺少 `certutil` 时在同一次授权里自动安装 |

Linux 上证书写入两处：系统证书库，以及 NSS 用户库 `~/.pki/nssdb`。系统证书库支持 Debian / Ubuntu、Fedora / RHEL、openSUSE 和 Arch 等使用 p11-kit 的发行版。没有图形界面或没有 `pkexec` 时，配置页和终端会给出可直接复制执行的 `sudo` 命令。关闭 Cursor 需要 `pgrep` / `pkill`（procps，各发行版通常自带）。

</details>

<details>
<summary><b>手动下载</b></summary>

从 [Releases](https://github.com/CSGrandeur/cursor-inner/releases/latest) 下载：

| 平台 | 文件 |
| --- | --- |
| Windows 10 / 11 | `cursor-inner-<版本>-windows-amd64.exe` |
| macOS（Apple 芯片 / Intel） | `cursor-inner-<版本>-darwin-arm64.tar.gz` / `darwin-amd64.tar.gz` |
| Linux（x86_64 / ARM64） | `cursor-inner-<版本>-linux-amd64.tar.gz` / `linux-arm64.tar.gz` |

浏览器下载的 exe 带有下载标记。Windows 第一次双击会弹出「打开文件 - 安全警告」，写着「无法验证发布者」。这是下载标记，不是程序损坏。安装脚本会去掉这个标记，所以用脚本安装不会弹出这张框。手动下载的文件，在 PowerShell 里对它执行 `Unblock-File .\cursor-inner-<版本>-windows-amd64.exe` 后再打开。程序没有代码签名证书，「未知发布者」这几个字要等有证书才能变成发布者名称。macOS 上先执行 `xattr -d com.apple.quarantine cursor-inner` 再运行。每个版本附带 `SHA256SUMS.txt` 和 SPDX 格式的 SBOM。

</details>

## 使用

1. 运行 cursor-inner。它会关闭正在运行的 Cursor，并在浏览器里打开配置页。配置页上的「启动 Cursor」会再打开 Cursor，「启动 Grok」会打开已安装的 Grok Bot（已在运行则不再开一份）。两个按钮在配置页顶栏，各带 Cursor、Grok Bot 自己的应用图标；都不退出 cursor-inner。
2. 添加模型：填写显示名、类型、模型名、接口地址和密钥，点「测试」确认能连通。已添加的模型可以编辑；复制会把这项填进添加表单，密钥需要重新填写；添加表单可以清空。编辑时密钥留空表示不改。上下文窗口和最大输出可以留空；填了之后，压缩历史按这个窗口计算，每次请求的输出不超过这个上限。打开「推理」后，模型菜单里可以选择力度。OpenAI 兼容接口还可以打开 Fast，选中后这一轮请求带 `service_tier`。要让 Agent 生成图片，在「出图」里填 OpenAI 兼容的出图接口。
3. 重新打开 Cursor，新开一个对话，在模型列表里选择刚添加的模型。
4. 用完后退出。Windows 上点窗口关闭按钮只会把窗口收回通知区域，右键图标选「退出」才结束；也可以在配置页点「退出」。macOS 和 Linux 上关闭终端窗口同样会退出。Cursor 的设置会自动还原。

> [!IMPORTANT]
> 选 **Auto** 时 Cursor 只使用官方模型。要使用自定义模型，必须在模型列表里手动选择。

## 工作原理

```mermaid
flowchart LR
    Cursor -->|http.proxy| CI["cursor-inner 本机代理"]
    CI -->|自定义模型| YourAPI["你的 OpenAI / Anthropic 接口"]
    CI -->|官方模型| Official["Cursor 官方服务"]
    CI -. 其他主机 .-> Tunnel["直接隧道转发"]
    Grok["Grok Bot"] -->|HTTPS_PROXY / --proxy-server| Proxy["你配置的出站代理"]
    CI -. 可选 TUN / 严格模式 .-> Proxy
```

接管时，cursor-inner 修改 Cursor 的 `settings.json`：`http.proxy` 指向本机代理，`http.proxySupport` 设为 `override`，并关闭 HTTP/2。原有的同名设置先备份，还原时写回。

本机代理只解密发往 `*.cursor.sh` 的连接，其余主机直接隧道转发。对话请求按模型 id 分流：属于自定义模型的由本地处理，其余原样转给官方。

接管 Grok 时，Grok Bot 固定走配置的 HTTP 代理：窗口用 `--proxy-server`（不加 `--proxy-pac-url` / `--proxy-auto-detect` / `--no-proxy-server`，也不带 `direct://` 回退），并 `--disable-quic`；主进程 undici（含登录轮询）用 `HTTPS_PROXY` / `HTTP_PROXY` / `ALL_PROXY` / `GRPC_PROXY`（大小写两份），并设 `NODE_USE_ENV_PROXY=1` 与 `--use-env-proxy`。`NO_PROXY`/`no_proxy` 与 `--proxy-bypass-list` 只含回环地址，**不**继承进程或系统里已有的 `no_proxy`（PAC 模式下宽泛的系统绕过会让部分请求直连，云功能可能误报额度用尽）。

不改系统 hosts。设置页有「接管 Cursor」和「接管 Grok」，默认都开。Cursor 里会读代理设置的流量进入本机代理，再从你配置的代理出去。这个设置正在运行的 Cursor 读不到，所以开关会关掉它，要自己重新打开，或用顶栏的「启动 Cursor」。正在运行的 Grok Bot 会按当前选择关掉并重新打开。地址只取主机和端口，按 HTTP 代理连接。没打开的 Grok Bot 不会被拉起；配置页的「启动 Grok」才会冷启动，接管开着时带上同一套参数。不读代理、自己直连的进程这次不覆盖。选中自定义模型时，Analytics、Nudge 一类请求里若仍带着本地模型 id，转发前会换成等长占位，本机先记住当前模型，Cmd+K 仍能用。

验证 Grok 出口是否走配置代理：把系统代理设成 PAC（例如 v2rayN 的 PAC 模式，不要用全局），在 cursor-inner 里打开接管 Grok 并填好出站代理，用配置页「启动 Grok」冷启动或让接管重开一次。在代理软件的连接日志里应看到 Grok / xAI / Cursor 相关域名的 CONNECT，而不是直连。自动化对照见仓库内 `internal/grokbot` 的 `TestProxyEnvForcesCONNECTThroughProxy`（在继承 `NO_PROXY=*` 时仍对目标发 CONNECT）。自定义模型若关闭了「走代理」，调试模式下也仍直连，这是刻意行为。

退出 cursor-inner 时不会改动 Grok Bot：它直接连你配置的代理，不经过 cursor-inner，所以退出后照样走代理。要撤掉 Grok 的代理，在配置页关掉「接管 Grok」。WebRTC 不走代理时就不发 UDP（`--force-webrtc-ip-handling-policy=disable_non_proxied_udp`）。

代理带用户名和密码、或是 socks 代理时，Grok Bot 自己用不了。这时 cursor-inner 在本机回环地址上开一个代理桥，经你配置的代理（含认证）转发。上游代理不通时代理桥返回 502，不会直连。cursor-inner 没在运行时，这种情况下的 Grok Bot 会连不上，而不是改成直连。

检查是否有遗漏：`scripts/grok-leak-check.ps1` 采样 Grok Bot 与 Cursor 所有进程的出站连接。它在运行时经代理用 DoH 解析官方域名（grok、x.ai、Cursor 及 cursorvm 等），再结合 TLS 证书名和反向解析，把直连官方端点标为 FAIL，GitHub、Google 等第三方直连标为 INFO。Electron 主进程和 Cursor 的部分 Node 服务直接用 socket，不认代理参数。要让官方端点一条不漏，可在配置页「严格模式」点开启（弹 UAC），或以管理员身份运行 `scripts/strict-egress.ps1 -Enable`：防火墙为 Grok Bot.exe 和 Cursor.exe 阻止到官方 IP 的直连，计划任务开机时和每 30 分钟重新解析。局限：按 IP 拦截，Cloudflare 等共享 CDN 上同 IP 的其他网站也会被这两个程序直连拦下（走代理不受影响）；清单外的新域名、两次刷新之间换的新 IP 会漏；不改 hosts。`-Disable` 撤销，`-Status` 查看。

关于仍然直连官方端点的两条连接（Cursor 的 `api3.cursor.sh`、Grok 的 `us*.cursorvm.com`）：已查明无法从应用外部把它们逼进代理。开启 `cursor.general.disableHttp2`（接管 Cursor 时已设）后，Cursor 的主 API 流量已经走代理；剩下这条 api3 直连是 Cursor 的指标/遥测客户端，Grok 那条是它连云端 agent 网关的探测。两者都是写死的 HTTP/2 客户端、不支持代理，且跑在各自独立的 Electron Node 进程里：外挂的 UI 扩展加载到的扩展宿主和发起这些请求的宿主不是同一个，`NODE_OPTIONS=--require` 又被 Electron 的 `nodeOptions` fuse 清掉，所以 `tls.connect` 注入和 `HTTPS_PROXY`/`NODE_USE_ENV_PROXY` 都够不到它们（已在测试实例上验证）。要接管只能改厂商自带的程序包。这两条的「零泄漏」保证仍然靠严格模式（上一轮的官方 IP 防火墙）。

TUN 模式（配置页「TUN 模式」点开启，弹 UAC）：cursor-inner 管理一个最小化的 sing-box TUN，只在网络层捕获官方域名（grok、x.ai、Cursor、cursorvm 等）并转进你配置的代理，连应用写死的 HTTP/2、以及上面那两条 `api3.cursor.sh` / `cursorvm` 直连也会被拦进代理，做到零泄漏。其它流量完全不碰：只有 fake-ip 段和你的真实 DNS 被路由进 TUN，其余域名照旧用真实 DNS 解析成真实 IP 并直连，LAN、WSL、v2rayN 均不受影响。管理员权限只在你确认后经 UAC 申请。若你拒绝授权、UAC 失败、sing-box 起不来或 TUN 中途退出，cursor-inner 继续用普通代理接管（env、flags、桥）工作，状态栏显示「TUN 未开启，官方端点可能泄漏」，并仍提供严格模式——绝不阻断启动或改坏网络。配置由 internal/tunnel 生成并通过 sing-box 1.14.3 校验；首次使用时把校验过哈希的 sing-box 下载到 cursor-inner 数据目录。

配置页只接受本机地址访问，并拒绝来自其他网站的改动请求，防止网页借浏览器操作本机配置。


<details>
<summary><b>设置项速览</b></summary>

配置页从上到下的开关与按钮，均可随时开关、互不影响：

- **更新** — 启动时检查一次 GitHub Release。更新时新版本接过同一组本机端口，Grok 和 Cursor 不需要重启；新版本没通过健康检查会自动回滚。 按钮「检查更新」/「更新」。
- **TUN 模式** — 用 sing-box TUN 只把官方端点（grok、x.ai、Cursor、cursorvm 等）在网络层转进当前代理，连应用自己绕过代理的 HTTP/2、写死 IP 也拦得住；其它网站的解析和直连完全不变。需要管理员授权（UAC）。失败会自动回退到普通代理接管。 「开启」/「关闭」。
- **严格模式** — 用 Windows 防火墙禁止 Grok Bot 和 Cursor 直连官方端点（grok、x.ai、Cursor），GitHub、Google 等第三方不受影响。需要管理员授权；按 IP 拦截，共享 CDN 上同 IP 的其他网站也会被这两个程序直连拦下。 「开启」/「关闭」。
- **日志与诊断** — 「打开日志」打开日志文件夹，「泄漏检查」在开发者版本里跑一次代理泄漏检查。
- **每个模型** — 可填「缓存键」与「备用模型」；能力面板右上角「关闭」关闭。

</details>

## 命令行窗口

配置页地址和运行状态固定在窗口顶部，不会被日志刷走。按回车即可打开配置页。在 Windows 上双击打开时，单击顶栏里的地址也会打开。在 Windows Terminal、iTerm2、GNOME Terminal 里，按住 Ctrl 或 Cmd 单击地址。自定义模型每一轮开始和结束各记一行。

```text
 ▌▐ cursor-inner v0.4.0   http://127.0.0.1:52341   按回车打开配置页
    接管 ● 接管中    出站 代理 socks5://127.0.0.1:1080    自定义模型 3
    模型列表 ✓ 3 · 20:31    本地 4    官方 12    最近错误 无
──────────────────────────────────────────────────────────────────────
20:31:07  接管代理 http://127.0.0.1:61022
20:31:07  已结束 Cursor，重新打开后生效
20:32:15  ▶ 示例模型  对话 3f2a9c  第 2 轮
20:32:41  ✓ 示例模型  26.0s · 工具 3 次（Read×2, StrReplace）· 输入 18.4k / 输出 1.2k · 缓存命中 87%
```

在 Windows 上从资源管理器、开始菜单或开机启动打开时，cursor-inner 使用经典控制台窗口，任务栏显示它自己的图标；在已有的终端里输入命令启动时，仍在原终端里运行。点窗口关闭按钮会收回通知区域。

| 参数 | 作用 |
| --- | --- |
| `--no-takeover` | 只打开配置页，不修改 Cursor 设置，也不关闭 Cursor |
| `--debug` | 另起一个使用临时数据目录的实例：提供代理和配置页，不改 Cursor 设置，不关闭 Cursor，不打开浏览器 |
| `--verbose` | 把调试日志也显示在窗口里（默认只写进 `inner.log`） |
| `--data-dir <目录>` | 使用指定的数据目录 |

## 已知限制

- 网页搜索合并 Bing、DuckDuckGo 和百度的结果；百度有时只返回安全验证页，这时只剩另外两家的结果。
- 用自定义模型时，在输入框里输入 `@` 不会弹出文件菜单。可以把文件拖进对话，或用「Add to Chat」添加。
- 有的 OpenAI 兼容接口不返回缓存命中数，窗口里就看不到缓存命中率，也无法确认上下文缓存是否生效。

## 常见问题

<details>
<summary><b>选了 Auto，为什么没用到我的模型？</b></summary>

Auto 由 Cursor 官方服务决定用哪个模型，只会在官方模型中选择。要使用自定义模型，在模型列表里手动选中它。

</details>

<details>
<summary><b>自定义模型能读写文件、运行命令吗？</b></summary>

支持工具调用的模型（OpenAI Chat / OpenAI Responses / Anthropic tool use）已经可以在 Agent 模式下读文件、搜索、修改和删除文件、编辑笔记本、运行命令、更新待办、提问、切换模式、提交计划、在批准后搜索或打开网页、调用本轮对话里的 MCP 工具，以及启动子代理。设置页填了出图接口之后才会出现 GenerateImage。工作区搜索先按词找出候选，再由当前自定义模型筛选。修改会在对话里显示带 diff 的工具卡片，接受之后才会写入；StrReplace 在精确匹配失败后会尝试忽略缩进/空白，再尝试唯一高相似度片段。模型把工具调用写进正文（`<tool_call>`、DSML、JSON 代码块）时会捞出来执行。同一工具同一参数连续出现会先警告再停止。命令的输出会跟着工具卡片更新；超时后命令转到后台。对话超过上下文窗口大约七成时，会先缩短较早的工具输出，仍然放不下就先写结构化摘要再回答，并提示重读刚改过的文件。每个模型可填备用模型 id：在还没流出任何字之前，鉴权失败、持续 5xx、限流超时或上下文溢出会切到下一个。测试按钮会做能力探测（工具、推理、图片、缓存）并尽量自动勾选设置。Ask、Plan、Debug、Multitask 会带上各自的模式说明。explore 子代理只做阅读和搜索，其他子代理可以改文件。点停止会中止还没结束的工具。工具还在执行时发来的消息会并进当前这一轮：当前这个工具会做完，后面还没开始的工具先跳过，下一次模型调用就能看到这条消息。`/summarize` 会把更早的历史收成摘要，每一轮结束留下检查点，之后可以回到较早的检查点。选了自定义模型的行内编辑和终端 Cmd+K 也由这里回答。提交说明和聊天标题的请求里没有模型，仍由 Cursor 生成。Tab 补全等其余功能仍由 Cursor 官方服务提供。

</details>

<details>
<summary><b>API 密钥和其他数据存在哪里？</b></summary>

| 平台 | 数据目录 |
| --- | --- |
| Windows | `%APPDATA%\cursor-inner\` |
| macOS | `~/Library/Application Support/cursor-inner/` |
| Linux | `~/.config/cursor-inner/`（或 `$XDG_CONFIG_HOME/cursor-inner/`） |

| 文件 | 内容 |
| --- | --- |
| `config.json` | 各项开关、代理地址、自定义模型和上次测试结果。**API 密钥以明文保存**，文件只有当前用户可读。 |
| `ca/` | 本机生成的根证书和私钥 |
| `conversations/` | 自定义模型对话的检查点：每个 Cursor 会话一份状态（`.state`），内容块在 `blobs/`，含对话内容和工具结果 |
| `cursor-settings.backup.json` | 接管期间 Cursor 原有代理设置的备份，还原后删除 |
| `inner.log` | 本次运行的分流日志，每次启动时重写（超过 5MB 时上一份改存为 `inner.log.1`），不记录对话内容和密钥 |
| `watch.log` | 异常退出后自动还原的记录 |
| `takeover.on`、`listen.url`、`instance.lock` | 接管标记、当前配置页地址、单实例锁 |

cursor-inner 不收集任何数据，没有遥测、统计或自动更新请求。配置页和本机代理只监听 `127.0.0.1`。对话内容只在 Cursor、本机和你配置的模型接口之间传递，自定义模型的对话历史只保存在上面的 `conversations/` 目录里。

</details>

<details>
<summary><b>cursor-inner 意外退出，Cursor 会连不上网吗？</b></summary>

不会。接管期间有一个独立的守护进程盯着主进程，主进程崩溃或被强制结束后，它会撤掉接管设置、写回你原来的代理设置，并关闭 Cursor，重新打开 Cursor 即可正常使用。

</details>

<details>
<summary><b>Cursor 升级后还能用吗？</b></summary>

官方模型不受影响。如果 Cursor 改动了内部协议，自定义模型可能暂时不可用，需要等 cursor-inner 更新。

</details>

<details>
<summary><b>cursor-inner 会自动更新吗？</b></summary>

会。启动时检查一次 GitHub Release，也可在配置页手动检查。更新时新版本接过同一组本机端口，Grok 和 Cursor 不需要重启；下载按清单里的 sha256 校验，新版本没通过健康检查会自动回滚，且绝不回退成直连。清单可用 ed25519 签名；内置公钥后，未签名或被篡改的清单会被拒绝。

</details>

<details>
<summary><b>怎么卸载？</b></summary>

1. 如果开过开机启动，先在配置页关掉。然后点「退出」，Cursor 的设置会自动还原。
2. 删除根证书：

   | 平台 | 命令 |
   | --- | --- |
   | Windows | `certutil -user -delstore Root "cursor-inner Local CA"` |
   | macOS | `security delete-certificate -c "cursor-inner Local CA" ~/Library/Keychains/login.keychain-db` |
   | Linux（NSS） | `certutil -d sql:$HOME/.pki/nssdb -D -n "cursor-inner Local CA"` |
   | Debian / Ubuntu | `sudo rm /usr/local/share/ca-certificates/cursor-inner.crt && sudo update-ca-certificates --fresh` |
   | Fedora / RHEL | `sudo rm /etc/pki/ca-trust/source/anchors/cursor-inner.pem && sudo update-ca-trust` |
   | Arch 等 | `sudo trust anchor --remove ~/.config/cursor-inner/ca/ca.crt` |

3. 删除数据目录和程序文件。用安装脚本装的，还要删除开始菜单里的 `cursor-inner.lnk`（Windows），或 `~/.local/share/applications/cursor-inner.desktop` 和 `~/.local/share/icons/hicolor/scalable/apps/cursor-inner.svg`（Linux）。

</details>

<details>
<summary><b>程序有代码签名吗？</b></summary>

没有。用安装脚本安装不会被系统拦截；从浏览器下载时需要按「手动下载」里的说明放行。终端窗口里的提示目前只有中文。

</details>

## 参与开发

需要 Go 1.26 或更新版本。

```bash
go test ./...
./build.sh windows                   # dist/cursor-inner-windows-amd64.exe
./build.sh darwin arm64              # dist/cursor-inner-darwin-arm64
VERSION=v0.4.0 ./build.sh linux      # dist/cursor-inner-v0.4.0-linux-amd64，并写入版本号
```

构建 Windows amd64 版本时，`build.sh` 会自动安装 [rsrc](https://github.com/akavel/rsrc)，用来把图标写进 exe。推送 `v*.*.*` 标签后，GitHub Actions 会测试、编译五个平台的版本，并按 [CHANGELOG.md](CHANGELOG.md) 里的对应条目发布 Release。

<details>
<summary><b>项目结构</b></summary>

```text
cmd/cursor-inner/      程序入口
internal/
├── program/           启动流程：单实例、命令行参数、弹窗、窗口图标、退出清理
├── agent/             自定义模型的运行：请求路由、模型与工具循环、检查点、压缩、系统提示词与各模式说明
├── app/               配置页用到的各项操作
├── autostart/         开机启动：Windows 登录任务、macOS LaunchAgent、Linux XDG 自动启动
├── catalog/           生成追加到 Cursor 模型目录里的条目
├── config/            读写 config.json
├── console/           命令行窗口的固定顶栏和日志
├── cursorlaunch/      从配置页启动本机已安装的 Cursor
├── cursorpb/          由 proto/cursor/agent_v1.proto 生成的 Cursor 协议代码
├── cursorsettings/    修改并还原 Cursor 的 settings.json
├── dialer/            直连或经 socks5 / http 代理拨号
├── fsutil/            原子写文件
├── i18n/              配置页用到的中英双语文本
├── logx/              日志：窗口里的每轮摘要、inner.log 明细
├── mitm/              本机代理：解密 *.cursor.sh、分流、行内编辑与终端 Cmd+K
├── procfwd/           直连子进程转发（保留；当前接管不改 hosts、不听 443）
├── grokbot/           Windows 上发现并启动 Grok Bot，并按开关加/清代理启动参数
├── protox/            Connect 帧与 protobuf 字段读写
├── provider/          调用 OpenAI Chat / Anthropic 接口，含流式工具调用
├── tools/             模型工具调用与 Cursor 执行请求、执行结果之间的转换
├── takeover/          接管与还原、根证书、关闭 Cursor、异常退出后的守护进程
└── web/               配置页与 HTTP API
assets/                图标
docs/images/           README 用图
proto/cursor/          Cursor agent 协议定义（取自 cursor-byok），用 buf generate 生成 internal/cursorpb
install.sh             macOS / Linux 安装脚本
install.ps1            Windows 安装脚本
scripts/gen_notices.sh 根据编译产物生成 THIRD_PARTY_NOTICES.md
```

</details>

## 致谢

- [cursor-byok](https://github.com/leookun/cursor-byok)：cursor-inner 的 Cursor 协议处理参考了它的设计。
- [goproxy](https://github.com/elazarl/goproxy)、[gjson / sjson](https://github.com/tidwall/sjson) 和 Go 官方扩展库。各依赖的许可证原文见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

## 免责声明

cursor-inner 是独立的第三方项目，与 Anysphere, Inc.（Cursor 的开发商）没有关联，也未获得其认可或授权。「Cursor」是其所有者的商标。

本工具通过本机代理改写 Cursor 客户端收到的模型列表，并在选中自定义模型时拦截对话请求。这种做法可能不符合 Cursor 的服务条款。使用者应自行阅读并遵守 Cursor 以及所用模型服务商的条款，并自行承担账号受限等后果。本软件按「原样」提供，不附带任何担保。

---

<p align="center">
  <img src="assets/icon.svg" width="28" alt=""><br>
  <sub><a href="LICENSE">MIT</a> © 2026 CSGrandeur · <a href="CHANGELOG.md">更新日志</a> · <a href="SECURITY.md">安全策略</a></sub>
</p>
