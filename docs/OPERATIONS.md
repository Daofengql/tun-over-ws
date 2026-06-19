# 运行和部署设想

## 平台支持

### 服务端

仅支持 Linux。

原因：

- TUN 支持成熟。
- IP forwarding、conntrack、iptables/nftables 方案清晰。
- 适合部署在 VPS、云主机或内网网关。

### 客户端

支持 Windows 和 Linux：

- Windows：使用 Wintun 驱动（通过 `golang.zx2c4.com/wireguard/tun` 自动管理）。
- Linux：使用 `/dev/net/tun`（同上库封装）。

第一版同时覆盖两个平台的客户端。

## 服务端出口配置示例

以下是未来文档可参考的 Linux 配置思路，不代表当前项目已有代码。

开启 IPv4 转发：

```sh
sysctl -w net.ipv4.ip_forward=1
```

使用 iptables 做 NAT：

```sh
iptables -t nat -A POSTROUTING -s 10.66.0.0/24 -o eth0 -j MASQUERADE
```

如果使用 nftables，后续可提供等价配置。

## 客户端路由示例

仅 overlay（默认，VIP 分配后自动配置）：

```sh
ip route add <assigned_overlay_cidr> dev wsvpn0
```

启用 exit：

```sh
ip route add <server_public_ip>/32 via <original_gateway>
ip route add 0.0.0.0/1 dev wsvpn0
ip route add 128.0.0.0/1 dev wsvpn0
```

注意：必须保留到服务端公网 IP 的真实路由，否则 WebSocket 连接可能被送进自己的隧道。

## 反向代理

WebSocket 可以放在 HTTPS 反向代理后面，例如：

```text
client -> wss://vpn.example.com/tunnel -> reverse proxy -> wsvpn server
```

需要确认：

- 反向代理允许 WebSocket upgrade。
- 空闲超时时间足够长。
- 不对 binary frame 做异常限制。
- 请求体和消息大小限制适配 MTU。

## 推荐默认值

```text
overlay_cidr: 10.66.0.0/24
server_tun_ip: 10.66.0.1
client_mtu: 1280
transport: websocket
tls: required in production
ipv6: disabled in MVP
```

## 配置草案

服务端：

```yaml
listen: ":8443"
overlay_cidr: "10.66.0.0/24"
server_tun:
  enabled: true
  name: "wsvpn0"
  ip: "10.66.0.1"
  mtu: 1280
exit:
  enabled: true
auth:
  tokens:
    - "replace-me"
    - "replace-me-too"
heartbeat:
  interval: 30s
```

客户端：

```yaml
server_url: "ws://vpn.example.com/tunnel"
uuid: "550e8400-e29b-41d4-a716-446655440000"
token: "replace-me"
tun:
  name: "wsvpn0"
  mtu: 1280
routes:
  exit:
    enabled: false
```

注意：

- 客户端不再配置 `virtual_ip`，连接后由服务端分配。
- 客户端 `uuid` 来源于登录系统，本地持久化，全局唯一。
- 服务端不再逐节点绑定 IP，改为动态分配池。

## 调试建议

常用检查：

- 客户端 TUN 是否创建成功。
- 客户端虚拟 IP 是否配置成功。
- 客户端路由表是否正确。
- WebSocket 是否连接成功。
- 服务端是否注册了正确的虚拟 IP。
- 服务端是否因为目标 IP 未命中而丢包。
- 服务端出口模式下 `ip_forward` 是否开启。
- NAT 规则是否命中。

建议后续内置诊断命令：

```text
wsvpn diag routes
wsvpn diag tun
wsvpn diag server
```
