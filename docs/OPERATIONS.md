# 运行和测试手册

## 平台支持

### 服务端

服务端目标平台是 Linux。当前 relay 只需要监听 WebSocket，不创建 server TUN；未来 exit gateway 会依赖 Linux TUN、IP forwarding、conntrack 和 NAT。

### 客户端

客户端当前支持 Windows 和 Linux：

- Windows：使用 Wintun 驱动，通过 `golang.zx2c4.com/wireguard/tun` 创建 TUN，使用 `netsh` 配置 IP。
- Linux：使用 `/dev/net/tun`，通过 `ip addr` 和 `ip link` 配置接口。

当前只配置 overlay connected route，不接管默认路由。

## 构建

Windows：

```powershell
go build -o .\bin\wsvpn.exe .\cmd\wsvpn
```

Linux amd64：

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -o .\bin\wsvpn-linux-amd64 .\cmd\wsvpn
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH
```

验证：

```powershell
go test -timeout 60s ./...
go vet ./...
git diff --check
```

Windows 客户端运行前确认：

- 管理员 PowerShell。
- `bin\wsvpn.exe` 存在。
- `bin\wintun.dll` 存在。

Linux 客户端运行前确认：

- root 权限。
- `/dev/net/tun` 存在。
- `ip` 命令可用。

## 配置说明

服务端开发配置示例：

```yaml
listen: ":18443"
overlay_cidr: "10.66.0.0/24"
server_tun:
  enabled: false
  name: "wsvpn0"
  ip: "10.66.0.1"
  mtu: 1280
exit:
  enabled: false
auth:
  tokens:
    - "test-token-aaa"
    - "test-token-bbb"
heartbeat:
  interval: 30s
```

客户端开发配置示例：

```yaml
server_url: "ws://127.0.0.1:18443/tunnel"
uuid: "client-a-00000000-0000-0000-0000-000000000001"
token: "test-token-aaa"
tun:
  name: "wsvpn0"
  mtu: 1280
routes:
  exit:
    enabled: false
```

注意：

- 客户端不配置 `virtual_ip`，连接后由服务端分配。
- 服务端会跳过 `server_tun.ip`，默认第一个客户端拿到 `10.66.0.2`。
- `server_tun.enabled` 当前不影响 overlay relay；exit 还未实现。
- UUID/token 是开发测试字段，后续会替换为签名登录机制。
- 真实配置文件、token、远端地址和临时 URL 不应提交。

## 本地 Windows Overlay 测试

需要管理员 PowerShell。

```powershell
# 终端 1：服务端
.\bin\wsvpn.exe server -c .\configs\local\server.yaml --log-level debug

# 终端 2：客户端 A
.\bin\wsvpn.exe client -c .\configs\local\client-a.yaml --log-level debug

# 终端 3：客户端 B
.\bin\wsvpn.exe client -c .\configs\local\client-b.yaml --log-level debug

