<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/banner-dark.svg">
    <img src="docs/images/banner-light.svg" alt="cursor-inner：在 Cursor 里接入自己的模型，官方模型照常使用" width="100%">
  </picture>
</p>

<p align="center"><sub>自己的模型用 Cursor 的 Agent · Cursor、Grok、Codex 走你的代理</sub></p>

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
  <a href="#安装">安装</a> · <a href="#功能">功能</a> · <a href="#使用">使用</a> · <a href="#常见问题">常见问题</a>
</p>

<br>

cursor-inner 在你的电脑上运行。自定义模型出现在 Cursor 的列表里，用 Cursor 的 Agent 读改文件、运行命令、搜索，调用当前对话里的 MCP 和子代理；选官方模型时，请求原样发往 Cursor。Cursor、Grok、Codex、反重力走你填的一个代理地址，不改系统代理，也不开系统 TUN 带上其它程序。退出或意外结束时，Cursor 原来的代理设置会写回。Windows、macOS、Linux 可用，配置页中英文可切换。

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/window-zh-dark.png">
    <img src="docs/images/window-zh-light.png" alt="cursor-inner 配置页" width="92%">
  </picture>
</p>

模型接口可能另行计费。cursor-inner 是独立项目，与 Cursor 及其开发商没有关联。

## 功能

- **自己的模型。** OpenAI Chat、OpenAI Responses、Anthropic 三种接口。在配置页添加、测试、编辑、删除，通过后追加在 Cursor 模型列表末尾。
- **模型分组。** 同一家供应商的模型放进一个分组，统一填接口地址、类型、密钥，组里每个模型只配模型名、上下文窗口等其余项。分组可折叠，只看名称、接口和密钥摘要。一个组可以存多套套餐（同一平台的多套地址与密钥，组头下拉切换。比如按量付费和 Token Plan），点“添加套餐”维护，组内模型跟着换。每个模型仍可单独配：表单里改哪个字段，哪个字段就成了它自己的；表格里拨过的开关旁有复位，点一下恢复跟随分组。删组时组内模型转为独立模型，配置保留。
- **探测上游模型。** 填好地址和密钥，点模型名旁的「探测模型」，从上游列表里点选模型名。
- **连通性测试。** 测试探测工具调用、推理、图片、缓存并记录速度；表下列出阶段、进度条和实时输出 token 数，测试中可点“取消”；结果随配置保存。
- **官方模型照常。** 列表只追加、不替换；官方模型的请求原样发往 Cursor。同一对话里换回官方模型，不受先前自定义模型的影响。
- **Agent 能干活。** 读改文件、运行命令、搜索、调用本轮对话的 MCP 和子代理；改动先看 diff，接受后才写入。用量条与官方模型同一套。
- **长任务能接着跑。** 模型可以在本机留一条后台命令，命令结束自动接着回答；也可以在某行输出出现时被叫醒。
- **停得清楚。** 点停止显示「已停止」；上游拒绝或报错会说明原因，已完成的步骤和检查点保留。热更新或重启后，已答完的对话能正常恢复。
- **走你的代理。** 接管 Cursor、Grok、Codex、反重力，填一个地址即可；也可以只让官方地址走代理（含反重力），其余直连。接管反重力时，它的 WSL 模式也会走代理，不改系统或 WSL 设置。每个自定义模型单独决定是否走代理。接管后仍有个别直连时，TUN 模式或严格模式只覆盖官方地址（TUN 也可覆盖反重力及其 WSL 流量），不动其它程序。
- **更新和退出。** 一键更新，Cursor 和 Grok 不用重启，只安装签名有效的版本。退出或意外结束都会还原 Cursor 的代理设置；Windows 上点关闭只收进通知区域。

## 安装

**macOS / Linux**

```bash
curl -fsSL https://raw.githubusercontent.com/CSGrandeur/cursor-inner/main/install.sh | sh
```

**Windows**（PowerShell）

```powershell
irm https://raw.githubusercontent.com/CSGrandeur/cursor-inner/main/install.ps1 | iex
```

安装脚本识别系统和 CPU，按 Release 里的 `SHA256SUMS.txt` 校验后装到当前用户目录，不需要管理员权限。指定版本：`CURSOR_INNER_VERSION=v0.6.1`。

<details>
<summary><b>装到了哪里</b></summary>

