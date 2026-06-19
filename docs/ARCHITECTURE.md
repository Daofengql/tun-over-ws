# 架构说明

## 总览

本项目计划实现一个中心化 WebSocket L3 VPN。客户端通过 TUN 接管指定路由的 IP 包，服务端作为 relay 和可选 egress gateway。

```text
                overlay packet
Client A TUN ------------------+
  10.66.0.2                    |
                               v
                         Server relay
                               |
Client B TUN <-----------------+
  10.66.0.3


                exit packet
Client A TUN
  -> WebSocket
  -> Server daemon
  -> Server TUN
  -> Linux routing/NAT
  -> Internet
```

## 平台支持

- 服务端：仅限 Linux。出口模式依赖内核 TUN、ip_forward 和 NAT。
- 客户端：Windows 和 Linux。TUN 层使用 `golang.zx2c4.com/wireguard/tun` 统一封装。
- IPv6 第一版不支持。

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
- 配置本机路由。
- 连接服务端 WebSocket。
- 从 TUN 读取 IP 包并发给服务端。
- 从服务端接收 IP 包并写回 TUN。
- 心跳（30s）和断线重连。
- WebSocket 连接池管理，应对反向代理超时断连。

客户端不负责：

- 解析完整 TCP 状态。
- 把流量转换为 SOCKS/HTTP。
- 直接与其他客户端 P2P 连接。

## 服务端职责

服务端负责：

- 接受客户端 WebSocket 连接。
- 验证 UUID + token 身份。
- 动态分配虚拟 IP，维护 `virtual_ip -> client connection` 映射。
- 解析 IP 包目标地址。
- 对 overlay 流量转发到目标客户端。
- 对 exit 流量按权限写入 server TUN。
- 统计和日志。
- 控制客户端源地址伪造（校验 source IP == 分配的 VIP）。
- 同一 UUID 只允许一个活跃连接。

服务端不应该在第一版里：

- 实现完整 TCP/IP 栈。
- 对每个 TCP 连接手写 socket 代理。
- 尝试做复杂 NAT 穿透。

## 数据平面

WebSocket 使用 binary frame 承载原始 IP 包。

最简单的数据帧：

```text
[raw IPv4 packet]
```

MVP 可以先不加自定义帧头，因为 WebSocket 连接已经绑定了客户端身份。

后续如果需要多通道、压缩、控制消息和统计，可以引入帧头：

```text
type     1 byte
flags    1 byte
length   2 or 4 bytes
payload  n bytes
```

但第一版应避免过早复杂化。

### 连接池与复用

WebSocket 连接可能受到反向代理（如 nginx）的最大时长限制而被断开。客户端必须设计连接池和复用机制：

- 预建多条 WebSocket 连接，避免建链延迟。
- 断线后快速重建，不等待当前连接完全关闭。
- 连接切换时保证数据不丢失（写队列转移）。
- 心跳探活，主动淘汰失效连接。

## 控制平面

控制平面通过连接后第一条 JSON 消息完成。

客户端 hello（不再携带虚拟 IP，由服务端分配）：

```text
uuid          客户端唯一标识（来自登录系统，本地持久化）
token         认证令牌
hostname      主机名
want_exit     是否请求出口
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

服务端注册成功后，分配虚拟 IP 并返回（类似 DHCP）：

```json
{
  "type": "hello_ok",
  "virtual_ip": "10.66.0.2",
  "overlay_cidr": "10.66.0.0/24",
  "mtu": 1280,
  "routes": ["10.66.0.0/24"]
}
```

同一台机器只允许运行一个客户端实例（UUID 全局唯一）。

## Overlay 转发

Overlay 转发的关键是目标地址查表。虚拟 IP 由服务端动态分配：

```text
Client A 连接 -> 服务端分配 10.66.0.2
Client B 连接 -> 服务端分配 10.66.0.3

registry[10.66.0.3] = Client B connection
```

服务端把原始 IP 包写入 Client B 的 WebSocket。Client B 收到后写入 TUN，由 Client B 的操作系统网络栈处理。

回包路径相同：

```text
src = 10.66.0.3
dst = 10.66.0.2
```

## Exit 转发

Exit 转发发生在目标地址不属于 overlay 网段时。

```text
src = 10.66.0.2
dst = 8.8.8.8
```

服务端如果允许该客户端使用出口，就把包写入 server TUN。Linux 内核随后根据路由和 NAT 规则把它转发到公网。

典型 Linux 配置思路：

```text
server tun ip: 10.66.0.1/24
enable net.ipv4.ip_forward = 1
NAT 10.66.0.0/24 out via eth0
```

回包从公网回到服务端，再经 conntrack/NAT 还原，进入 server TUN，被服务端 daemon 读出并按目标虚拟 IP 转回客户端。

## 安全边界

服务端必须认为客户端发来的 IP 包不可信。

至少要做：

- WebSocket 连接身份认证（UUID + token）。
- 虚拟 IP 由服务端统一分配，不允许客户端自报。
- 检查包的 source IP 是否等于该客户端被分配的虚拟 IP。
- 检查目标 IP 是否允许访问。
- 限制单客户端速率和并发。
- 不允许未授权客户端使用 exit。
- 同一 UUID 只允许一个活跃连接。

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

第一版建议至少记录：

- 客户端连接和断开。
- 虚拟 IP 注册。
- IP 冲突。
- 丢包原因。
- overlay 转发包数和字节数。
- exit 转发包数和字节数。
- WebSocket 重连次数。

日志里不要默认打印完整数据包内容。
