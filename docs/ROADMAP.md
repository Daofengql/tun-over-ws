# 开发路线

## 阶段 0：项目基础

目标：只搭框架和说明，不写网络功能。

**已完成。**

- 创建项目目录。
- 约定单二进制、多角色。
- 约定代码不堆在根目录。
- 写明 overlay 和 exit 两条数据路径。
- 写明 MVP 和风险。

## 阶段 1：包解析和配置

目标：建立可以测试的小模块。

**已完成。**

- 定义 YAML 配置格式。
- 解析 overlay CIDR、服务端地址、MTU、token。
- 配置校验要求 overlay 和 server TUN IP 为 IPv4。
- 配置校验要求 server TUN IP 位于 overlay CIDR 内。
- 实现 IPv4 包头解析。
- 从 IP 包中提取 source、destination、protocol。
- 对短包、非 IPv4 包、错误 header length、total length 做校验。
- 单元测试覆盖正常和 malformed 包。

## 阶段 2：WebSocket relay 原型

目标：先不接 TUN，用模拟 IP 包验证服务端转发表。

**已完成。**

- 服务端接受 WebSocket 连接。
- 客户端发送 hello 注册，服务端分配虚拟 IP（DHCP 模式）。
- 服务端维护 `virtual_ip -> client` 映射。
- 服务端解析 packet destination。
- 服务端校验 packet source 必须等于连接分配的 VIP。
- overlay 命中时转发给目标客户端。
- 未命中时丢弃并记录原因。
- 端到端测试：两个模拟客户端通过 relay 互发包成功。

## 阶段 3：客户端 TUN MVP

目标：实现真实客户端组网。

**已完成（Windows + Linux overlay）。**

- 客户端创建 TUN（wireguard-go + Wintun/Linux TUN）。
- Windows 配置虚拟 IP（`netsh`，不修改系统默认路由）。
- Linux 配置虚拟 IP（`ip addr add` + `ip link set up`，不修改默认路由）。
- TUN -> WebSocket 数据泵。
- WebSocket -> TUN 数据泵。
- Linux TUN read/write 使用 `tunPacketOffset = 16`，修复 wireguard-go 后端的 offset 要求。
- Windows 单机两客户端互 ping 验证通过：`ping -S 10.66.0.2 10.66.0.3`。
- Linux/Windows overlay 验证通过。
- `wintun.dll` 需放在 Windows 二进制同目录。

待做：

- 优雅退出时更完整地清理路由和 TUN 设备。
- 将跨平台 overlay 测试流程沉淀为可重复脚本。
- 增加 TUN 平台层的错误提示和诊断。

## 阶段 4：服务端出口

目标：让服务端成为 egress gateway。

**未开始。**

任务：

- 服务端创建 TUN，使用 `10.66.0.1/24`。
- 服务端读取 TUN 回包并转发给客户端。
- 客户端支持 exit mode 路由配置。
- 文档化 Linux `ip_forward` 和 NAT 配置。
- 服务端按客户端权限控制是否允许 exit。
- 确保 WebSocket 服务端公网 IP 不被默认路由送进隧道。

验收：

- 客户端启用 exit 后能访问公网 IP。
- 客户端访问服务端 WebSocket 的连接不被隧道吞掉。
- 禁用 exit 的客户端无法把非 overlay 流量发出。

## 阶段 5：稳定性

目标：让 MVP 可持续运行。

**部分完成。**

已完成：

- 心跳（30s WebSocket Ping，服务端和客户端双向）。
- 断线重连（指数退避，最大 30s）。
- VIP 跨重连保持（UUID 绑定，进程内存级别）。
- 连接替换（同 UUID 重连时替换旧连接）。
- 写队列 512 缓冲，满时丢包并告警。
- buffer pool 减少 TUN read GC 压力。
- 服务端连接生命周期受根 context 控制，服务端退出时能推动连接关闭。

待做：

- 连接替换和并发注销的单元测试。
- 写队列背压（慢客户端主动限速）。
- 连接超时配置化。
- 指标统计（转发包数/字节数/丢包原因）。
- 配置热加载。
- 客户端 TUN 生命周期和重连生命周期拆分得更清楚。

验收：

- 服务端重启后客户端能重连。
- 网络短断后能恢复。
- 同 UUID 频繁重连不会破坏最新连接的 VIP 映射。

## 阶段 6：安全增强

目标：降低真实部署风险。

**未开始。**

任务：

- token 改为节点密钥、证书或服务端签名登录。
- 服务端持久化节点身份和虚拟 IP。
- ACL。
- 审计日志。
- 限制客户端可宣告路由。
- 限制客户端速率和连接频率。

已完成的安全基础：

- 虚拟 IP 由服务端统一分配。
- 服务端检查客户端发来的 packet source IP 是否等于该连接分配的 VIP。
- 同一 UUID 只保留一个活跃连接。

验收：

- 客户端不能伪造其他节点的 source IP。
- 未授权节点无法注册。
- 未授权节点无法使用 exit。

## 阶段 7：性能和传输扩展

目标：解决 WebSocket 的性能上限。

候选方向：

- QUIC transport。
- UDP transport。
- 多 WebSocket 连接分流。
- 批量收发。
- 自适应 MTU。
- MSS clamping。

注意：

WebSocket 是为了 MVP 和部署友好，不一定是长期最佳数据通道。
