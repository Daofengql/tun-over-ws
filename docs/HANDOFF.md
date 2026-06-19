# 交接文档

## 项目定位

`ws-vpn-go` 是一个计划中的 Go 单二进制网络工具。它通过 TUN 虚拟网卡捕获三层 IP 包，再通过 WebSocket 与中心服务端建立隧道，实现两类能力：

- 类 Tailscale 内网：客户端之间通过服务端中继互通。
- 服务端出口：客户端把非虚拟网段流量交给服务端，由服务端路由和 NAT 出口。

这个项目不是传统应用层代理。传统代理通常处理 `CONNECT host:port` 或 SOCKS 请求；本项目处理的是完整 IP 包。

## 当前状态

阶段 0-3 已完成，阶段 5 部分完成（心跳和重连）。代码可运行，Windows 客户端单机互 ping 验证通过。

已实现：

- IPv4 包头解析和校验（`internal/packet`）。
- YAML 配置加载（`internal/config`）。
- WebSocket relay 服务端，VIP 动态分配（`internal/relay`）。
- 客户端 WebSocket 连接、心跳（30s）、断线重连（指数退避）（`internal/conn`）。
- TUN 设备创建和 IP 配置（`internal/tun`，Windows 已验证）。
- Cobra CLI 入口（`cmd/wsvpn`）。
- 彩色终端日志（`internal/logger`，zerolog）。

关键约束：

- 未来构建产物应是一个二进制。
- 运行时通过子命令区分服务端和客户端（`wsvpn server` / `wsvpn client`）。
- Go 代码入口应放在 `cmd/wsvpn/`，业务实现放在 `internal/`，不要把 `.go` 文件堆在仓库根目录。
- 第一版传输层使用 WebSocket binary frame 承载原始 IP 包。
- 服务端仅限 Linux，客户端支持 Windows 和 Linux。
- TUN 层统一使用 `golang.zx2c4.com/wireguard/tun`（封装 Linux tun + Windows Wintun）。
- WebSocket 库使用 `github.com/coder/websocket`。
- CLI 框架使用 `github.com/spf13/cobra`。
- 配置格式仅 YAML。
- 日志使用彩色终端友好的现成库。
- TLS MVP 由反向代理终止，后续可能加入 Go 原生 TLS。
- 仅输出二进制，不附带 systemd unit 或 Dockerfile。

## 设计共识

前期讨论形成了这些共识：

1. 客户端使用 TUN，不使用 TAP。
2. 组网层处理 IP 包，不在应用层模拟 SOCKS/HTTP。
3. 虚拟 IP 由服务端统一分配（类 DHCP），客户端通过本地持久化的 UUID 标识身份。
4. 同一 UUID 只允许一个活跃客户端连接。
5. 客户端之间互通时，服务端只做按目标虚拟 IP 的包转发。
6. 服务端仅限 Linux，依赖内核做 IP forwarding、conntrack 和 NAT。
7. 客户端跨平台（Windows + Linux），TUN 层使用 `golang.zx2c4.com/wireguard/tun` 统一封装。
8. WebSocket 使用 `github.com/coder/websocket`，需要连接池和复用重连机制应对反向代理超时断连。
9. 心跳间隔 30 秒。
10. WebSocket 适合 MVP，因为部署友好、容易跑在 `443` 和反向代理后面。
11. WebSocket over TCP 会有 TCP-over-TCP 问题，后续可以考虑 QUIC/UDP 作为可选传输。

## 推荐 MVP

第一阶段只实现最小闭环：

```text
Client A TUN 10.66.0.2
  -> WebSocket
  -> Server relay
  -> WebSocket
  -> Client B TUN 10.66.0.3
```

验收标准：

- 两个客户端都能连接服务端，各获得一个服务端分配的虚拟 IP。
- 服务端维护 `virtual_ip -> client connection` 映射。
- A 可以 `ping` B 的虚拟 IP。
- B 可以 `ping` A 的虚拟 IP。
- A 可以访问 B 监听在虚拟 IP 上的 TCP 服务。

