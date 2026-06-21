# 管理控制台设计方案

## 概述

为 wsvpns 添加 Web 管理控制台，类似 Tailscale 的设备授权和管理体验。

## 核心决策

- 服务端和客户端拆分为两个二进制
- Web 控制台与 WebSocket 隧道共用同一端口
- 前端使用 React + Material UI
- 数据库使用 SQLite（MVP），抽象接口支持后续扩展 MySQL

## 认证体系

| 术语 | 全称 | 用途 | 有效期 | 使用方 |
|------|------|------|--------|--------|
| `session_code` | - | 设备授权会话标识，一次性 | 10 分钟 | 客户端（显示链接） |
| `ak` | Access Key | 客户端日常认证 | 24 小时 | 客户端 → 服务端 |
| `rk` | Refresh Key | 刷新 AK，不出设备 | 90 天 | 客户端本地保存 |
| `jwt` | JSON Web Token | 管理员 Web 控制台登录 | 24 小时 | 浏览器 → 服务端 |

**设计原则：** JWT 仅用于 Web 管理界面，客户端设备使用 AK/RK 认证。

## 设备授权流程

```
┌──────────┐         ┌──────────┐         ┌──────────┐
│  Client   │         │  Server   │         │  Browser  │
└─────┬────┘         └─────┬────┘         └─────┬────┘
      │                    │                    │
      │  1. WS 连接        │                    │
      │  {device_info}     │                    │
      │───────────────────>│                    │
      │                    │                    │
      │  2. 不存在则创建设备，自动分配 VIP       │
      │                    │                    │
      │  3. 返回 auth_url + session_code       │
      │<───────────────────│                    │
      │                    │                    │
      │  4. 打印链接等待    │                    │
      │  "请访问 https://...│                    │
      │   来授权此设备"     │                    │
      │                    │                    │
      │                    │  5. 用户打开链接     │
      │                    │<───────────────────│
      │                    │                    │
      │                    │  6. 登录 + 授权     │
      │                    │<───────────────────│
      │                    │                    │
      │  7. 通过 WS 推送   │                    │
      │  {ak, rk, vip}     │                    │
      │<───────────────────│                    │
      │                    │                    │
      │  8. 保存 ak/rk     │                    │
      │  后续用 ak 认证     │                    │
      │───────────────────>│                    │
```

**关键逻辑：** `device_id` 不存在时自动创建设备，首次创建从 VIP 池中随机分配 IP 并记录，用户可在控制台再次设置自定义 IP。

## 客户端设备标识

采集多维度组合生成设备唯一标识：

```go
type DeviceInfo struct {
    OS        string `json:"os"`         // windows / linux
    Arch      string `json:"arch"`       // amd64 / arm64
    Hostname  string `json:"hostname"`   // 可重复，辅助用
    MachineID string `json:"machine_id"` // 核心标识
}
```

`machine_id` 采集策略：

| 平台 | 来源 | 稳定性 |
|------|------|--------|
| Windows | `HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid` | 重装系统才变 |
| Linux | `/etc/machine-id` 或 `/var/lib/dbus/machine-id` | 重装系统才变 |

客户端使用 `machine_id` 派生稳定 `device_id`，不再生成随机 UUID。AK/RK 持久化到 `~/.wsvpn/device.json`。

## 数据库设计

### 表结构

```sql
-- 管理员（Web 控制台登录）
CREATE TABLE admins (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    username   TEXT NOT NULL UNIQUE,
    password   TEXT NOT NULL,        -- bcrypt hash
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 设备
CREATE TABLE devices (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id       TEXT NOT NULL UNIQUE,      -- machine_id 派生的设备唯一标识
    device_info     TEXT NOT NULL,             -- JSON: {os, arch, hostname, machine_id}
    name            TEXT DEFAULT '',           -- 用户在 Web 端设置的可读名称
    virtual_ip      TEXT DEFAULT NULL,         -- 手动指定 VIP，NULL = 使用自动分配的
    auto_vip        TEXT NOT NULL,             -- 首次创建时自动分配的 VIP
    status          TEXT DEFAULT 'pending',    -- pending / approved / revoked
    access_key      TEXT DEFAULT NULL,         -- AK (hashed)
    refresh_key     TEXT DEFAULT NULL,         -- RK (hashed)
    key_expires_at  DATETIME,                 -- AK 过期时间
    rk_expires_at   DATETIME,                 -- RK 过期时间
    approved_by     INTEGER REFERENCES admins(id),
    last_seen_at    DATETIME,
    created_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at      DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- 设备授权会话（临时）
CREATE TABLE auth_sessions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_code TEXT NOT NULL UNIQUE,         -- 随机生成，给客户端显示
    device_id    TEXT NOT NULL,
    status       TEXT DEFAULT 'pending',       -- pending / approved / expired
    expires_at   DATETIME NOT NULL,            -- 10 分钟后过期
    created_at   DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_auth_sessions_code ON auth_sessions(session_code);
CREATE INDEX idx_devices_device_id ON devices(device_id);
CREATE INDEX idx_devices_ak ON devices(access_key);
```

### VIP 分配逻辑

```
设备连接 → 查找 devices 表
  ├── 不存在 → 创建设备，auto_vip = 从池中随机分配，virtual_ip = NULL
  └── 存在 → 使用 virtual_ip（若有）或 auto_vip
```

用户在控制台修改 VIP 时更新 `virtual_ip` 字段，`auto_vip` 保持不变作为备份。

## API 设计

### WebSocket 隧道

```
ws://server:8443/tunnel          # 原有隧道，认证改为 AK
```

### HTTP API（同端口，不同路径）

