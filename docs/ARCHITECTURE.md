# 架构说明

## 总览

本项目实现一个中心化 WebSocket L3 VPN。客户端通过 TUN 接管 overlay 网段的 IPv4 包，服务端作为 relay 按虚拟 IP 转发。

当前已经实现 overlay relay；exit gateway 仍是后续阶段。

```text
                overlay packet
Client A TUN ------------------+
  10.66.0.2                    |
                               v
                         Server relay
                               |
Client B TUN <-----------------+
  10.66.0.3
```

未来 exit 数据路径：

```text
Client A TUN
  -> WebSocket
  -> Server daemon
  -> Server TUN
  -> Linux routing/NAT
  -> Internet
```

## 平台支持

- 服务端：Linux 优先。当前 relay 不依赖 server TUN；未来出口模式依赖 Linux TUN、ip_forward 和 NAT。
- 客户端：Windows 和 Linux。TUN 层使用 `golang.zx2c4.com/wireguard/tun` 统一封装。
- IPv4：当前支持。
- IPv6：第一版不支持，收到 IPv6 包时丢弃并输出 debug 日志。

最终产物叫 `wsvpn`，通过子命令区分角色：

```text
wsvpn server --config server.yaml
wsvpn client --config client.yaml
```

## 客户端职责

客户端负责：

- 创建 TUN。
- 通过 hello 注册身份（UUID + token），接收服务端分配的虚拟 IP。
- 配置 TUN 的虚拟 IP 和 MTU。
- 使用系统 connected route 让 overlay 网段进入 TUN。
- 连接服务端 WebSocket。
- 从 TUN 读取 IPv4 包并发给服务端。
- 从服务端接收 IPv4 包并写回 TUN。
- 心跳（30s）和连接池管理（自动轮换、QoS 检测、拥塞控制）。

客户端不负责：

- 解析完整 TCP 状态。
- 把流量转换为 SOCKS/HTTP。
- 直接与其他客户端 P2P 连接。
- 当前阶段不接管默认路由。

## 服务端职责

服务端负责：

- 接受客户端 WebSocket 连接。
- 验证 UUID + token 身份（当前为测试实现）。
- 动态分配虚拟 IP，维护 `virtual_ip -> client connection` 映射。
- 解析 IP 包目标地址。
- 检查包的 source IP 是否等于该客户端被分配的 VIP。
- 对 overlay 流量转发到目标客户端。
- 对非 overlay 流量丢弃；未来 exit 模式会在这里写入 server TUN。
- 记录连接、注册、转发、丢包原因。
- 同一 UUID 支持多条连接（连接池），转发时按队列深度选路。

服务端不应该在第一版里：

- 实现完整 TCP/IP 栈。
- 对每个 TCP 连接手写 socket 代理。
- 尝试做复杂 NAT 穿透。

## 数据平面

WebSocket 使用 binary frame 承载原始 IPv4 包。

当前数据帧：

```text
[raw IPv4 packet]
```

MVP 不加自定义帧头，因为 WebSocket 连接已经绑定了客户端身份。

后续如果需要多通道、压缩、控制消息和统计，可以引入帧头：

```text
type     1 byte
flags    1 byte
length   2 or 4 bytes
payload  n bytes
```

但第一版应避免过早复杂化。

## TUN 抽象

TUN 封装位于 `internal/tun`。

关键实现点：

- `Create(name, mtu)` 调用 wireguard-go `tun.CreateTUN`。
- `Read` 返回不带平台前缀的原始 IPv4 包。
- `Write` 接收原始 IPv4 包并写入 TUN。
- Linux 和 Windows 的 IP 配置通过 build tag 分开。
- `tunPacketOffset = 16` 给 wireguard-go TUN 后端保留 headroom（Linux virtio-net 要求）。
- 多个 readConn 写 TUN 时通过 `tunWriteMu` 串行化。

Linux 上曾遇到 `tun write failed: invalid offset`。原因是 wireguard-go 的 Linux TUN 后端可能启用 virtio/offload 相关 headroom，offset 不能为 0。当前读写都使用 16 字节 offset 后，Linux -> Windows ping 已验证通过。

## 连接生命周期

客户端使用连接池管理多条 WebSocket 连接。

连接池（`internal/conn/pool.go`）：

- 首次 `Connect` 成功后创建 TUN，TUN 生命周期独立于任何单条连接。
- 池内维护多条连接，默认最多 3 条，最多 2 条同时活跃。
- 每条连接有独立的 read/write/heartbeat goroutine。
- 选择连接时按权重加权随机：健康连接优先，限速连接承载溢出。
- 连接断开后由池自动补充新连接。

QoS 检测（`internal/conn/connstate.go`）：

