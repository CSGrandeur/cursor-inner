# Security Policy

## Supported versions

Only the latest release receives security fixes.

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose **Report a vulnerability**. Do not open a public issue.

Include the cursor-inner version, your operating system, and steps to reproduce. You should receive a reply within 7 days.

## Scope

cursor-inner installs a root certificate that exists only on your machine and runs a proxy on `127.0.0.1` that decrypts traffic to `*.cursor.sh`. Issues of particular interest:

- the certificate private key or `config.json` (which stores API keys) becoming readable by other users or processes;
- the settings page or proxy becoming reachable from outside `127.0.0.1`;
- traffic other than `*.cursor.sh` being decrypted, or official-model requests being altered beyond the configured outbound proxy.

---

## 安全策略

只有最新版本会收到安全修复。

发现漏洞请通过 GitHub 私下报告：打开仓库的 **Security** 标签页，选择 **Report a vulnerability**。请不要提交公开 issue。报告中请写明 cursor-inner 版本、操作系统和复现步骤，7 天内会有回复。

特别关注的问题：证书私钥或保存 API 密钥的 `config.json` 能被其他用户或进程读取；配置页或代理能从 `127.0.0.1` 以外访问；`*.cursor.sh` 以外的流量被解密，或官方模型的请求被改动（出站代理除外）。
