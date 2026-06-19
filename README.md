# wsvpn

`wsvpn` 是一个基于 WebSocket 的三层 VPN 组网工具，Go 单二进制实现。

它不是 SOCKS/HTTP 代理，而是通过 TUN 虚拟网卡接收原始 IPv4 包，再把这些包放进 WebSocket binary frame，由中心服务端按虚拟 IP 转发。

## 当前状态

阶段 0-3 已完成：项目骨架、配置、IPv4 包解析、WebSocket relay、VIP 动态分配、Windows/Linux TUN 客户端和 overlay 转发闭环。

已验证：

- Windows 单机双客户端互 ping 通过。
- Linux 服务端 + Linux 客户端 + Windows 客户端跨平台组网通过。
- Linux -> Windows overlay ping 验证通过，0% 丢包。
- Windows -> Linux overlay ping 验证通过，服务端可见双向转发日志。

当前仍是 MVP：

- 只实现 overlay 内网转发，exit gateway 还没有实现。
- UUID/token 只是测试阶段身份字段，后续会替换为服务端签名登录机制。
- 不接管默认路由；测试时使用显式源地址或接口。
- IPv6 第一版不支持，客户端日志里看到 IPv6 包会以 debug 级别丢弃。

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

Windows 运行客户端需要把 `wintun.dll` 放在 `bin\` 目录下。Linux 运行客户端需要 root 权限、`/dev/net/tun` 和 `iproute2`。

## 本地 Windows 测试

需要管理员 PowerShell。

```powershell
# 终端 1：服务端
.\bin\wsvpn.exe server -c .\configs\local\server.yaml --log-level debug

# 终端 2：客户端 A，通常获得 10.66.0.2
.\bin\wsvpn.exe client -c .\configs\local\client-a.yaml --log-level debug

# 终端 3：客户端 B，通常获得 10.66.0.3
.\bin\wsvpn.exe client -c .\configs\local\client-b.yaml --log-level debug

# 终端 4：指定源地址测试 overlay
ping -S 10.66.0.2 10.66.0.3
```

也可以使用脚本：

```powershell
.\scripts\test-tun.ps1
```

脚本会读取 `bin\wsvpn.exe` 和 `bin\wintun.dll`。

## 架构

```text
Client A TUN (10.66.0.2)
    -> wsvpn client
    -> WebSocket binary frame
    -> Server relay (VIP allocation + overlay forwarding)
    -> WebSocket binary frame
    -> wsvpn client
    -> Client B TUN (10.66.0.3)
```

服务端收到客户端 IP 包后的核心分支：

```text
if destination is inside overlay_cidr:
    forward to the client that owns destination VIP
else:
    drop for now; exit mode is not implemented yet
```

## 项目结构

```text
cmd/wsvpn/          CLI 入口（cobra server/client 子命令）
internal/
  config/           YAML 配置解析和基础校验
  conn/             客户端连接、心跳、断线重连、TUN/WS 数据泵
  logger/           彩色终端日志
  packet/           IPv4 包头解析
  relay/            服务端 relay、VIP 分配、转发、源地址校验
  tun/              TUN 设备（wireguard-go）+ 平台 IP 配置
bin/                构建产物（gitignored）
configs/            本地配置文件（gitignored）
docs/               设计、运维、路线和交接文档
```

## 设计文档

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) - 架构说明
- [docs/ROADMAP.md](docs/ROADMAP.md) - 开发路线和完成状态
- [docs/OPERATIONS.md](docs/OPERATIONS.md) - 构建、运行和测试手册
- [docs/HANDOFF.md](docs/HANDOFF.md) - 项目上下文、决策记录和剩余问题
