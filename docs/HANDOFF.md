# 交接文档

## 项目定位

`tun-over-ws` 是一个 Go 单二进制网络工具。它通过 TUN 虚拟网卡捕获三层 IPv4 包，再通过 WebSocket 与中心服务端建立隧道，实现 overlay 内网组网。

这个项目不是传统应用层代理。传统代理通常处理 `CONNECT host:port` 或 SOCKS 请求；本项目处理的是完整 IP 包，数据平面更接近一个中心化 relay VPN。

长期目标有两条数据路径：

- 类 Tailscale 内网：客户端之间通过服务端中继互通。
- 服务端出口：客户端把非虚拟网段流量交给服务端，由服务端路由和 NAT 出口。

当前实现只完成第一条 overlay 数据路径。exit gateway 尚未实现。

## 当前状态

阶段 0-3 已完成，阶段 5 部分完成。

已实现：

- IPv4 包头解析和长度校验（`internal/packet`）。
- YAML 配置加载和基础校验（`internal/config`）。
- WebSocket relay 服务端，VIP 动态分配（`internal/relay`）。
- 服务端 source VIP 校验，防止客户端伪造其他节点源地址。
- 客户端 WebSocket 连接、30s 心跳、断线重连（`internal/conn`）。
- TUN 设备创建、读写和平台 IP 配置（`internal/tun`）。
- Windows 客户端 TUN 配置（Wintun + `netsh`）。
- Linux 客户端 TUN 配置（wireguard-go + `ip addr` / `ip link`）。
- Linux TUN 读写 offset/headroom 适配，修复 `tun write failed: invalid offset`。
- Cobra CLI 入口（`cmd/wsvpn`）。
- 彩色终端日志（`internal/logger`，zerolog）。

已验证：

- `go test ./...` 通过。
- Windows 构建通过：`go build -o bin\wsvpn.exe ./cmd/wsvpn`。
- Linux amd64 构建通过：`GOOS=linux GOARCH=amd64 go build -o bin\wsvpn-linux-amd64 ./cmd/wsvpn`。
- Windows 单机双客户端 overlay ping 通过。
- Linux 服务端 + Linux 客户端 + Windows 客户端跨平台 overlay ping 通过。

跨平台测试记录不在公开文档中保留具体公网地址、连接 URL、登录信息或临时机器信息。若后续需要复测，应以两端客户端日志和服务端 `forwarded` 日志共同判断。

## 当前不是重点的事情

这些点不是当前进度重点，不要把它们当成阻塞项：

- UUID/token 只是测试阶段身份字段。后续计划改为服务端签名登录机制。
- 当前不实现 exit gateway。
- 当前不接管系统默认路由。
- 当前不做 Linux 侧公网 NAT 出口测试。
- IPv6 第一版不支持，IPv6 包会在客户端 debug 日志中被丢弃。

## 关键约束

- 构建产物应是一个二进制。
- 运行时通过子命令区分服务端和客户端：`wsvpn server` / `wsvpn client`。
- Go 代码入口放在 `cmd/wsvpn/`，业务实现放在 `internal/`，不要把 `.go` 文件堆在仓库根目录。
- 第一版传输层使用 WebSocket binary frame 承载原始 IPv4 包。
- 服务端仅限 Linux。
- 客户端支持 Windows 和 Linux。
- TUN 层统一使用 `golang.zx2c4.com/wireguard/tun`。
- WebSocket 库使用 `github.com/coder/websocket`。
- CLI 框架使用 `github.com/spf13/cobra`。
- 配置格式仅 YAML。
- 日志使用彩色终端友好的 zerolog console writer。
- TLS MVP 由反向代理终止，后续可能加入 Go 原生 TLS。
- 仅输出二进制，不附带 systemd unit 或 Dockerfile。

## 设计共识

1. 客户端使用 TUN，不使用 TAP。
2. 组网层处理 IP 包，不在应用层模拟 SOCKS/HTTP。
3. 虚拟 IP 由服务端统一分配（类 DHCP）。
4. 客户端通过本地持久化 UUID 标识身份；当前只是测试字段。
5. 同一 UUID 只允许一个活跃客户端连接，新连接会替换旧连接。
6. 客户端之间互通时，服务端只做按目标虚拟 IP 的包转发。
7. 服务端出口未来依赖 Linux 内核 IP forwarding、conntrack 和 NAT。
8. WebSocket 使用 `github.com/coder/websocket`，当前是单连接 + 自动重连，不是真正连接池。
9. 心跳间隔 30 秒。
10. WebSocket 适合 MVP，因为部署友好、容易跑在 `443` 和反向代理后面。
11. WebSocket over TCP 会有 TCP-over-TCP 问题，后续可以考虑 QUIC/UDP 作为可选传输。

