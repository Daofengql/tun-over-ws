# 开发路线

路线图只保留当前仍有指导价值的阶段状态。已经完成并通过测试的连接池详细计划已经归档到架构和交接文档，不再保留独立计划文件。

## 阶段 0：项目基础

状态：已完成。

- 单二进制、多角色：`wsvpn server` / `wsvpn client`。
- Go 代码放在 `cmd/` 和 `internal/`，根目录不堆 `.go` 文件。
- 明确两条长期数据路径：overlay 内网和服务端出口。
- 建立 README、架构、运维、路线和交接文档。

## 阶段 1：包解析和配置

状态：已完成并有单元测试。

- YAML 配置解析和默认值。
- overlay CIDR、服务端地址、MTU、token 基础校验。
- IPv4 包头解析。
- source、destination、protocol 提取。
- 短包、非 IPv4、错误 IHL、错误 total length 校验。
- TCP、UDP、ICMP、组播、广播和噪声流量分类。

## 阶段 2：WebSocket Relay 原型

状态：已完成并有端到端单元测试。

- 服务端接受 WebSocket 连接。
- 客户端 hello 注册。
- 服务端动态分配虚拟 IP。
- 维护 `virtual_ip -> client connections` 映射。
- 校验 packet source 必须等于连接分配的 VIP。
- overlay 命中时转发给目标客户端。
- 未命中或非 overlay 流量按当前策略丢弃。

## 阶段 3：真实 TUN 客户端

状态：已完成并通过 Windows/Linux overlay 测试。

- Windows 客户端：Wintun + `netsh` 配置虚拟 IP。
- Linux 客户端：`/dev/net/tun` + `ip addr` / `ip link` 配置接口。
- TUN -> WebSocket 数据泵。
- WebSocket -> TUN 数据泵。
- Linux TUN 使用 `tunPacketOffset = 16`，修复 wireguard-go 后端 headroom 要求。
- Windows 单机双客户端 overlay ping 通过。
- Linux 服务端 + Linux 客户端 + Windows 客户端 overlay ping 通过。

仍需改进：

- 异常退出后的 TUN/路由清理更完整。
- 平台层失败提示和诊断信息更友好。

## 阶段 4：连接池和稳定性

状态：核心实现已完成，并通过单元测试与跨平台基础压测。

已完成：

- 固定大小 WebSocket 连接池。
- 正常状态下单 primary 承载主要流量，多 standby 热连接。
- primary 断开后提升 standby，并后台补建。
- TCP flow 绑定：已有 flow 保持在同一 WebSocket，避免跨连接乱序。
- TCP 背压：队列满时阻塞 TUN 读取路径，不再用令牌桶在热路径中主动丢 TCP。
- primary 退化或队列高压时，新 TCP flow 可尝试 standby 突发。
- UDP 可尝试 standby，仍不可用则快速丢弃。
- ICMP 短等待，组播/广播/IGMP 噪声过滤。
- QoS 观测：吞吐、写延迟 EWMA、读写字节、最后读写时间、队列深度。
- CDN/nginx 连接寿命探测和计划轮换。
- 计划内 draining/rotation 关闭不会参与超时学习。
- 服务端侧按目标 VIP 的连接池转发，TCP 保持 flow 绑定，UDP 可突发或丢弃。

已验证：

- `go test -timeout 60s ./...` 通过。
- Windows/Linux 二进制构建通过。
- 跨平台 overlay ping 通过。
- `iperf3` UDP 1M/5M 双向 0% 丢包。
- `iperf3` TCP 双向可通。
- 修复后未再观察到 standby 空闲读超时导致的假超时探测和无故轮换。

仍需改进：

- Linux -> Windows TCP 方向吞吐低、重传多，需要重点查 Windows TUN MTU、写入路径、分片和 TCP MSS。
- 日志中的包字节数字段目前可读性不足，需要修正输出格式。
- 根据更多本地低延迟测试结果调整写延迟阈值、critical latency、flow idle timeout。
- 增加结构化 metrics：连接数、转发包/字节数、丢包原因、重连和轮换次数。

## 阶段 5：服务端出口

状态：未开始。

目标：

- 服务端创建 server TUN，使用 `10.66.0.1/24`。
- 服务端读取 TUN 回包并转发给客户端。
- Linux 开启 IP forwarding，并配置 NAT。
- 客户端支持 exit mode 路由配置。
- 确保到 WebSocket 服务端公网地址的真实路由不被隧道吞掉。
- 服务端按权限控制哪些客户端可以使用 exit。

验收：

- 客户端启用 exit 后能访问公网。
- 禁用 exit 的客户端无法发送非 overlay 流量。
- WebSocket 控制连接不会被自己的默认路由送回隧道。

## 阶段 6：安全增强

状态：未开始。

目标：

- 用服务端签名登录、节点密钥或证书替换测试 token。
- 持久化节点身份和虚拟 IP。
- ACL 和审计日志。
- 限制客户端速率、连接频率和可访问目标。
- 限制 exit 权限。

当前已有安全基础：

- VIP 由服务端统一分配。
- 服务端校验 packet source IP 必须等于连接分配的 VIP。
- 同一 UUID 可拥有多条连接，但共享一个服务端分配的 VIP。

## 阶段 7：性能和传输扩展

状态：候选方向。

可能方向：

- 自适应 MTU 和 MSS clamping。
- 批量收发和更少日志开销。
- 更完整的本地低延迟压测。
- QUIC 或 UDP transport。
- 连接池诊断命令。

WebSocket 适合 MVP 和反向代理部署，但不一定是长期最佳传输。
