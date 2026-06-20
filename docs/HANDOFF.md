# 交接文档

## 项目定位

`tun-over-ws` 是一个 Go 单二进制三层 overlay 组网工具。客户端通过 TUN 虚拟网卡捕获 IPv4 包，再通过 WebSocket 与中心服务端建立隧道，由服务端按虚拟 IP 转发。

它不是传统 SOCKS/HTTP 代理。传统代理处理应用层连接请求，本项目处理完整 IP 包，数据平面更接近中心化 relay VPN。

长期目标有两条数据路径：

- overlay 内网：客户端之间通过服务端中继互通。
- 服务端出口：客户端把非虚拟网段流量交给服务端，由服务端路由和 NAT 出口。

当前只完成 overlay 数据路径。exit gateway 尚未实现。

## 当前状态

已实现：

- IPv4 包头解析、长度校验和流量分类（`internal/packet`）。
- YAML 配置加载和基础校验（`internal/config`）。
- WebSocket relay 服务端和 VIP 动态分配（`internal/relay`）。
- 服务端 source VIP 校验，防止客户端伪造其他节点源地址。
- 服务端支持同一 UUID/VIP 多条 WebSocket 连接。
- 客户端 TUN 设备创建、读写和平台 IP 配置（`internal/tun`）。
- Windows 客户端 TUN 配置（Wintun + `netsh`）。
- Linux 客户端 TUN 配置（wireguard-go + `ip addr` / `ip link`）。
- Linux TUN 读写 offset/headroom 适配，修复 `tun write failed: invalid offset`。
- 客户端固定大小 WebSocket 连接池（`internal/conn`）。
- 单 primary + 多 standby；primary 断开时提升 standby，并后台补建。
- TCP flow 绑定和背压：已有 flow 不跨连接乱序，队列满时阻塞 TUN 读取路径。
- UDP/ICMP/噪声流量独立策略：UDP 可 standby 突发或丢弃，ICMP 短等待，组播/广播/IGMP 过滤。
- QoS 观测：吞吐、写延迟 EWMA、读写字节、最后读写时间、队列深度。
- CDN/nginx 连接寿命探测和计划轮换。
- 计划内 draining/rotation 关闭不会参与超时学习，避免自触发重连风暴。
- Cobra CLI 入口（`cmd/wsvpn`）。
- 彩色终端日志（`internal/logger`，zerolog）。

已验证：

- `go test -timeout 60s ./...` 通过。
- Windows 构建通过：`go build -o bin\wsvpn.exe ./cmd/wsvpn`。
- Linux amd64 构建通过：`GOOS=linux GOARCH=amd64 go build -o bin\wsvpn-linux-amd64 ./cmd/wsvpn`。
- Windows 单机双客户端 overlay ping 通过。
- Linux 服务端 + Linux 客户端 + Windows 客户端跨平台 overlay ping 通过。
- 跨平台 `iperf3` UDP 1M/5M 双向 0% 丢包。
- 跨平台 `iperf3` TCP 双向可通；Windows -> Linux 方向吞吐明显好于 Linux -> Windows。
- 修复 standby 空闲读超时后，压测期间未再观察到无故 primary 轮换或连接池重连风暴。

跨平台测试记录不在公开文档中保留具体公网地址、连接 URL、登录信息或临时机器信息。后续复测应以两端客户端日志和服务端 `forwarded` 日志共同判断。

## 当前不是重点的事情

这些点不是当前进度重点，不要把它们当成阻塞项：

- UUID/token 只是测试阶段身份字段，后续会改为服务端签名登录机制。
- 当前不实现 exit gateway。
- 当前不接管系统默认路由。
- 当前不做 Linux 侧公网 NAT 出口测试。
- IPv6 第一版不支持，IPv6 包会在客户端 debug 日志中被丢弃。

## 关键约束

- 构建产物应是一个二进制。
- 运行时通过子命令区分服务端和客户端：`wsvpn server` / `wsvpn client`。
- Go 代码入口放在 `cmd/wsvpn/`，业务实现放在 `internal/`，不要把 `.go` 文件堆在仓库根目录。
- 第一版传输层使用 WebSocket binary frame 承载原始 IPv4 包。
- 服务端优先支持 Linux；当前 relay 不依赖 server TUN。
- 客户端支持 Windows 和 Linux。
- TUN 层统一使用 `golang.zx2c4.com/wireguard/tun`。
- WebSocket 库使用 `github.com/coder/websocket`。
- CLI 框架使用 `github.com/spf13/cobra`。
- 配置格式仅 YAML。
- 日志使用 zerolog console writer。
- TLS MVP 由反向代理终止，后续可加入 Go 原生 TLS。
- 不提交 `bin/`、`configs/`、`testdata/`、日志、二进制、`wintun.dll`、临时远端信息。

