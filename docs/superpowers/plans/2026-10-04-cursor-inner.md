# cursor-inner 实现计划

**目标：** 在 Windows 11 上跑一个 Go 程序。它打开本地配置页，接管 Cursor，在保留官方模型的同时追加自定义模型，并按开关把流量送进用户指定的代理。开机启动可以反复开关，结果始终同一套。

**技术：** Go 1.26，`CGO_ENABLED=0`，在 WSL2 里交叉编译 `dist/cursor-inner.exe`（`windows/amd64`）。配置页、协议拼接、开机启动计划用 `go test` 在 WSL 里验证。真正结束 `Cursor.exe`、写注册表、写任务计划只在 Windows 构建里执行。

## 目录

行数是实现后的实测（不含空行与测试）。测试文件另计。

```
cursor-inner/
├── cmd/cursor-inner/                 进程入口
│   ├── main.go                       探端口、打印地址、打开浏览器、默认接管、退出时撤掉接管
│   ├── singleton_windows.go          命名互斥量，第二个进程只打印已有地址后退出
│   ├── singleton_other.go
│   ├── console_windows.go            控制台关闭时做退出清理
│   └── console_other.go
├── internal/
│   ├── config/                       %APPDATA%\cursor-inner\config.json
│   ├── dialer/                       直连或 socks5/http 代理；地址空着视为不代理
│   ├── protox/                       Connect 帧与 protobuf 字段读写
│   ├── catalog/                      把自定义模型追加到官方目录，不替换官方字段
│   ├── provider/                     OpenAI Chat 与 Anthropic 的探测和流式文本
│   ├── agent/                        BidiAppend 决定本地还是原样转发
│   ├── cursorsettings/               改 Cursor 的 settings.json，只动接管用到的键
│   ├── autostart/                    开机启动的计划、任务 XML、Windows 落地
│   ├── mitm/                         127.0.0.1 上的 HTTP 代理；只解密 *.cursor.sh
│   ├── takeover/                     证书、结束 Cursor.exe、写设置、启停代理
│   ├── app/                          把上面几块收成配置页用的操作
│   └── web/                          配置页与 /api
├── scripts/build_windows.sh
└── dist/cursor-inner.exe             构建产物，不入库
```

## 启动

```
cursor-inner.exe
        |
        v
命名互斥量已存在? --是--> 打印 listen.url，再打开浏览器，退出
        |
        否
        v
读 config.json（缺省：接管开，代理开关开，地址空，开机启动关）
        |
        v
127.0.0.1:0 监听配置页，终端打印 http://127.0.0.1:<端口>
打开默认浏览器
按 config 幂等同步开机启动
接管为开 --> 生成/安装用户根证书 --> 启动接管代理 --> 写入 Cursor 设置 --> taskkill Cursor.exe
```

调试时用 `cursor-inner.exe --no-takeover`。这条路径不写 Cursor 设置，不结束 Cursor，退出时也不回滚。

接管一旦写成设置，就在 `%APPDATA%\cursor-inner\takeover.on` 留下标记，并拉起一个脱离控制台的监视进程，盯着主进程句柄。正常退出、Ctrl+C、关掉控制台或 panic，都会删掉接管键、结束 `Cursor.exe`、再删掉标记。主进程被强行杀掉时，监视进程做同一件事。标记不在，监视进程不动 Cursor。标记还在而回滚没做完时，监视进程会再试一次。

## 接管与官方模型

Cursor 的 `settings.json` 在 `%APPDATA%\Cursor\User\settings.json`。接管写入：

| 键 | 值 |
| --- | --- |
| `http.proxy` | `http://127.0.0.1:<接管端口>` |
| `http.proxySupport` | `override` |
| `http.proxyKerberosServicePrincipal` | 同一个本地地址 |
| `cursor.general.disableHttp2` | `true` |
| `http.experimental.systemCertificatesV2` | `true` |

`http.noProxy` 删掉。`override` 让 Cursor 主进程、网络服务、以及带 `--useHostProxy` 的扩展宿主都走这个本地代理，不再被环境变量里的直连配置绕开。

本地代理的分流：