第二阶段再做服务端出口：

```text
Client A TUN
  -> WebSocket
  -> Server daemon
  -> Server TUN
  -> Linux forwarding/NAT
  -> Internet
```

验收标准：

- 客户端能选择只走虚拟网段，或启用服务端出口。
- 客户端默认路由不会把 WebSocket 连接自身送进隧道造成循环。
- 服务端启用 `ip_forward` 和 NAT 后，客户端能访问公网 IP。

## 初始模块划分建议

`cmd/wsvpn`

- 只放 CLI 入口。
- 解析 `server` / `client` 子命令。
- 加载配置并调用 `internal/app`。

`internal/app`

- 编排启动生命周期。
- 负责信号处理、上下文取消、组件启动和关闭。

`internal/config`

- 解析 YAML/TOML/JSON 配置。
- 校验虚拟 IP、网段、服务端地址、token、MTU。
- 提供默认值。

`internal/tun`

- 创建和配置 TUN。
- 读写 IP 包。
- 使用 `golang.zx2c4.com/wireguard/tun` 统一封装 Linux 和 Windows。
- Linux 底层为 `/dev/net/tun`，Windows 底层为 Wintun（库自动管理 DLL）。
- 路由和 IP 配置的平台差异在这里用 build tag 隔离。

`internal/packet`

- 最小化解析 IPv4/IPv6 header。
- 获取 source IP、destination IP、protocol。
- 做基础长度和版本校验。
- 不实现 TCP 栈。

`internal/relay`

- 服务端 WebSocket handler。
- 维护客户端注册表。
- 根据目标 IP 做 overlay 转发。
- 对非 overlay 流量按策略决定写入 server TUN 或丢弃。

## 服务端转发决策

服务端收到客户端发来的 IP 包后，逻辑应接近：

```text
dst = packet.destination_ip

if dst belongs to overlay_cidr:
    peer = client_registry[dst]
    if peer exists:
        send packet to peer websocket
    else:
        drop or return diagnostic
else:
    if source_client is allowed to use exit:
        write packet to server_tun
    else:
        drop
```

这个分支是项目的核心。

## 客户端路由策略

只访问虚拟内网时：

```text
route 10.66.0.0/24 -> TUN
```

启用服务端出口时：

```text
route server_public_ip -> original_gateway
route 0.0.0.0/1 -> TUN
route 128.0.0.0/1 -> TUN
```

使用两个 `/1` 是为了覆盖大部分 IPv4 默认流量，同时保留到 WebSocket 服务端公网 IP 的真实网络路径，避免隧道连接自吞。

IPv6 第一版可以先不支持，文档和配置里明确禁用或忽略。

## 风险清单

- WebSocket over TCP 会出现 TCP-over-TCP 队头阻塞。
- MTU 过大会导致分片、黑洞和奇怪的连接卡顿。
- 默认路由配置错误会导致客户端连不上服务端。
- 服务端出口需要管理员权限和系统网络配置，不是纯 Go 程序能完全解决。
- 反向代理（如 nginx）可能限制 WebSocket 最大连接时长，需要连接池和快速重连机制应对。
- Windows TUN 创建需要管理员权限和 Wintun 驱动。
- 服务端要防止任意客户端伪造源 IP。
- 真实部署必须使用 `wss://` 或额外加密认证。

## 下一步建议

1. 服务端仅限 Linux，客户端同时支持 Windows 和 Linux。~~（已确认）~~
2. TUN 库已确定 `golang.zx2c4.com/wireguard/tun`，WebSocket 库已确定 `github.com/coder/websocket`，CLI 已确定 `cobra`。~~（已确认）~~
3. 实现服务端 VIP 动态分配和客户端 UUID 身份持久化。
4. 实现 `packet` 的 IPv4 目标地址解析，并写单元测试。
5. 实现 server relay 注册表和转发逻辑。
6. 实现 client TUN read/write 与 WebSocket 双向泵（含连接池和快速重连）。
7. 做两个客户端互 ping 的端到端测试。
8. 再引入 server TUN 和 NAT 出口模式。
