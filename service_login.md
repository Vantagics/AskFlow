# service.vantagedata.chat 接口实现文档

## 背景

VantageData 桌面应用的"客户服务"按钮需要实现一键 SSO 登录到客服系统（service.vantagedata.chat）。整体流程复用已有的 License 服务器 SN+Email 认证机制。

当前问题：service.vantagedata.chat 上的服务对 `/api/auth/sn-login` 请求返回了前端 SPA 的 HTML 页面，而非 JSON，导致客户端报错 `service portal returned HTML instead of JSON`。

## 认证流程

```
用户点击"客户服务"
    ↓
桌面应用检查 License 激活状态、SN、Email
    ↓
桌面应用 → POST https://license.vantagedata.chat/api/marketplace-auth {sn, email}
    ↓ 返回 {success, token}
桌面应用 → POST https://service.vantagedata.chat/api/auth/sn-login {token}
    ↓ 【需要实现】service portal 拿 token 去 License Server 验证
    ↓ 验证通过后查找或创建用户，生成一次性 login_ticket
    ↓ 返回 {success, login_ticket}
桌面应用在浏览器中打开 https://service.vantagedata.chat/auth/ticket-login?ticket=xxx
    ↓ 【需要实现】验证 ticket，创建会话，重定向到客服主页
```

## 依赖的已有接口（License Server）

service portal 需要调用 License Server 验证令牌：

```
POST https://license.vantagedata.chat/api/marketplace-verify
Content-Type: application/json

请求：
{"token": "<从客户端收到的 JWT>"}

成功响应（200）：
{"success": true, "sn": "XXXX-XXXX-XXXX", "email": "user@example.com"}

失败响应：
{"success": false, "message": "token expired", "code": "TOKEN_EXPIRED"}
```

## 需要实现的端点

### 端点 1：POST /api/auth/sn-login

桌面应用会 POST 到此端点，Content-Type 为 application/json。

#### 请求

```json
{"token": "<license_server_jwt>"}
```

#### 处理流程

1. 解析请求体，提取 `token` 字段
2. 如果 `token` 为空，返回 400 错误
3. 将 `token` 发送到 License Server `POST https://license.vantagedata.chat/api/marketplace-verify` 进行验证
4. 如果 License Server 验证失败，返回 401 错误
5. 验证成功后，用返回的 `email` 在本地数据库查找用户
   - 如果用户不存在：自动创建，`display_name` 取 email 的 `@` 前缀（如 `user@example.com` → `user`）
   - 如果用户已存在：更新最后登录时间
6. 生成一次性 `login_ticket`（建议 UUID，有效期 5 分钟，只能使用一次）
7. 将 ticket 存储到数据库或缓存中（关联用户 ID、创建时间、是否已使用）
8. 返回 JSON 响应

#### 成功响应（200）

```json
{
  "success": true,
  "login_ticket": "550e8400-e29b-41d4-a716-446655440000"
}
```

#### 失败响应

```json
// token 为空（400）
{"success": false, "message": "token is required"}

// License Server 验证失败（401）
{"success": false, "message": "license authentication failed: token expired"}

// License Server 不可达（502）
{"success": false, "message": "failed to contact license server"}

// 内部错误（500）
{"success": false, "message": "internal error"}
```

#### 关键约束

- 所有响应必须是 JSON 格式（`Content-Type: application/json`），绝不能返回 HTML
- 仅接受 POST 方法，其他方法返回 405
- `login_ticket` 必须是一次性的，使用后立即失效
- 同一 email 多次登录只创建一个用户（幂等）
- 无效令牌（空字符串、随机字符串、过期 JWT）必须返回 `success: false`

---

### 端点 2：GET /auth/ticket-login?ticket=xxx

用户的浏览器会直接访问此 URL（由桌面应用调用系统浏览器打开）。

#### 请求

```
GET /auth/ticket-login?ticket=550e8400-e29b-41d4-a716-446655440000
```

#### 处理流程

1. 从 URL query 参数提取 `ticket`
2. 在数据库/缓存中查找该 ticket
3. 验证：ticket 存在、未过期（5 分钟内）、未被使用过
4. 验证通过：
   - 标记 ticket 为已使用
   - 为关联的用户创建会话（设置 session cookie）
   - 302 重定向到客服系统主页（如 `/dashboard` 或 `/`）
5. 验证失败：
   - 302 重定向到登录页，可附带错误参数（如 `/login?error=invalid_ticket`）

#### 关键约束

- ticket 只能使用一次，第二次使用同一 ticket 必须失败
- ticket 有效期建议 5 分钟
- 这是浏览器直接访问的页面，返回 302 重定向而非 JSON

---

## 路由注意事项

当前 service.vantagedata.chat 的 `/api/auth/sn-login` 返回了 SPA 的 HTML 页面，说明 API 路由被 SPA 的 catch-all 规则覆盖了。

需要确保：
- `/api/*` 路径优先走后端 API 处理
- `/auth/*` 路径优先走后端认证处理
- 只有不匹配任何 API/认证路由的请求才 fallback 到 SPA 的 `index.html`

## 参考实现

market.vantagedata.chat（端口 8088）上已有类似的 `handleSNLogin` 实现，逻辑几乎一样。区别在于：
- marketplace 的 sn-login 最后返回的是 JWT token（用于 API 调用）
- service portal 的 sn-login 最后返回的是一次性 login_ticket（用于浏览器跳转登录）

## 数据模型建议

### users 表

| 字段 | 类型 | 说明 |
|------|------|------|
| id | INTEGER PRIMARY KEY | 用户 ID |
| email | TEXT UNIQUE | 用户邮箱（唯一标识） |
| display_name | TEXT | 显示名称（首次登录时从 email 提取） |
| sn | TEXT | License SN |
| last_login_at | DATETIME | 最后登录时间 |
| created_at | DATETIME | 创建时间 |

### login_tickets 表

| 字段 | 类型 | 说明 |
|------|------|------|
| ticket | TEXT PRIMARY KEY | 一次性票据（UUID） |
| user_id | INTEGER | 关联用户 ID |
| used | BOOLEAN DEFAULT FALSE | 是否已使用 |
| created_at | DATETIME | 创建时间 |
| expires_at | DATETIME | 过期时间（created_at + 5min） |

## 测试验证

实现完成后，可用以下命令验证：

```bash
# 1. 先从 License Server 获取 token
curl -X POST https://license.vantagedata.chat/api/marketplace-auth \
  -H "Content-Type: application/json" \
  -d '{"sn":"YOUR-SN","email":"your@email.com"}'

# 2. 用 token 调用 sn-login（应返回 JSON，不是 HTML）
curl -X POST https://service.vantagedata.chat/api/auth/sn-login \
  -H "Content-Type: application/json" \
  -d '{"token":"TOKEN-FROM-STEP-1"}'

# 3. 在浏览器中打开 ticket-login URL
# https://service.vantagedata.chat/auth/ticket-login?ticket=TICKET-FROM-STEP-2
```
