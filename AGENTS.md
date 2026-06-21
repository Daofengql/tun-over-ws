# AI 接手说明

## 项目概览

`tun-over-ws` 是一个 Go 三层组网工具。客户端创建 TUN 虚拟网卡，把原始 IPv4 包通过 WebSocket 发给中心 relay 服务端；服务端按虚拟 IP 转发 overlay 内网流量。服务端和客户端分别构建为 `wsvpns` / `wsvpnc`。

当前主线是 overlay 客户端互通。服务端出口（exit gateway）还没有实现。

## 当前进度

已完成并验证：

- 配置解析、IPv4 包解析、WebSocket relay、VIP 动态分配。
- Windows 和 Linux TUN 客户端。
- 固定大小 WebSocket 连接池：单 primary + 多 standby。
- primary 断开后的 standby 提升、后台补建、重连。
- TCP flow 绑定和背压模型：已有 TCP flow 不跨连接乱序，队列满时阻塞 TUN 读取路径，让内层 TCP 自然降速。
- UDP 独立策略：primary 满时可尝试 standby，仍不可用则快速丢弃。
- 组播、广播、IGMP 等噪声流量过滤。
- CDN/nginx 连接寿命探测和计划轮换；计划内关闭不会再被误判为远端超时。
- Windows 单机双客户端 overlay ping。
- Linux 服务端 + Linux 客户端 + Windows 客户端 overlay ping。
- 跨平台 `iperf3` 测试：UDP 10M 双向 0% 丢包；TCP 双向 42-54 Mbits/sec，Linux -> Windows 方向 0 重传。

未实现或仍需谨慎处理：

- exit gateway、server TUN、NAT 和默认路由接管。
- 生产级认证加固；当前已有管理台 JWT、设备 AK/RK 和 machine-id 派生 device_id，仍需 HTTPS 部署、ACL、审计等生产能力。
- ACL、审计日志、持久化节点/VIP、配置热加载。

## 目录约定

```text
tun-over-ws/
  bin/                  构建产物，忽略提交
  cmd/wsvpns/     服务端 Cobra CLI 入口
  cmd/wsvpnc/     客户端 Cobra CLI 入口
  internal/
    config/             YAML 配置解析和校验
    conn/               客户端连接池、QoS 观测、背压、TUN 数据泵
    logger/             zerolog 彩色终端日志
    packet/             IPv4 包解析和流量分类
    relay/              服务端 relay、VIP 分配、转发、源地址校验
    tun/                TUN 封装和平台 IP 配置
  configs/              本地配置，忽略提交
  scripts/              辅助脚本
  docs/                 架构、运维、路线和交接文档
```

## 测试和文件约定

1. 测试用的配置文件全部放在 `testdata/` 下，非必要不创建多个版本的 YAML。
2. 构建产物（`wsvpn` 二进制、`wsvpn-linux-amd64`）放在 `bin/`，不要提交。
3. `iperf3` 位于 `bin/iperf3-win/`，性能测试时可直接使用，测试端口推荐 26001。
4. `wintun.dll` 放在 `bin/` 目录下，与 Windows 二进制同目录，不要提交。
5. 测试产生的日志文件用完即删除；如需保留，放在 `testdata/logs/` 下，不要提交。
6. 远端部署的临时文件放在目标用户主目录下，测试完毕后清理。
7. 不要在文档或提交中写入远端 IP、登录信息、token 或临时 URL。

## 开发规则

1. Go 代码只放在 `cmd/` 和 `internal/`，不要在仓库根目录放 `.go` 文件。
2. 平台相关代码使用 `_windows.go`、`_linux.go` 文件后缀。
3. 测试使用标准 `_test.go` 命名，优先运行 `go test ./...`。
4. 配置格式只使用 YAML。
5. 除非用户明确要求，否则不要开始 exit gateway 工作。
6. 客户端不再配置 UUID/token；使用 machine-id 派生 device_id，设备认证使用 AK/RK，管理台登录使用 JWT。

## 常用命令

```powershell
go test -timeout 60s ./...
go vet ./...
go build -o .\bin\wsvpns.exe .\cmd\wsvpns
go build -o .\bin\wsvpnc.exe .\cmd\wsvpnc

$env:GOOS = "linux"
$env:GOARCH = "amd64"
go build -o .\bin\wsvpns-linux-amd64 .\cmd\wsvpns
go build -o .\bin\wsvpnc-linux-amd64 .\cmd\wsvpnc
Remove-Item Env:\GOOS
Remove-Item Env:\GOARCH
```

Windows 本地集成测试需要管理员 PowerShell：

```powershell
.\bin\wsvpns.exe -c .\configs\local\server.yaml --log-level debug
.\bin\wsvpnc.exe -c .\configs\local\client-a.yaml --log-level debug
.\bin\wsvpnc.exe -c .\configs\local\client-b.yaml --log-level debug
ping -S 10.66.0.2 10.66.0.3
```

跨平台测试只使用 overlay connected route 和显式源地址/接口，不修改默认路由。

## 关键依赖

- `github.com/coder/websocket`：WebSocket 传输。
- `golang.zx2c4.com/wireguard/tun`：跨平台 TUN。
- `github.com/spf13/cobra`：CLI。
- `github.com/rs/zerolog`：日志。
- `gopkg.in/yaml.v3`：YAML 配置。

## 实现注意事项

- Linux TUN 读写需要保留 headroom；`internal/tun/tun.go` 中的 `tunPacketOffset = 16` 不要轻易删除。
- IPv6 当前不支持，会以 debug 日志丢弃。
- `routes.exit.enabled` 目前不会配置默认路由。
- `--send-to` 仍是占位测试参数，不会注入真实数据包。
- 连接池当前是 primary/standby 背压模型，不是多连接加权随机分流。