# 终端 4：指定源地址 ping
ping -S 10.66.0.2 10.66.0.3
```

预期：

- 客户端 A 日志出现 `virtual_ip=10.66.0.2`。
- 客户端 B 日志出现 `virtual_ip=10.66.0.3`。
- 服务端日志出现 `forwarded`。
- ping 有回复，0% 丢包。

脚本方式：

```powershell
.\scripts\test-tun.ps1
```

## Windows/Linux Overlay 测试

跨平台测试使用本地配置文件，不在公开仓库中保存任何公网地址、连接 URL、临时 token 或主机信息。

Linux 指定源接口测试：

```bash
ping -I 10.66.0.2 -c 4 -W 2 10.66.0.3
```

Windows 指定源地址测试：

```powershell
ping -S 10.66.0.3 10.66.0.2
```

不要修改 Linux 或 Windows 默认路由。当前测试只要求 overlay connected route 生效。

## iperf3 测试

可以用 `iperf3` 验证 TCP/UDP 双向转发。示例中 Windows 是 `10.66.0.3`，Linux 是 `10.66.0.2`。

iperf3 二进制位于 `bin/iperf3-win/` 目录下，可直接用于测试。

注意：测试端口应选择 ufw 未拦截的端口。远程 Linux 服务器上 ufw 放行了 20000-30000 范围，推荐使用 **26001**。5201 在 DROP 范围（444:5243）内，不可用。

Windows 启动 server：

```powershell
bin\iperf3-win\iperf3.exe -s -B 10.66.0.3 -p 26001
```

Linux -> Windows TCP：

```bash
iperf3 -c 10.66.0.3 -B 10.66.0.2 -p 26001 -t 10
```

Windows -> Linux TCP：

```bash
iperf3 -c 10.66.0.2 -B 10.66.0.3 -p 26001 -t 10
```

Linux -> Windows UDP：

```bash
iperf3 -c 10.66.0.3 -B 10.66.0.2 -p 26001 -t 10 -u -b 10M -l 1000
```

Windows -> Linux UDP：

```bash
iperf3 -c 10.66.0.2 -B 10.66.0.3 -p 26001 -t 10 -u -b 10M -l 1000
```

### 跨平台远端测试记录（2026-06-20）

测试环境：Linux 服务端 + Linux 客户端（海外 Ubuntu 24.04）+ Windows 客户端（本地），overlay 跨公网中继。

| 方向 | 协议 | 吞吐 | 重传/丢包 |
| ---- | ---- | ---- | --------- |
| Windows -> Linux | TCP | 42-44 Mbits/sec | — |
| Linux -> Windows | TCP | 51-54 Mbits/sec | 0 次重传 |
| Windows -> Linux | UDP 10M | 9.98 Mbits/sec | 0% 丢包 |
| Linux -> Windows | UDP 10M | 10.0 Mbits/sec | 0% 丢包 |

与早期测试相比，Linux -> Windows TCP 方向从吞吐低、重传多改善到 0 重传、54 Mbits/sec。

远端链路 RTT 高时，吞吐数字只能作为功能和稳定性信号，不应当作为最终性能结论。

## 停止测试进程

如果使用后台进程，可用这些命令收尾：

```bash
pkill -x wsvpn || true
pkill -x iperf3 || true
ip link show wsvpn0
```

如果接口还残留：

```bash
ip addr del 10.66.0.2/24 dev wsvpn0 2>/dev/null || true
ip link set dev wsvpn0 down 2>/dev/null || true
```

Windows 停止本地测试进程：

```powershell
Get-Process wsvpn,iperf3 -ErrorAction SilentlyContinue | Stop-Process -Force
```

## 日志判断

客户端成功连接：

```text
registered with server virtual_ip=10.66.0.x overlay_cidr=10.66.0.0/24
tun device created
tun configured
client ready
standby added
```

客户端发包：

```text
tun -> pool src=10.66.0.x dst=10.66.0.y class=tcp
```

服务端转发：

```text
forwarded from=10.66.0.x dst=10.66.0.y class=tcp
```

客户端收包：

```text
ws -> tun src=10.66.0.x dst=10.66.0.y
```

稳定连接池不应频繁出现：

```text
rotation threshold reached
primary rotated
conn read ended
reconnect
```

这些日志如果在直连或空闲场景频繁出现，优先检查 standby 是否被空闲读超时关闭、计划关闭是否被纳入超时探测样本。

常见 debug 噪音：

- `not an IPv4 packet: version 6`：系统把 IPv6 包送进 TUN，当前不支持 IPv6，会丢弃。
- `dst not found, dropping`：目标 VIP 不在线，或者 VIP 分配和测试命令不一致。
- `exit disabled, dropping`：目标不在 overlay 内，当前没有 exit gateway。

已修复过的关键问题：

- Linux `tun write failed: invalid offset`：wireguard-go Linux TUN 后端需要读写 buffer headroom；当前 `internal/tun/tun.go` 使用 `tunPacketOffset = 16`。
- standby 空闲读超时导致假断连样本：连接池读取 WebSocket 不再给正常 read 加固定短超时，计划内关闭也不会喂给 timeout detector。

## 未来服务端出口配置示例

以下是未来 exit gateway 的 Linux 配置思路，不代表当前项目已有完整代码。

开启 IPv4 转发：

```sh
sysctl -w net.ipv4.ip_forward=1
```

使用 iptables 做 NAT：

```sh
iptables -t nat -A POSTROUTING -s 10.66.0.0/24 -o eth0 -j MASQUERADE
```

## 未来客户端 Exit 路由示例

仅 overlay（当前默认）：

```sh
ip route add 10.66.0.0/24 dev wsvpn0
```

启用 exit：

```sh
ip route add <server_public_ip>/32 via <original_gateway>
ip route add 0.0.0.0/1 dev wsvpn0
ip route add 128.0.0.0/1 dev wsvpn0
```

必须保留到服务端公网 IP 的真实路由，否则 WebSocket 连接可能被送进自己的隧道。

## 反向代理

WebSocket 可以放在 HTTPS 反向代理后面：

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
ipv6: disabled in current version
```

## 诊断建议

常用检查：

- 客户端 TUN 是否创建成功。
- 客户端虚拟 IP 是否配置成功。
- 客户端路由表是否有 overlay connected route。
- WebSocket 是否连接成功，standby 是否已注册。
- 服务端是否注册了正确的虚拟 IP 和连接数。
- 服务端是否因为目标 IP 未命中而丢包。
- 服务端是否因为 source IP 不等于 VIP 而丢包。
- 是否出现无故 rotation/reconnect。
- Windows TUN MTU 和分片行为是否异常。
- 服务端出口模式下 `ip_forward` 和 NAT 是否配置正确。

建议后续内置诊断命令：

```text
wsvpn diag routes
wsvpn diag tun
wsvpn diag pool
wsvpn diag server
```