## 设计共识

1. 客户端使用 TUN，不使用 TAP。
2. 组网层处理 IP 包，不在应用层模拟 SOCKS/HTTP。
3. 虚拟 IP 由服务端统一分配，客户端不能自报 VIP。
4. UUID/token 当前只是测试身份字段，未来会替换为服务端签名登录。
5. 同一 UUID 可以有多条 WebSocket 连接，形成一个共享 VIP 的连接池。
6. 客户端连接池保持固定大小，正常情况下只有一条 primary，其他为 standby。
7. TCP 优先保证流顺序和背压，不做逐包加权随机分发。
8. UDP 可以复用 standby 做突发，也可以在压力下丢弃。
9. 客户端之间互通时，服务端只按目标虚拟 IP 和流量类型转发。
10. 服务端出口未来依赖 Linux 内核 IP forwarding、conntrack 和 NAT。
11. WebSocket 适合 MVP，因为部署友好、容易跑在 443 和反向代理后面。
12. WebSocket over TCP 会有 TCP-over-TCP 问题，后续可考虑 QUIC/UDP 作为可选传输。

## 模块划分

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
- 做 TCP、UDP、ICMP、组播、广播和噪声流量分类。
- 不实现 TCP 栈。

`internal/relay`

- 服务端 WebSocket handler。
- 维护客户端注册表。
- 根据目标 IP 做 overlay 转发。
- 校验 source IP 必须等于该连接被分配的 VIP。
- 同一 UUID/VIP 支持多条连接。
- 服务端侧维护 TCP flow 绑定，避免已有 flow 在目标连接池中乱序。
- 对非 overlay 流量按当前策略丢弃。

`internal/conn`

- 客户端 WebSocket hello/hello_ok。
- 固定大小连接池。
- 每连接心跳、读写循环、自动重连。
- primary/standby/draining/dead 生命周期管理。
- TCP flow 绑定、背压和 standby 突发。
- UDP/ICMP/噪声分流策略。
- TUN -> WebSocket 与 WebSocket -> TUN 双向 pump。

## 服务端转发决策

服务端收到客户端发来的 IP 包后，当前逻辑接近：

```text
dst = packet.destination_ip

if packet.source_ip != source_connection.virtual_ip:
    drop
else if dst belongs to overlay_cidr:
    target_pool = client_registry[dst]
    if target_pool exists:
        forward according to traffic class
    else:
        drop
else:
    drop because exit mode is not implemented
```

对目标连接池的策略：

- TCP：已有 flow 发送到绑定连接；新 flow 默认进入 inferred primary，高压时可尝试 standby；队列满时等待。
- UDP：优先 primary，满时尝试 standby，仍不可用则丢弃。
- ICMP：短等待。
- 噪声：过滤或丢弃。

未来 exit 模式会把非 overlay 分支改为按权限写入 server TUN。

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
- Linux -> Windows TCP 吞吐低且重传较多，需要继续查 Windows TUN MTU、写入路径、分片和 MSS。
- 日志中部分 `bytes=` 字段目前没有清晰打印数值，需要修正可观测性。
- 还没有内置 metrics、ACL、审计日志、配置热加载。

## 下一步建议

1. 优先定位 Linux -> Windows TCP 方向重传和吞吐低的问题。
2. 修正日志中 `bytes=` 等字段的输出，降低 debug 噪音。
3. 在本地低延迟环境复测 TCP/UDP，排除远端高 RTT 对吞吐的放大影响。
4. 补充连接池跨机器压测脚本或半自动测试说明。
5. 增加 metrics：连接数、转发包数、字节数、丢包原因、重连/轮换次数。
6. 引入服务端签名登录机制，替换测试 UUID/token。
7. 设计 exit gateway 的 server TUN、NAT、权限和路由保护。
8. 再考虑 QUIC/UDP transport，避免过早优化 WebSocket 数据平面。
