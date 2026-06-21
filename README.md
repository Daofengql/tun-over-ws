# tun-over-ws

`tun-over-ws` 是一个基于 WebSocket 的三层 overlay 组网工具。服务端和客户端分别构建为 `wsvpns` 与 `wsvpnc`。

它不是 SOCKS/HTTP 代理，而是通过 TUN 虚拟网卡接收原始 IPv4 包，把这些包放进 WebSocket binary frame，再由中心服务端按虚拟 IP 转发。

## 当前状态

当前已经完成 overlay 内网数据路径：

- 服务端 relay、VIP 动态分配、source VIP 校验。
- Windows 和 Linux TUN 客户端。
- 固定大小 WebSocket 连接池：单 primary + 多 standby。
- primary 断开后的 standby 提升、自动补建和重连。
- TCP flow 绑定和背压模型：已有 TCP flow 不跨 WebSocket，TCP 队列满时阻塞 TUN 读取路径，让系统 TCP 自然降速。
- UDP 复用 WebSocket 池，但采用短等待、standby 突发和可丢弃策略。
- 组播、广播、IGMP 等噪声流量过滤。
- CDN/nginx 连接寿命探测和计划轮换；计划内关闭不会参与超时学习。
- 管理控制台基础版：设备授权、AK/RK 设备认证、SQLite 设备持久化。

已经验证：

- `go test -timeout 60s ./...` 通过。
- Windows 和 Linux amd64 二进制构建通过。
- Windows 单机双客户端 overlay ping 通过。
- Linux 服务端 + Linux 客户端 + Windows 客户端 overlay ping 通过。
- 跨平台 `iperf3` 基础测试通过：UDP 1M/5M 双向 0% 丢包，TCP 双向可通。
- 修复 standby 空闲读超时后，压测期间没有再出现无故 primary 轮换或重连风暴。

仍未实现：

- 服务端出口（exit gateway）、server TUN、NAT 和默认路由接管。
- 生产级认证加固：HTTPS 部署、审计日志、ACL、密钥轮换。
- 配置热加载。
- Linux -> Windows TCP 方向吞吐仍偏低且重传较多，后续需要继续查 Windows TUN MTU、写入路径和分片行为。

## 构建

Windows：

```powershell
go build -o .\bin\wsvpns.exe .\cmd\wsvpns
go build -o .\bin\wsvpnc.exe .\cmd\wsvpnc
```

Linux amd64：

```powershell
$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -o .\bin\wsvpns-linux-amd64 .\cmd\wsvpns
go build -o .\bin\wsvpnc-linux-amd64 .\cmd\wsvpnc
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH
```

验证：

```powershell
go test -timeout 60s ./...
go vet ./...
```

Windows 运行客户端需要把 `wintun.dll` 放在 `bin\` 目录下。Linux 运行客户端需要 root 权限、`/dev/net/tun` 和 `iproute2`。

## 管理台开发

生产模式下 `wsvpns` 会使用嵌入的 `internal/admin/static` 前端资源。开发时前后端可以独立运行：

```powershell
# 后端
go run .\cmd\wsvpns -c .\testdata\server.yaml --log-level debug

# 前端
cd .\www
$env:VITE_API_TARGET = "http://127.0.0.1:18443"
npm run dev
```

Vite 会代理 `/api` 和 `/tunnel` 到后端；如果想让浏览器跨域直连 API，可设置 `VITE_API_BASE`，并在服务端配置 `admin.dev_origin`。

## 本地 Windows 测试

需要管理员 PowerShell。配置文件位于本地 `configs/` 目录，该目录不应提交。

```powershell
# 终端 1：服务端
.\bin\wsvpns.exe -c .\configs\local\server.yaml --log-level debug

# 终端 2：客户端 A，通常获得 10.66.0.2
.\bin\wsvpnc.exe -c .\configs\local\client-a.yaml --log-level debug

# 终端 3：客户端 B，通常获得 10.66.0.3
.\bin\wsvpnc.exe -c .\configs\local\client-b.yaml --log-level debug

# 终端 4：指定源地址测试 overlay
ping -S 10.66.0.2 10.66.0.3
```

也可以使用脚本：

```powershell
.\scripts\test-tun.ps1
```

脚本会读取 `bin\wsvpnc.exe`、`bin\wsvpns.exe` 和 `bin\wintun.dll`。

## 架构简图

```text
Client A TUN (10.66.0.2)
    -> wsvpnc
    -> WebSocket binary frame
    -> Server relay (VIP allocation + overlay forwarding)
    -> WebSocket binary frame
    -> wsvpnc
    -> Client B TUN (10.66.0.3)
```

服务端收到客户端 IP 包后的核心分支：

```text
if source_ip != client_virtual_ip:
    drop
else if destination is inside overlay_cidr:
    forward to the client that owns destination VIP
else:
    drop for now; exit mode is not implemented yet
```

## 项目结构

```text
cmd/wsvpns/   服务端 CLI 入口
cmd/wsvpnc/   客户端 CLI 入口
internal/
  config/           YAML 配置解析和基础校验
  conn/             客户端连接池、心跳、背压、重连、TUN/WS 数据泵
  logger/           彩色终端日志
  packet/           IPv4 包头解析和流量分类
  relay/            服务端 relay、VIP 分配、转发、源地址校验
  tun/              TUN 设备（wireguard-go）+ 平台 IP 配置
bin/                构建产物（gitignored）
configs/            本地配置文件（gitignored）
testdata/           本地测试配置/数据（gitignored）
docs/               架构、运维、路线和交接文档
```

## 文档

- [架构说明](docs/ARCHITECTURE.md)
- [开发路线](docs/ROADMAP.md)
- [运行和测试手册](docs/OPERATIONS.md)
- [交接文档](docs/HANDOFF.md)

## 敏感信息约定

不要提交或写入公开文档：

- 远端公网 IP、SSH 地址、账号、密码、私钥、公钥路径。
- 临时 AK/RK/JWT、真实配置文件、测试 URL。
- `bin/`、`configs/`、`testdata/`、日志和抓包文件。