| 平台 | 安装位置 | 启动方式 |
| --- | --- | --- |
| Windows | `%LOCALAPPDATA%\Programs\cursor-inner\` | 开始菜单里的 cursor-inner |
| macOS | `~/.local/bin/cursor-inner` | 在终端运行 `cursor-inner` |
| Linux | `~/.local/bin/cursor-inner` | 应用菜单里的 cursor-inner，或在终端运行 |

</details>

<details>
<summary><b>首次运行要信任本机证书</b></summary>

第一次接管时，cursor-inner 会生成本机根证书“cursor-inner Local CA”，并请求系统信任它。不信任的话，Cursor 连不上本机代理。

| 平台 | 会看到什么 |
| --- | --- |
| Windows | 询问是否安装证书，选“是” |
| macOS | 要求登录密码，把证书写入登录钥匙串并设为受信任 |
| Linux | polkit 授权框；缺少 `certutil` 时在同一次授权里安装 |

没有图形界面时，配置页和终端会给出可复制的 `sudo` 命令。

</details>

<details>
<summary><b>手动下载</b></summary>

从 [Releases](https://github.com/CSGrandeur/cursor-inner/releases/latest) 下载对应文件：

| 平台 | 文件 |
| --- | --- |
| Windows 10 / 11 | `cursor-inner-<版本>-windows-amd64.exe` |
| macOS（Apple 芯片 / Intel） | `cursor-inner-<版本>-darwin-arm64.tar.gz` / `darwin-amd64.tar.gz` |
| Linux（x86_64 / ARM64） | `cursor-inner-<版本>-linux-amd64.tar.gz` / `linux-arm64.tar.gz` |

程序没有代码签名。用安装脚本不会被 SmartScreen 或 Gatekeeper 拦住。浏览器下载的 Windows 文件，先在 PowerShell 里执行 `Unblock-File .\cursor-inner-<版本>-windows-amd64.exe`。macOS 上先执行 `xattr -d com.apple.quarantine cursor-inner`。

</details>

## 使用

1. 运行 cursor-inner。它会关掉正在运行的 Cursor，并打开配置页。顶栏的“启动 Cursor”“启动 Grok”“启动 Codex”“启动反重力”再打开它们，不退出 cursor-inner。
2. 在“模型”里填写显示名、模型名、接口地址和密钥；同一家供应商有多个模型时，点“新建分组”，填分组名称和共享的地址、类型、密钥，再往组里添加模型。不确定模型名时点「探测模型」从上游列表里选。点“测试”验证，表下会显示阶段、进度和实时 token 数。编辑时密钥留空表示不改。
3. 重新打开 Cursor，新开一个对话，在模型列表里选刚添加的模型。Cursor 已经开着时，窗口聚焦约每 5 分钟才拉一次模型列表；想马上看到，在 Cursor 设置 → Models 点“刷新模型列表”（Refresh model list），或执行 Reload Window。
4. 用完点“退出”。Windows 上点窗口关闭按钮只会收到通知区域，右键图标选“退出”才结束。Cursor 的设置会还原。

   退出前会问一次“退出 cursor-inner？”，并列出会一并关闭的程序；勾“不再询问”以后直接退出。只关 cursor-inner 自己接管的进程，你自己另开的不动。下次打开时，配置页顶部可以一键“恢复”上次关掉的程序，照常走代理；点“忽略”不再提示。

> [!IMPORTANT]
> 选 **Auto** 时 Cursor 只用官方模型。要用自定义模型，必须在列表里手动选。

配置页四个标签：**模型**、**网络**、**通用**、**更新**。网址末尾加上 `#models`、`#network`、`#general`、`#update` 可以直接打开。

- **模型**：仅支持 Cursor。添加、探测模型和测试；列表里的密钥带“密钥”标签，点击复制；“复制”把配置连同密钥带进添加表单，密钥默认隐藏。分组在列表里按节显示，点组头折叠或展开，折叠后只剩摘要；组头可以直接给组添加模型、编辑或删除分组。组内模型的接口列标着“跟随分组”，与分组不同的字段标“自定义”，悬停可看是哪几个。组里存了多套套餐时，组头出现套餐下拉，切换后组内模型立即使用新套餐的地址、类型和密钥；在分组编辑里的“套餐”区点“添加套餐”增加一套，每套填套餐名称、类型、地址和密钥，编辑时密钥留空表示不改。表单顶部的分组下拉可以选已有分组、独立模型或新建分组；选组后地址、类型、密钥留空即跟随分组。未单独配置的字段使用分组的设置；改动后即成为这个模型的自定义值。上下文窗口、最大输出、图片张数上限、图片总量上限（MB）、推理、Fast、缓存键和备用模型都可以留空；读入的图片会先缩小，一次请求超过上限时从最旧的工具图片起省略，最新几张保留。备用模型填其它已添加模型的名称，逗号分隔，在还没输出文字时用于鉴权失败或限流。上下文窗口和最大输出旁的“?”打开一张小帮助卡。打开推理后，Cursor 的思考档列表按模型能力生成。
- **网络**：代理地址、代理范围（“全局”或“仅官方”）、接管 Cursor、接管 Grok、接管 Codex、接管反重力、TUN 模式、严格模式。代理留空则直连；只写 `主机:端口` 时自动识别 socks5 或 http。“仅官方”包括反重力的地址。接管反重力时，WSL 模式也会走代理，不改系统或 WSL 设置。TUN 模式同样可以覆盖反重力（含 WSL）的官方流量。cursor-inner 比代理软件先启动时，状态栏显示“等待代理…”，就绪后自动接上，官方流量不会直连。
- **更新**：按钮是“检查更新”和“更新”。只安装签名有效的版本，更新时 Grok 和 Cursor 不用重启；新版本没通过检查会回到当前版本。
- **通用**：出图、开机启动。Windows 上还有**后台运行**和**通知**。要让 Agent 生成图片，在“出图”里填 OpenAI 兼容接口。

