# 开发路线

## 阶段 0：项目基础

目标：只搭框架和说明，不写网络功能。

已完成：

- 创建项目目录。
- 约定单二进制、多角色。
- 约定代码不堆在根目录。
- 写明 overlay 和 exit 两条数据路径。
- 写明 MVP 和风险。

## 阶段 1：包解析和配置

目标：建立可以测试的小模块。

任务：

- 定义配置格式。
- 解析 overlay CIDR、虚拟 IP、服务端地址、MTU、token。
- 实现 IPv4 包头解析。
- 从 IP 包中提取 source、destination、protocol。
- 对短包、非 IPv4 包、错误 header length 做校验。

验收：

- 单元测试覆盖正常 IPv4 包。
- 单元测试覆盖 malformed packet。
- 不需要真实 TUN。

## 阶段 2：WebSocket relay 原型

目标：先不接 TUN，用模拟 IP 包验证服务端转发表。

任务：

- 服务端接受 WebSocket 连接。
- 客户端发送 hello 注册虚拟 IP。
- 服务端维护 `virtual_ip -> client` 映射。
- 服务端解析 packet destination。
- overlay 命中时转发给目标客户端。
- 未命中时丢弃并记录原因。

验收：

- 两个模拟客户端连接服务端。
- A 发送目标为 B 的测试包，B 能收到。
- B 回包，A 能收到。

## 阶段 3：客户端 TUN MVP

目标：实现真实客户端组网。

任务：

- 客户端创建 TUN。
- 配置虚拟 IP 和 MTU。
- 添加 overlay 路由。
- TUN -> WebSocket 数据泵。
- WebSocket -> TUN 数据泵。
- 支持优雅退出时清理路由。

验收：

- 两台客户端机器或两个网络命名空间互 ping。
- TCP 服务可以通过虚拟 IP 访问。

## 阶段 4：服务端出口

目标：让服务端成为 egress gateway。

任务：

- 服务端创建 TUN，使用 `10.66.0.1/24`。
- 服务端读取 TUN 回包并转发给客户端。
- 客户端支持 exit mode 路由配置。
- 文档化 Linux `ip_forward` 和 NAT 配置。
- 服务端按客户端权限控制是否允许 exit。

验收：

- 客户端启用 exit 后能访问公网 IP。
- 客户端访问服务端 WebSocket 的连接不被隧道吞掉。
- 禁用 exit 的客户端无法把非 overlay 流量发出。

## 阶段 5：稳定性

目标：让 MVP 可持续运行。

任务：

- 心跳。
- 断线重连。
- 写队列和背压。
- 连接超时。
- 速率限制。
- 指标统计。
- 配置热加载可延后。

验收：

- 服务端重启后客户端能重连。
- 网络短断后能恢复。
- 慢客户端不会拖垮服务端全局转发。

## 阶段 6：安全增强

目标：降低真实部署风险。

任务：

- token 改为节点密钥或证书。
- 服务端固定分配虚拟 IP。
- 源 IP 绑定校验。
- ACL。
- 审计日志。
- 限制客户端可宣告路由。

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