## 当前模块划分

`cmd/wsvpn`

- CLI 入口。
- 解析 `server` / `client` 子命令。
- 加载配置并启动 relay 或 client。

`internal/config`

- 解析 YAML 配置。
- 校验 overlay CIDR、服务端地址、token、MTU。
- 提供默认值。

`internal/tun`

- 创建和配置 TUN。
- 读写原始 IPv4 包。
- 使用 `golang.zx2c4.com/wireguard/tun` 统一封装 Linux 和 Windows。
- 使用 `tunPacketOffset = 16` 给 wireguard-go TUN 后端保留 headroom。
- Linux 通过 `ip addr add <vip>/24 dev <name>` 和 `ip link set up` 配置接口。
- Windows 通过 `netsh interface ip set address` 配置接口。

`internal/packet`

- 最小化解析 IPv4 header。
- 获取 source IP、destination IP、protocol。
- 做基础长度、版本、IHL、total length 校验。
- 不实现 TCP 栈。

`internal/relay`

- 服务端 WebSocket handler。
- 维护客户端注册表。
- 根据目标 IP 做 overlay 转发。
- 校验 source IP 必须等于该连接被分配的 VIP。
- 对非 overlay 流量按当前策略丢弃。

`internal/conn`

- 客户端 WebSocket hello/hello_ok。
- 心跳、读写循环、断线重连。
- TUN -> WebSocket 与 WebSocket -> TUN 双向 pump。

## 服务端转发决策

服务端收到客户端发来的 IP 包后，当前逻辑接近：

```text
dst = packet.destination_ip

if packet.source_ip != source_client.virtual_ip:
    drop
else if dst belongs to overlay_cidr:
    peer = client_registry[dst]
    if peer exists:
        send packet to peer websocket
    else:
        drop
else:
    drop because exit mode is not implemented
```

未来 exit 模式会把最后一个分支改为按权限写入 server TUN。

## 客户端路由策略

当前只做 overlay 内网：

```text
route 10.66.0.0/24 -> TUN
```

Linux 侧由于接口地址配置为 `<vip>/24`，内核会生成 connected route。Windows 侧 `netsh` 设置静态地址后也会生成对应接口路由。

启用服务端出口时，未来计划使用：

```text
route server_public_ip -> original_gateway
route 0.0.0.0/1 -> TUN
route 128.0.0.0/1 -> TUN
```

使用两个 `/1` 是为了覆盖大部分 IPv4 默认流量，同时保留到 WebSocket 服务端公网 IP 的真实网络路径，避免隧道连接自吞。

## 已知问题和剩余风险

- exit gateway 未实现。
- 当前 auth 只是 token 列表 + UUID，不能用于真实生产部署。
- VIP 分配只在进程内存中保持，服务端重启后会重新分配。
- `routes.exit.enabled` 目前不会真正改默认路由。
- Linux/Windows TUN 清理是 best effort，异常退出后可能需要手动清理接口或路由。
- 客户端 `--send-to` 仍是占位测试参数，没有真正发包。
- WebSocket over TCP 在大流量或丢包网络下可能出现队头阻塞。
- 写队列满时当前直接丢包并告警，没有主动限速。
- 还没有内置指标、ACL、审计日志、配置热加载。

## 下一步建议

1. 补充 relay 的连接替换和并发注销单元测试。
2. 补充 Linux TUN setup 的可观测性和失败提示。
3. 把 Windows/Linux overlay ping 流程固定成脚本或半自动测试说明。
4. 引入服务端签名登录机制，替换测试 UUID/token。
5. 设计 exit gateway 的 server TUN、NAT、权限和路由保护。
6. 增加 metrics：连接数、转发包数、丢包原因、重连次数。
7. 再考虑 QUIC/UDP transport，避免过早优化 WebSocket 数据平面。
