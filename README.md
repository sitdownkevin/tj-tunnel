# TJ Tunnel

[![CI](https://github.com/sitdownkevin/TJ Tunnel/actions/workflows/ci.yml/badge.svg)](https://github.com/sitdownkevin/TJ Tunnel/actions/workflows/ci.yml)
[![Docker](https://github.com/sitdownkevin/TJ Tunnel/actions/workflows/docker.yml/badge.svg)](https://github.com/sitdownkevin/TJ Tunnel/actions/workflows/docker.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

把校园网 VPN 变成一台 **SOCKS5 代理服务器**，同时支持 **TCP 与 UDP**。

纯用户态实现：不需要 root、不创建 TUN 设备、不改动宿主机路由。整个程序编译成一个
约 8 MB 的静态二进制，容器镜像约 10 MB，适合放在任何一台 VPS 上长期运行，
把校园网出口共享给手机、平板、路由器等设备。

```
客户端 ──SOCKS5(TCP/UDP)──▶ TJ Tunnel ──TLS──▶ 校园 VPN 网关 ──▶ 校内 / 公网
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
- **实时状态面板**：React + MUI 界面，WebSocket 推送，可一键复制 SOCKS5 连接信息
- **面板内控制**：连接 / 断开 / 自动重连开关，容器默认不自动连接

## 快速开始

### 1. 服务器上部署

```sh
mkdir -p ~/TJ Tunnel && cd ~/TJ Tunnel
curl -fsSLO https://raw.githubusercontent.com/sitdownkevin/TJ Tunnel/main/docker-compose.yml

vi docker-compose.yml       # 填 TJ_USER / TJ_PASSWORD / SOCKS_USER / SOCKS_PASS

docker compose up -d
docker compose logs -f      # 看到 "SOCKS5 ready" 即成功
```

只需要一个文件，所有配置都写在 `docker-compose.yml` 里（不用 `.env`）。最少要改这四处：

```yaml
environment:
  TJ_USER: "你的账号"
  TJ_PASSWORD: "你的密码"
  SOCKS_USER: "proxyuser"
  SOCKS_PASS: "换一个强密码"
```

镜像来自 GitHub Container Registry：`ghcr.io/sitdownkevin/TJ Tunnel:latest`
（公开包，拉取无需登录）。

部署完成后可以打开状态面板看实时情况：

```
http://服务器IP:8080      # 页面内登录，账号见 WEB_USER / WEB_PASS
```

> **默认不会自动连接**：容器启动后隧道处于「已停止」，登录面板点「连接」才开始
> 建立隧道。想开机即连就把 compose 里的 `AUTOSTART` 改成 `"true"`。

不需要面板就把 compose 里的 `WEB_LISTEN` 留空、并删掉 `8080:8080/tcp` 那行端口。

> 想锁定版本就改成 `ghcr.io/sitdownkevin/TJ Tunnel:0.1.1`。
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
| `AUTOSTART` | `true` = 启动即连接；`false` = 等面板点「连接」 | `false` |
| `WEB_LISTEN` | Web 状态面板监听地址，留空关闭 | 空 |
| `WEB_USER` / `WEB_PASS` | 面板登录账号（默认沿用 SOCKS 认证） | 同 SOCKS |
| `TJ_LOG_LEVEL` | `debug` / `info` / `warn` / `error` | `info` |

### 改宿主机端口

只改 `ports:` 左边：

```yaml
ports:
  - "7891:1080/tcp"
  - "7891:1080/udp"
```

## 状态面板

开启 `WEB_LISTEN` 后，浏览器访问 `http://服务器IP:8080`，在页面里输入账号密码
登录（不是浏览器弹窗），即可看到：

- 隧道阶段（启动中 / 登录中 / 运行中 / 重连中）与本次会话时长
- 隧道 IP、网关、认证方式、校内 DNS
- 上/下行实时速率与累计流量、活跃连接数与累计连接数
- **可一键复制的 SOCKS5 连接信息**（链接、服务器、端口、UDP 支持、认证方式、用户名）
- **连接控制**：`连接` / `断开` 按钮，以及 `自动重连` 开关
- 最近一次错误（登录失败、会话被顶下线等）

实现要点：

- 前端是 **React + MUI**，构建产物用 `go:embed` 编进二进制，运行时无 Node、无 CDN
- 状态通过 **WebSocket** 每秒推送；断线自动指数退避重连，会话失效则退回登录页
- 登录态是 HMAC 签名的 HttpOnly Cookie（进程重启即失效，服务端无需存储会话）
- 静态资源带内容哈希并长期缓存，页面本身不缓存，升级后立刻生效
- WebSocket 校验 `Origin`，跨站连接被拒绝

> 面板登录默认使用 `WEB_USER` / `WEB_PASS`；未设置时沿用 `SOCKS_USER` /
> `SOCKS_PASS`。**不要**在公网把面板裸奔。

### 连接控制

| 操作 | 行为 |
| --- | --- |
| 连接 | 登录网关并建立隧道；面板立即显示「登录中 → 运行中」 |
| 断开 | 结束隧道并注销会话，代理端口仍在监听但会立即拒绝请求 |
| 自动重连 | 开启时断线会按 2s→60s 退避重试；关闭时断开即停在「已停止」 |

代理端口（SOCKS5）始终监听，隧道未建立时请求会立刻收到失败应答，不会挂住。

对应 HTTP 接口（需要登录 Cookie）：

```sh
curl -X PUT http://服务器IP:8080/api/tunnel \
     -H 'Content-Type: application/json' \
     -d '{"running": true, "auto_reconnect": true}'
```

### 控制接口

### 关于 UDP 通告地址### 关于 UDP 通告地址

`UDP ASSOCIATE` 的响应里要告诉客户端「往哪里发 UDP 包」。默认返回
`0.0.0.0:<端口>`，Clash / mihomo / sing-box / v2rayN 会自动把它替换成
已连接的代理地址，因此在 Docker 端口映射下开箱即用。

若你的客户端不认 `0.0.0.0`，就显式指定公网地址：

```ini
SOCKS_UDP_ADVERTISE=203.0.113.10:1080
```

### 不用 Docker

```sh
go build -mod=vendor -o TJ Tunnel ./cmd/TJ Tunnel

./TJ Tunnel -u 学号 -p 密码 -listen 0.0.0.0:1080 \
          -socks-user proxyuser -socks-pass 密码
```

命令行参数与环境变量等价，优先级：默认值 → `~/.TJ Tunnel.json` → `-config`
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
-diagnose                逐项诊断 DNS / TCP / TLS / 登录
-web-listen <addr>       开启 Web 状态面板，例如 0.0.0.0:8080
-version                 打印版本
```

## 工作原理

```
                 ┌──────────────── TJ Tunnel ────────────────┐
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
make build         # 编译到 bin/TJ Tunnel
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
make rename MODULE=github.com/你的用户名/TJ Tunnel
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
| 不知道卡在哪一步 | 在服务器上运行 `docker compose run --rm TJ Tunnel -diagnose`，会逐项检查 DNS / TCP / TLS / 登录 |
| 客户端连上就报错 / 日志 `tunnel is not ready` | 容器在跑但 VPN 未登录成功，先看上面的诊断结果 |
| TCP 正常、UDP 不通 | 确认 compose 里 `1080/udp` 也映射了；必要时设 `SOCKS_UDP_ADVERTISE` |
| 容器 `unhealthy` | `docker logs` 查看是否还在重连；健康检查 30 s 一次 |
| 面板打不开 | 确认 `WEB_LISTEN` 已设置且 `8080:8080/tcp` 端口已映射 |
| 面板提示会话已失效 | 服务重启会让旧 Cookie 失效，重新登录即可 |
| 校内域名解析到公网地址 | 客户端要用 `socks5h`（域名交给代理解析） |

## 安全

- 公网部署**必须**设置 `SOCKS_USER` / `SOCKS_PASS`，否则等于开放代理。
- 账号密码只存在于 `docker-compose.yml` 的 `environment:` 里，不会写进镜像。
  真实凭证不要提交到公开仓库（本仓库的 compose 文件只有占位符）。
- 容器以非 root 运行，根文件系统只读，丢弃全部 capability。
- 详见 [SECURITY.md](SECURITY.md)。

## 免责声明

本项目仅供合法授权的用户在授权范围内使用。请遵守所在网络的使用规定，不要将
出口用于违规用途或转售给他人。因使用本项目产生的一切后果由使用者自行承担。

## 许可证

[MIT](LICENSE)。协议细节参考自
[Open TJ]