<details>
<summary><b>命令行</b></summary>

| 参数 | 作用 |
| --- | --- |
| `--no-takeover` | 只打开配置页，不改 Cursor 设置，也不关闭 Cursor |
| `--debug` | 用临时数据目录再开一份：不改正在使用的 Cursor 和 Grok |
| `--verbose` | 窗口里也显示调试日志 |
| `--data-dir <目录>` | 使用指定的数据目录 |

</details>

## 流量怎么走

<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/flow-zh-dark.svg">
    <img src="docs/images/flow-zh-light.svg" alt="Cursor 经本机代理 cursor-inner 分流：自定义模型发往你的接口，官方模型原样发往 Cursor；Grok 等走你的代理，其他主机原样隧道" width="100%">
  </picture>
</p>

Cursor 里读代理设置的流量先到本机：自定义模型由本机调用你的接口，官方模型原样转发，其余主机原样隧道、不解密。Grok、Codex、反重力走你配置的代理。配置页只接受本机访问。

## 已知限制

- 网页搜索合并 Bing、DuckDuckGo 和百度。百度有时只返回验证页，这时只有另外两家的结果。
- 用自定义模型时，输入框里的 `@` 不会弹出文件菜单。把文件拖进对话，或用 Add to Chat。
- 有的接口不返回缓存命中数，窗口里就看不到缓存命中率。

## 常见问题

<details>
<summary><b>选了 Auto，为什么没用到我的模型？</b></summary>

Auto 只在官方模型里选。要使用自定义模型，在模型列表里手动选中它。

</details>

<details>
<summary><b>自定义模型能改文件、跑命令吗？</b></summary>

支持工具调用的模型可以。Agent 里能读文件、搜索、修改和删除、编辑笔记本、运行命令、调用本轮对话里的 MCP，以及启动子代理。修改先显示 diff，接受后才写入。点停止会中止还没结束的工具。提交说明、聊天标题和 Tab 补全仍由 Cursor 完成。

</details>

<details>
<summary><b>密钥存在哪里？</b></summary>

| 平台 | 数据目录 |
| --- | --- |
| Windows | `%APPDATA%\cursor-inner\` |
| macOS | `~/Library/Application Support/cursor-inner/` |
| Linux | `~/.config/cursor-inner/`（或 `$XDG_CONFIG_HOME/cursor-inner/`） |

`config.json` 里是开关、代理和自定义模型。**API 密钥以明文保存**，文件只有当前用户可读。自定义模型的对话在 `conversations/`。配置页和本机代理只监听 `127.0.0.1`。

</details>

<details>
<summary><b>cursor-inner 意外退出，Cursor 会连不上网吗？</b></summary>

不会。主进程崩溃或被强制结束后，会把你原来的代理设置写回，并关闭 Cursor。重新打开 Cursor 即可。

</details>

<details>
<summary><b>Cursor 升级后还能用吗？</b></summary>

官方模型不受影响，自定义模型也由本机回答。若遇到自定义模型回合卡住或恢复失败，更新到本版并重启一次 Cursor。

</details>

<details>
<summary><b>怎么卸载？</b></summary>

1. 开过开机启动的话，先在配置页关掉，再点“退出”。
2. 删除根证书：

   | 平台 | 命令 |
   | --- | --- |
   | Windows | `certutil -user -delstore Root "cursor-inner Local CA"` |
   | macOS | `security delete-certificate -c "cursor-inner Local CA" ~/Library/Keychains/login.keychain-db` |
   | Linux（NSS） | `certutil -d sql:$HOME/.pki/nssdb -D -n "cursor-inner Local CA"` |
   | Debian / Ubuntu | `sudo rm /usr/local/share/ca-certificates/cursor-inner.crt && sudo update-ca-certificates --fresh` |
   | Fedora / RHEL | `sudo rm /etc/pki/ca-trust/source/anchors/cursor-inner.pem && sudo update-ca-trust` |
   | Arch 等 | `sudo trust anchor --remove ~/.config/cursor-inner/ca/ca.crt` |

3. 删除数据目录和程序。用安装脚本装的，还要删除开始菜单里的 `cursor-inner.lnk`（Windows），或 `~/.local/share/applications/cursor-inner.desktop`（Linux）。

</details>

## 参与开发

需要 Go 1.26 或更新版本。

```bash
go test ./...
./build.sh windows
./build.sh darwin arm64
VERSION=v0.6.1 ./build.sh linux
```

## 致谢

Cursor 协议处理参考了 [cursor-byok](https://github.com/leookun/cursor-byok)。依赖的许可证原文见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。

---

<p align="center">
  <img src="assets/icon.svg" width="28" alt=""><br>
  <sub><a href="LICENSE">MIT</a> © 2026 CSGrandeur · <a href="CHANGELOG.md">更新日志</a> · <a href="SECURITY.md">安全策略</a></sub>
</p>
