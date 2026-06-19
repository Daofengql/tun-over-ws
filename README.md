# wsvpn

一个基于 WebSocket 的三层 VPN 组网工具，Go 单二进制实现。

## 当前状态

阶段 0-3 已完成：WebSocket relay、VIP 动态分配、Windows TUN 客户端。
单机双客户端互 ping 验证通过（<1ms 延迟，0% 丢包）。

## 快速开始

### 构建

```bash
go build -o bin/wsvpn.exe ./cmd/wsvpn/
```

Windows 需要 `wintun.dll` 放在 `bin/` 目录下。

### 运行测试（需要管理员权限）

```powershell
# 终端 1：服务端
.\bin\wsvpn.exe server -c testdata\server.yaml

# 终端 2：客户端 A
.\bin\wsvpn.exe client -c testdata\client-a.yaml

# 终端 3：客户端 B
.\bin\wsvpn.exe client -c testdata\client-b.yaml

# 终端 4：测试连通性
ping -S 10.66.0.2 10.66.0.3
```

或使用测试脚本：

```powershell
.\scripts\test-tun.ps1
```

## 架构

```text
Client A TUN (10.66.0.2)
    -> wsvpn client
    -> WebSocket (30s heartbeat)
    -> Server relay (VIP 分配 + 转发)
    -> WebSocket
    -> wsvpn client
    -> Client B TUN (10.66.0.3)
```

## 项目结构

```text
cmd/wsvpn/          CLI 入口（cobra server/client 子命令）
internal/
  config/           YAML 配置解析
  conn/             客户端连接、心跳、断线重连
  logger/           彩色终端日志
  packet/           IPv4 包头解析
  relay/            服务端 relay、VIP 分配、转发
  tun/              TUN 设备（wireguard-go）+ 平台 IP 配置
bin/                构建产物（gitignored）
testdata/           测试配置文件
docs/               设计文档
```

## 设计文档

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — 架构说明
- [docs/ROADMAP.md](docs/ROADMAP.md) — 开发路线（含完成状态）
- [docs/OPERATIONS.md](docs/OPERATIONS.md) — 配置和部署
- [docs/HANDOFF.md](docs/HANDOFF.md) — 项目上下文和决策记录
