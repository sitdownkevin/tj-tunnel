# tjsocks

[![CI](https://github.com/sitdownkevin/tjsocks/actions/workflows/ci.yml/badge.svg)](https://github.com/sitdownkevin/tjsocks/actions/workflows/ci.yml)
[![Docker](https://github.com/sitdownkevin/tjsocks/actions/workflows/docker.yml/badge.svg)](https://github.com/sitdownkevin/tjsocks/actions/workflows/docker.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

把同济校园网 VPN 变成一台 **SOCKS5 代理服务器**，同时支持 **TCP 与 UDP**。

纯用户态实现：不需要 root、不创建 TUN 设备、不改动宿主机路由。整个程序编译成一个
约 8 MB 的静态二进制，容器镜像约 10 MB，适合放在任何一台 VPS 上长期运行，
把校园网出口共享给手机、平板、路由器等设备。

```
客户端 ──SOCKS5(TCP/UDP)──▶ tjsocks ──TLS──▶ 校园 VPN 网关 ──▶ 校内 / 公网
```

## 特性

- **TCP + UDP**：`CONNECT` 与 `UDP ASSOCIATE`（DNS、QUIC、语音、游戏都能走）
- **零特权**：用 gVisor netstack 在用户态终结隧道 IP 包，无需 root / TUN / iptables
- **代理认证**：RFC 1929 用户名密码，公网部署可安全暴露
- **自动重连**：指数退避，断线后自动重新登录
- **DNS over tunnel**：域名走校内 DNS 解析，内网域名也能正确解析，并带 TTL 缓存
- **容器安全**：distroless + 非 root + 只读根文件系统 + 无 capability
- **多架构镜像**：`linux/amd64`、`linux/arm64`（树莓派 / ARM VPS 可用）
- **零配置发布**：GitHub Actions 自动构建并推送到 GHCR，服务器一条命令拉取

## 快速开始

### 1. 服务器上部署

```sh
mkdir -p ~/tjsocks && cd ~/tjsocks
curl -fsSLO https://raw.githubusercontent.com/sitdownkevin/tjsocks/main/docker-compose.yml

vi docker-compose.yml       # 填 TJ_USER / TJ_PASSWORD / SOCKS_USER / SOCKS_PASS

docker compose up -d
docker compose logs -f      # 看到 "SOCKS5 ready" 即成功
```

只需要一个文件，所有配置都写在 `docker-compose.yml` 里（不用 `.env`）。最少要改这四处：

```yaml
environment:
  TJ_USER: "你的学号"
  TJ_PASSWORD: "你的密码"
  SOCKS_USER: "proxyuser"
  SOCKS_PASS: "换一个强密码"
```

镜像来自 GitHub Container Registry：`ghcr.io/sitdownkevin/tjsocks:latest`
（公开包，拉取无需登录）。

> 想锁定版本就改成 `ghcr.io/sitdownkevin/tjsocks:0.1.0`。
> 升级：`docker compose pull && docker compose up -d`。

### 2. 其他设备上使用

在 Clash / mihomo / sing-box / v2rayN / 系统代理里添加一个 SOCKS5 节点：

| 项目 | 值 |
| --- | --- |
| 服务器 | 你的服务器 IP |
| 端口 | `1080` |
| 用户名 / 密码 | compose 里的 `SOCKS_USER` / `SOCKS_PASS` |
| UDP | 打开 |

命令行验证：

```sh
# TCP
curl -x socks5h://proxyuser:密码@服务器IP:1080 https://www.tongji.edu.cn/
# 出口 IP（应当是校园网地址）
curl -x socks5h://proxyuser:密码@服务器IP:1080 https://api.ip.sb/ip
```

> `socks5h` 表示让代理解析域名，校内域名才能解析到内网地址。

## 配置

### docker-compose.yml 配置项

全部写在 `docker-compose.yml` 的 `environment:` 里，改完 `docker compose up -d` 生效。

| 配置项 | 说明 | 默认 |
| --- | --- | --- |
| `TJ_USER` / `TJ_PASSWORD` | 校园 VPN 账号密码（**必填**） | — |
| `SOCKS_USER` / `SOCKS_PASS` | 代理认证（公网部署务必设置） | 无认证 |
| `SOCKS_LISTEN` | 容器内 TCP 监听地址 | `0.0.0.0:1080` |
| `SOCKS_UDP_LISTEN` | 容器内 UDP 中继监听地址 | 同 TCP 端口 |
| `SOCKS_UDP_ADVERTISE` | 返回给客户端的 UDP 中继地址 | `0.0.0.0:<端口>` |
| `TJ_GATEWAY` | VPN 网关域名 | `vpn.tongji.cn` |
| `TJ_GATEWAY_ADDRESS` | 网关 `IP:端口`，DNS 解析不了时直连 | 空 |
| `TJ_AUTH_METHOD` | 认证方式 | `LDAP3` |
| `TJ_LOG_LEVEL` | `debug` / `info` / `warn` / `error` | `info` |

### 改宿主机端口

只改 `ports:` 左边：

```yaml
ports:
  - "7891:1080/tcp"
  - "7891:1080/udp"
```

### 关于 UDP 通告地址

`UDP ASSOCIATE` 的响应里要告诉客户端「往哪里发 UDP 包」。默认返回
`0.0.0.0:<端口>`，Clash / mihomo / sing-box / v2rayN 会自动把它替换成
已连接的代理地址，因此在 Docker 端口映射下开箱即用。

若你的客户端不认 `0.0.0.0`，就显式指定公网地址：

```ini
SOCKS_UDP_ADVERTISE=203.0.113.10:1080
```

### 不用 Docker

```sh
go build -mod=vendor -o tjsocks ./cmd/tjsocks

./tjsocks -u 学号 -p 密码 -listen 0.0.0.0:1080 \
          -socks-user proxyuser -socks-pass 密码
```

命令行参数与环境变量等价，优先级：默认值 → `~/.tjsocks.json` → `-config`
→ 环境变量 → 命令行参数。

```
-u, -p                   校园 VPN 账号密码
-listen                  SOCKS5 TCP 监听（默认 :1080）
-udp-listen              UDP 中继监听（默认与 -listen 同端口）
-udp-advertise           返回给客户端的 UDP 地址
-socks-user/-socks-pass  SOCKS5 认证
-config                  JSON 配置文件
-log-level               日志级别
-healthcheck <addr>      探测本地 SOCKS5（容器健康检查用）
-version                 打印版本
```

## 工作原理

```
                 ┌──────────────── tjsocks ────────────────┐
SOCKS5 客户端 ──▶│ socks.Server                            │
                 │   ├─ CONNECT   ──▶ netstack.DialTCP ──┐  │
                 │   └─ UDP ASSOC ──▶ netstack.DialUDP ──┤  │
                 │                                       ▼  │
                 │                          gVisor netstack │
                 │   resolver（DNS over TCP，走隧道）     │
                 └───────────────────┬────────────────────┘
                                     │ 原始 IPv4 包
                                     ▼
                        TLS 隧道 ──▶ 校园 VPN 网关
```

1. 用账号密码登录网关，把 HTTPS 连接升级成承载 IPv4 包的隧道（见
   [docs/protocol.md](docs/protocol.md)）。
2. 隧道里的 IP 包被灌进 gVisor netstack —— 一个用户态的 TCP/IP 栈。
3. SOCKS5 的 `CONNECT` / `UDP ASSOCIATE` 通过这个栈拨号，因此完全不需要
   操作系统层面的网卡和路由。
4. 域名解析走网关下发的校内 DNS，保证内网域名可用。

## 开发

```sh
make help          # 所有可用任务
make build         # 编译到 bin/tjsocks
make test          # 单元测试
make test-race     # 竞态检测
make lint          # gofmt 检查 + go vet
make docker        # 构建镜像
```

依赖已 vendor（`vendor/`），因此离线也能构建。

> **为什么要 vendor**：gvisor 仓库里 `pkg/tcpip/stack/bridge_test.go` 的包名与
> 所在目录不一致（上游用 bazel 构建，不受影响），直接 `go build` 会报
> `found packages stack and bridge`。`vendor/` 不包含 `_test.go`，正好绕开。
> 所以**必须**带 `-mod=vendor`。

第一次 fork 后把模块路径改成自己的：

```sh
make rename MODULE=github.com/你的用户名/tjsocks
```

### 发布

- 推送到 `main`：构建并推送 `latest` 与 `sha-xxxx` 标签
- 打 `v*.*.*` 标签：额外推送语义化版本标签
- Pull Request：只构建 `linux/amd64` 验证，不推送

镜像推送到 GitHub Container Registry（`ghcr.io/<owner>/<repo>`），使用仓库自带的
`GITHUB_TOKEN`，**不需要配置任何 Secret**。

首次推送后到仓库 **Packages** 页面把包的可见性设为 `Public`，其他人（包括你的
服务器）就能免登录拉取；私有仓库则需要在服务器上 `docker login ghcr.io`。

## 故障排查

| 现象 | 处理 |
| --- | --- |
| 日志 `authentication failed` | 账号或密码错；确认认证方式是 `LDAP3`（`TJ_AUTH_METHOD`） |
| 日志 `gateway terminated the session (reason 1)` | 同一账号在别处登录了，顶掉了本会话。**一个账号同时只能有一个会话** |
| 一直 `reconnecting` / `no such host` | 服务器 DNS 解析不了 `vpn.tongji.cn`，在 compose 里取消注释并设置 `TJ_GATEWAY_ADDRESS: "202.120.189.34:443"` |
| TCP 正常、UDP 不通 | 确认 compose 里 `1080/udp` 也映射了；必要时设 `SOCKS_UDP_ADVERTISE` |
| 容器 `unhealthy` | `docker logs` 查看是否还在重连；健康检查 30 s 一次 |
| 校内域名解析到公网地址 | 客户端要用 `socks5h`（域名交给代理解析） |

## 安全

- 公网部署**必须**设置 `SOCKS_USER` / `SOCKS_PASS`，否则等于开放代理。
- 账号密码只存在于 `docker-compose.yml` 的 `environment:` 里，不会写进镜像。
  真实凭证不要提交到公开仓库（本仓库的 compose 文件只有占位符）。
- 容器以非 root 运行，根文件系统只读，丢弃全部 capability。
- 详见 [SECURITY.md](SECURITY.md)。

## 免责声明

本项目仅供同济大学师生访问校内资源使用。请遵守校园网使用规定，不要将校园网
出口用于违规用途或转售给他人。因使用本项目产生的一切后果由使用者自行承担。

## 许可证

[MIT](LICENSE)。协议细节参考自
[Open TJ](https://github.com/sitdownkevin/open_tj)（MIT）。
