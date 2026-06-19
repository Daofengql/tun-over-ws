# ws-vpn-go

一个计划中的单二进制 WebSocket 三层 VPN / 组网项目。

这个项目目前只初始化目录和设计文档，不包含 Go 代码。后续目标是构建一个 Go 二进制，运行时通过参数选择 `server` 或 `client` 模式：

```text
wsvpn server --config server.yaml
wsvpn client --config client.yaml
```

## 核心想法

客户端创建 TUN 虚拟网卡，把系统路由导入 TUN。程序从 TUN 读取原始 IP 包，通过 WebSocket binary frame 发给服务端。服务端根据目标 IP 做两类转发：

```text
1. Overlay 内网转发
   Client A -> Server -> Client B

2. 服务端出口转发
   Client A -> Server TUN -> Linux routing/NAT -> Internet
```

它更接近一个中心化的 WebSocket L3 VPN，而不是普通 HTTP/SOCKS 代理。

## 目标

- 单个 Go 二进制，同时支持服务端和客户端角色。
- Go 代码不堆在仓库根目录。
- 支持虚拟网段内客户端互通，例如 `10.66.0.0/24`。
- 支持服务端作为出口网关，为客户端访问公网或真实内网。
- 传输层先使用 WebSocket，便于穿透企业网络、反向代理和 `443` 端口部署。
- 第一阶段优先实现可靠、可调试的 MVP，而不是追求极致性能。

## 非目标

- 第一版不做 Tailscale 级别的 NAT 穿透和 P2P 直连。
- 第一版不做复杂 ACL 策略引擎。
- 第一版不手写用户态 TCP/IP 栈。
- 第一版不把 IP 包转换成 SOCKS/HTTP 代理请求。
- 第一版不承诺高吞吐和低延迟场景。

## 计划目录

```text
ws-vpn-go/
  README.md
  docs/
    HANDOFF.md
    ARCHITECTURE.md
    ROADMAP.md
    OPERATIONS.md
  cmd/
    wsvpn/
      # 未来放 main 包入口
  internal/
    app/
      # 未来放 server/client 启动编排
    config/
      # 未来放配置加载、校验、默认值
    packet/
      # 未来放 IPv4/IPv6 包头解析和基础校验
    relay/
      # 未来放 WebSocket relay、客户端表、路由分发
    tun/
      # 未来放跨平台 TUN 创建、读写、MTU 配置
```

## 推荐阅读顺序

1. [docs/HANDOFF.md](docs/HANDOFF.md)
2. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)
3. [docs/ROADMAP.md](docs/ROADMAP.md)
4. [docs/OPERATIONS.md](docs/OPERATIONS.md)