```
Cursor 的 HTTP(S)
        |
        v
127.0.0.1 接管代理
        |
        +-- 主机不是 *.cursor.sh --> 不解密，TCP 隧道
        |
        +-- *.cursor.sh
                |
                +-- AvailableModels / GetUsableModels
                |       先向上游要官方目录，再把自定义模型的 protobuf 接在后面
                |
                +-- BidiAppend
                |       读出模型 id
                |         自定义 --> 本地收下，稍后由本程序调用用户的接口
                |         其他   --> 原请求原样转发上游（官方 opus、gpt 等）
                |
                +-- RunSSE
                |       等 BidiAppend 的结论
                |         自定义 --> 把文本增量写成 Connect 帧
                |         其他或超时 --> 原样转发，官方对话不经过本程序的模型
                |
                +-- 其余路径 --> 原样转发
        |
        v
代理开关生效时，上面所有向外的连接都经用户填写的代理拨出
代理开关关，或开着但地址是空白 --> 直接拨出
自定义模型另有「走代理」，默认关；开着才使用同一条用户代理
```

官方默认模型不被换成自定义模型：`GetDefaultModel` 一类接口不拦截。目录接口只追加 repeated 字段，不重写上游已有的模型列表。

自定义模型这一版只回文本。工具调用、补全、账号接口仍走上游。

证书放在 `%APPDATA%\cursor-inner\ca\`，用 `certutil -user -addstore -f Root` 装进当前用户的根存储。装过一次再装仍是同一张证书。

## 代理

配置里 `proxy.enabled` 默认 `true`，`proxy.address` 默认空。

- 开关关：不代理。
- 开关开且地址空：不代理。
- 地址可写 `127.0.0.1:1080`，按 `socks5://127.0.0.1:1080` 理解。也接受 `socks5`、`socks5h`、`http`、`https`。
- 地址指向接管代理自己的端口：拒绝保存。
- 自定义模型 `use_proxy` 默认 `false`。要求走代理但全局代理未生效时，这次调用失败，不改走直连。

## 开机启动

Windows 服务不能用。服务在会话 0，打不开用户的浏览器，也碰不到当前用户的证书和 `%APPDATA%`。

采用的顺序：

1. **当前用户的登录任务**（主路径）。任务计划不受「启动应用」里那条 `StartupApproved` 禁用位影响，也能写明电池、空闲和运行时长。这些默认值在 Windows 11 上会让普通任务计划在笔记本或空闲时根本不跑，或在 72 小时后被杀掉。
2. 任务写失败时，才写 `HKCU\...\Run`，并把 `StartupApproved\Run\cursor-inner` 设为启用（首字节 `0x02`，共 12 字节）。这是备用，不和任务同时存在。
3. 启动文件夹里的 `cursor-inner.lnk` 若存在则删除。本程序不创建快捷方式，避免和前两条叠成两次启动。

任务 XML 固定为：

- 触发：当前用户登录，延迟 `PT15S`
- `LogonType` = `InteractiveToken`，`RunLevel` = `LeastPrivilege`（不提权，不弹 UAC）
- `MultipleInstancesPolicy` = `IgnoreNew`
- `DisallowStartIfOnBatteries` = false，`StopIfGoingOnBatteries` = false
- `StartWhenAvailable` = true
- `RunOnlyIfNetworkAvailable` = false，`RunOnlyIfIdle` = false
- `StopOnIdleEnd` = false
- `ExecutionTimeLimit` = `PT0S`（不限时）
- 动作：当前 exe 的绝对路径，工作目录为其文件夹
- 创建命令带 `/F`，同名任务被覆盖而不是再加一条

不设失败重启。关掉窗口应当能退出；登录成功率靠延迟、电池策略和 `StartWhenAvailable`，不靠退出后又被拉起。

幂等：

| 操作 | 结果 |
| --- | --- |
| 开，且任务已是这份 XML | 再写一次同样的任务，注册表启动项保持删除 |
| 开，但上次只能写成注册表 | 再次尝试任务；任务成功后删掉注册表项 |
| 关，本来就没有 | 删除命令遇到「找不到」视为成功 |
| 关，两种都有 | 任务、`Run`、`StartupApproved`、快捷方式都删 |
| 每次进程启动 | 按配置里的开关再执行一遍，exe 换了位置也会改到新路径 |

配置页的开关调用的是「设为开」或「设为关」，不是在未知状态上翻转。

## 配置页

`http://127.0.0.1:<端口>/` 提供四个操作：接管、代理、开机启动、自定义模型（添加、测试、是否走代理、删除）。接口在 `/api/` 下，只绑定环回地址。

## 构建

```bash
scripts/build_windows.sh
```

产物：`dist/cursor-inner.exe`。
