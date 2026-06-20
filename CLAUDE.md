# Claude 接手说明

## 项目定位

`tun-over-ws` 是一个 Go 单二进制三层 overlay 组网工具。客户端通过 TUN 捕获原始 IPv4 包，并通过 WebSocket 连接中心服务端；服务端按虚拟 IP 转发包。

当前范围：

- overlay 客户端互通已经实现，并在 Windows/Linux 之间验证。
- WebSocket 连接池已经改为固定大小 primary/standby 模型。
- exit gateway 尚未实现。
- UUID/token 只是开发测试身份字段，后续会替换为服务端签名登录。

## 构建和测试

```powershell
go test -timeout 60s ./...
go vet ./...
go build -o .\bin\wsvpn.exe .\cmd\wsvpn

$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -o .\bin\wsvpn-linux-amd64 .\cmd\wsvpn
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH
```

不要提交 `bin/`、二进制、`wintun.dll`、日志、`configs/` 或 `testdata/`。

## 本地 Windows 运行

需要管理员 PowerShell：

```powershell
# 终端 1：服务端
.\bin\wsvpn.exe server -c .\configs\local\server.yaml --log-level debug

# 终端 2：客户端 A，通常拿到 10.66.0.2
.\bin\wsvpn.exe client -c .\configs\local\client-a.yaml --log-level debug

# 终端 3：客户端 B，通常拿到 10.66.0.3
.\bin\wsvpn.exe client -c .\configs\local\client-b.yaml --log-level debug

# 终端 4：指定源地址测试
ping -S 10.66.0.2 10.66.0.3
```

## 模块划分

- `cmd/wsvpn/`：Cobra CLI 入口，提供 `server` 和 `client` 子命令。
- `internal/config/`：YAML 配置加载和校验。
- `internal/packet/`：IPv4 包解析和 TCP/UDP/ICMP/噪声分类。
- `internal/relay/`：服务端 WebSocket relay、VIP 分配、source 校验、primary/standby aware 转发。
- `internal/conn/`：客户端连接池、心跳、自动重连、背压、TUN 数据泵。
- `internal/tun/`：TUN 设备封装和平台 IP 配置。
- `internal/logger/`：zerolog 彩色终端日志。

## 关键设计

- 服务端目标平台是 Linux。
- 客户端支持 Windows 和 Linux。
- VIP 由服务端统一分配，客户端不能自报虚拟 IP。
- WebSocket 使用 `github.com/coder/websocket`，每条连接独立心跳。
- 连接池是固定大小：一条 primary 承载常规流量，多条 standby 用于热备、突发和切换。
- TCP 使用 flow 绑定和有界队列背压，不再在热路径中用令牌桶随意丢 TCP 包。
- UDP 可复用 WebSocket 池，但策略是短等待、突发或丢弃，不阻塞 TCP。
- Windows 运行需要 `wintun.dll` 与二进制同目录。
- Linux TUN 读写使用 16 字节 packet offset/headroom。
- TLS 在 MVP 中建议由反向代理终止。

## 已验证

- `go test -timeout 60s ./...` 通过。
- Windows/Linux 二进制构建通过。
- Windows 单机双客户端 overlay ping 通过。
- Linux 服务端 + Linux 客户端 + Windows 客户端 overlay ping 通过。
- `iperf3` UDP 1M/5M 双向 0% 丢包。
- `iperf3` TCP 双向可通，但 Linux -> Windows 方向吞吐低且重传多。
- 修复后，压测期间没有再出现无故 primary 轮换、standby 空闲读超时导致的误判重连。

## 已知边界

- IPv6 包当前丢弃。
- `routes.exit.enabled` 不会配置默认路由。
- server TUN、NAT、exit 数据路径未实现。
- 认证不是生产级。
- `--send-to` 是占位参数。
- 不要在公开文档中写入远端 IP、账号、token 或临时测试 URL。