```http
# ========= 设备认证（无需登录） =========

POST /api/auth/init              # 客户端发起授权
POST /api/auth/poll              # 客户端轮询授权结果
POST /api/auth/refresh           # 用 RK 刷新 AK

# ========= Web 控制台（需要 JWT） =========

POST /api/web/login              # 管理员登录 → JWT
GET  /api/web/devices            # 设备列表
GET  /api/web/devices/:device_id      # 设备详情
PUT  /api/web/devices/:device_id      # 修改设备（名称、VIP、禁用）
DELETE /api/web/devices/:device_id    # 删除设备
POST /api/web/devices/:device_id/approve   # 批准设备
POST /api/web/devices/:device_id/revoke    # 吊销设备

# ========= 静态文件 =========

GET  /admin/*                    # React SPA
```

### WebSocket 握手改造

```json
// 客户端发送
{
  "type": "hello",
  "device_id": "dev-a1b2c3d4...",
  "ak": "hex-string...",
  "hostname": "daofeng-laptop"
}

// 服务端响应（成功）
{
  "type": "hello_ok",
  "virtual_ip": "10.66.0.2",
  "overlay_cidr": "10.66.0.0/24",
  "mtu": 1280,
  "routes": ["10.66.0.0/24"]
}
```

## 代码结构

```
cmd/
  wsvpns/
    main.go              # Cobra 入口，加载配置，启动各模块
  wsvpnc/
    main.go              # 复用现有 client 逻辑

internal/
  config/
    server.go            # ServerConfig（新增 DB、Admin 字段）
    client.go            # ClientConfig（简化，去掉 token）

  relay/
    server.go            # WS relay，改为从 Store 查询设备认证
    allocator.go         # VIPAllocator 增加 DB 支持

  store/                 # 新增
    store.go             # Store 接口定义
    sqlite.go            # SQLite 实现
    mysql.go             # MySQL 实现（后期）

  admin/                 # 新增
    handler.go           # HTTP 路由 + API handler
    auth.go              # 管理员认证中间件（JWT）
    device_auth.go       # 设备授权流程（init/poll/approve）
    static/              # 嵌入的前端文件
      index.html
      ...

  conn/                  # 客户端连接池
    deviceinfo.go        # 设备信息采集
    auth.go              # 设备授权流程（客户端侧）

  packet/                # 不变
  tun/                   # 不变
  logger/                # 不变

frontend/                # React 前端（独立构建，输出嵌入 static/）
  src/
    App.tsx
    pages/
      Login.tsx
      Dashboard.tsx
      DeviceDetail.tsx
      DeviceAuth.tsx     # 设备授权页面
    components/
      DeviceTable.tsx
```

## 配置文件

### server.yaml

```yaml
listen: ":8443"
overlay_cidr: "10.66.0.0/24"

database:
  driver: "sqlite"
  dsn: "./data/wsvpn.db"

admin:
  username: "admin"
  password: "changeme"      # 首次启动创建
  jwt_secret: "replace-with-random-secret"
  static_dir: ""            # 开发时可指向 www/dist，空值使用嵌入资源
  dev_origin: ""            # 开发跨域来源，例如 http://127.0.0.1:5173

server_tun:
  enabled: false
  name: "wsvpn0"
  ip: "10.66.0.1"
  mtu: 1280

heartbeat:
  interval: 30s
```

### client.yaml（简化）

```yaml
server_url: "ws://127.0.0.1:18443/tunnel"
# device_id 由 machine_id 派生；ak/rk 自动管理，存储在 ~/.wsvpn/device.json
tun:
  name: "wsvpn0"
  mtu: 1280
```

### 客户端本地存储 ~/.wsvpn/device.json

```json
{
  "device_id": "dev-a1b2c3d4...",
  "device_info": {
    "os": "windows",
    "arch": "amd64",
    "hostname": "daofeng-laptop",
    "machine_id": "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx"
  },
  "ak": "...",
  "rk": "...",
  "ak_expires_at": "2026-06-22T10:00:00Z",
  "rk_expires_at": "2026-09-19T10:00:00Z"
}
```

### 前后端独立开发

生产模式下 `wsvpns` 使用 `internal/admin/static` 的嵌入前端资源。开发时允许前后端独立运行：

```powershell
# 后端
go run .\cmd\wsvpns -c .\testdata\server.yaml --log-level debug

# 前端
cd .\www
$env:VITE_API_TARGET = "http://127.0.0.1:18443"
npm run dev
```

Vite 开发服务器代理 `/api` 和 `/tunnel` 到后端；如需跨域直连，可设置 `VITE_API_BASE` 并在服务端配置 `admin.dev_origin`。

## 实现优先级

| Phase | 内容 | 工作量 |
|-------|------|--------|
| 1 | `Store` 接口 + SQLite 实现 | 2-3 天 |
| 2 | 设备自动创建 + VIP 自动分配 | 1 天 |
| 3 | 设备授权流程（init/poll/approve） | 2 天 |
| 4 | AK/RK 签发和刷新 | 1 天 |
| 5 | 改造 relay 认证逻辑 | 1 天 |
| 6 | Web 管理员登录 + JWT | 1 天 |
| 7 | React 前端（设备列表 + 授权页面） | 3-5 天 |
| 8 | 拆分二进制 + 构建脚本 | 1 天 |

## 安全考虑

1. **管理界面必须 HTTPS**：生产环境用反向代理终止 TLS
2. **密码存储**：bcrypt，禁止明文
3. **AK/RK 存储**：数据库中存 hash，不存明文
4. **AK/RK 随机生成**：`crypto/rand` 生成 32 字节 hex 字符串，数据库只保存 hash
5. **API 限流**：登录接口防暴力破解（5 次/分钟）
6. **CORS**：管理界面同源，API 不开放跨域