- 每条连接独立追踪吞吐：200ms 采样，10s 滑动窗口。
- 动态 peak 检测，新连接 5s 预热期。
- 降级条件：peak > 1MB/s 且 current < peak × 50%。
- 标记为 throttled 后永久不恢复（CDN 单链接限速不会解除）。
- throttled 连接权重降至 0.1，仍可承载溢出流量。

拥塞控制（`internal/conn/ratelimit.go`）：

- 令牌桶按所有连接总可用带宽放行。
- 超出容量的包被丢弃，触发 TCP 源端自然降速。
- 全部限速时定期探测恢复，逐步放宽容量。

超时探测（`internal/conn/timeout.go`）：

- 被动学习 CDN/nginx 连接时长限制。
- 连续 3 次以上存活时长在 ±5s 内 → 确认为超时限制。
- 轮换间隔 = 探测值 × 0.8，提前建新连接无缝切换。

服务端：

- 每个客户端连接有独立 read/write/heartbeat goroutine。
- 服务端根 context 取消时会推动连接关闭。
- 同 UUID 多条连接共存，转发时选 WriteCh 队列最浅的连接。
- 连接注销时从列表中移除指定连接，不影响同 UUID 的其他连接。

## 控制平面

控制平面通过连接后第一条 JSON 消息完成。

客户端 hello：

```text
uuid          客户端唯一标识（当前来自配置，未来来自登录系统）
token         认证令牌（当前为测试 token）
hostname      主机名
want_exit     是否请求出口（当前固定 false）
client_version
```

示例：

```json
{
  "type": "hello",
  "uuid": "550e8400-e29b-41d4-a716-446655440000",
  "token": "replace-me",
  "hostname": "laptop-a",
  "want_exit": false
}
```

服务端注册成功后，分配虚拟 IP 并返回：

```json
{
  "type": "hello_ok",
  "virtual_ip": "10.66.0.2",
  "overlay_cidr": "10.66.0.0/24",
  "mtu": 1280,
  "routes": ["10.66.0.0/24"]
}
```

同一 UUID 支持多条连接同时注册，每条连接独立完成 hello 握手。

## Overlay 转发

Overlay 转发的关键是目标地址查表。虚拟 IP 由服务端动态分配：

```text
Client A 连接 -> 服务端分配 10.66.0.2
Client B 连接 -> 服务端分配 10.66.0.3

registry[10.66.0.3] = [Client B conn1, Client B conn2, ...]
```

服务端把原始 IP 包写入 Client B 队列最空闲的 WebSocket 连接。Client B 收到后写入 TUN，由 Client B 的操作系统网络栈处理。

回包路径相同：

```text
src = 10.66.0.3
dst = 10.66.0.2
```

服务端转发前会校验：

```text
packet.src == connection.virtual_ip
```

不匹配则丢弃。

## Exit 转发

Exit 转发发生在目标地址不属于 overlay 网段时。

```text
src = 10.66.0.2
dst = 8.8.8.8
```

当前实现会在 `exit.enabled = false` 时丢弃；即使配置为 true，server TUN 数据路径也尚未实现。

未来计划：

```text
server tun ip: 10.66.0.1/24
enable net.ipv4.ip_forward = 1
NAT 10.66.0.0/24 out via eth0
```

公网回包从服务端经 conntrack/NAT 还原后进入 server TUN，再由 daemon 读出并按目标虚拟 IP 转回客户端。

## 安全边界

服务端必须认为客户端发来的 IP 包不可信。

当前已做：

- WebSocket 连接 token 校验。
- 虚拟 IP 由服务端统一分配，不允许客户端自报。
- 检查包的 source IP 是否等于该客户端被分配的 VIP。
- 同 UUID 多连接共存，转发按队列深度选路。

仍需补齐：

- token 替换为节点密钥、证书或服务端签名登录。
- 检查目标 IP 是否允许访问。
- ACL。
- 限制单客户端速率和并发。
- 不允许未授权客户端使用 exit。
- 审计日志。

## MTU

建议第一版默认 TUN MTU：

```text
1280
```

原因：

- WebSocket/TLS/TCP 有额外开销。
- 1280 是 IPv6 最小 MTU，也适合作为保守默认值。
- 可以减少路径 MTU 黑洞问题。

后续可以加入 MSS clamping 或路径 MTU 探测。

## 日志和可观测性

第一版已记录：

- 客户端连接和断开。
- 虚拟 IP 注册。
- 丢包原因。
- overlay 转发包数相关日志。
- WebSocket 心跳。
- WebSocket 重连。

建议后续增加：

- 结构化 metrics。
- 每客户端转发包数和字节数。
- 每原因丢包计数。
- 当前连接表诊断命令。

日志里不要默认打印完整数据包内容。
