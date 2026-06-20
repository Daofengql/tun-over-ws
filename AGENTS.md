# AI 接手说明

## 项目概览

`tun-over-ws` 是一个 Go 单二进制三层组网工具。客户端创建 TUN 虚拟网卡，把原始 IPv4 包通过 WebSocket 发给中心 relay 服务端；服务端按虚拟 IP 转发 overlay 内网流量。

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
- 跨平台 `iperf3` 基础测试：UDP 1M/5M 双向 0% 丢包；TCP 双向可通。

未实现或仍需谨慎处理：

- exit gateway、server TUN、NAT 和默认路由接管。
- 生产级认证；当前 UUID/token 只是开发测试字段。
- ACL、审计日志、持久化节点/VIP、配置热加载。
- Linux -> Windows TCP 吞吐仍偏低且有较多重传，后续需要继续查 Windows TUN MTU、写入路径和分片行为。

## 目录约定

```text
tun-over-ws/
  bin/                  构建产物，忽略提交
  cmd/wsvpn/            Cobra CLI 入口
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

## 开发规则

1. Go 代码只放在 `cmd/` 和 `internal/`，不要在仓库根目录放 `.go` 文件。
2. 平台相关代码使用 `_windows.go`、`_linux.go` 文件后缀。
3. 测试使用标准 `_test.go` 命名，优先运行 `go test ./...`。
4. 构建产物放在 `bin/`，不要提交二进制、`wintun.dll`、日志或测试配置。
5. 本地配置放在 `configs/` 或 `testdata/`，二者都不应被追踪。
6. 配置格式只使用 YAML。
7. 不要把远端 IP、登录信息、token、临时 URL 写入文档或提交历史。
8. 除非用户明确要求，否则不要开始 exit gateway 工作。
9. 不要把 UUID/token 当成生产认证；后续会替换为服务端签名登录。

## 常用命令

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

Windows 本地集成测试需要管理员 PowerShell：

```powershell
.\bin\wsvpn.exe server -c .\configs\local\server.yaml --log-level debug
.\bin\wsvpn.exe client -c .\configs\local\client-a.yaml --log-level debug
.\bin\wsvpn.exe client -c .\configs\local\client-b.yaml --log-level debug
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
