# Helpdesk 使用手册

## 1. 构建与启动

### 1.1 本地构建

```bash
# Linux / macOS
chmod +x build_local.sh
./build_local.sh

# 构建完成后启动
./helpdesk
```

```cmd
# Windows（需要 Go 环境）
go build -o helpdesk.exe .
helpdesk.exe
```

服务默认监听 `0.0.0.0:8080`，在浏览器访问 `http://localhost:8080` 即可使用。

### 1.2 命令行用法

```
helpdesk                                              启动 HTTP 服务（默认端口 8080）
helpdesk import [--product <product_id>] <目录> [...]  批量导入目录下的文档到知识库
helpdesk help                                         显示帮助信息
```

### 1.3 批量导入文档

```bash
# 导入单个目录（导入到公共库）
helpdesk import ./docs

# 导入多个目录
helpdesk import ./docs ./manuals /path/to/files

# 导入到指定产品
helpdesk import --product <product_id> ./docs
```

使用 `--product` 参数可将导入的文档关联到指定产品。不指定时，文档将导入到公共库。若指定的产品 ID 不存在，系统将报错并中止导入。

支持的文件格式：`.pdf` `.doc` `.docx` `.xls` `.xlsx` `.ppt` `.pptx` `.md` `.markdown`

---

## 2. 初始配置

### 2.1 管理员初始化

首次启动后，访问系统会提示设置超级管理员账户。通过 API 或前端界面完成：

```
POST /api/admin/setup
Content-Type: application/json

{
  "username": "admin",
  "password": "your_password"
}
```

设置完成后使用该账户登录管理后台。

### 2.2 配置文件

配置文件位于 `data/config.json`，包含以下配置项：

| 配置项 | 说明 |
|--------|------|
| `server.port` | HTTP 服务端口，默认 8080 |
| `llm.endpoint` | LLM API 地址（OpenAI 兼容） |
| `llm.api_key` | LLM API 密钥（自动加密存储） |
| `llm.model_name` | LLM 模型名称 |
| `llm.temperature` | 生成温度，默认 0.3 |
| `llm.max_tokens` | 最大生成 token 数，默认 2048 |
| `embedding.endpoint` | Embedding API 地址 |
| `embedding.api_key` | Embedding API 密钥（自动加密存储） |
| `embedding.model_name` | Embedding 模型名称 |
| `embedding.use_multimodal` | 是否启用多模态 Embedding |
| `vector.chunk_size` | 文本分块大小，默认 512 字符 |
| `vector.overlap` | 分块重叠字符数，默认 128 |
| `vector.top_k` | 检索返回的最相关片段数，默认 5 |
| `vector.threshold` | 相似度阈值，默认 0.5 |
| `product_intro` | 全局产品介绍文本（用于意图分类）。各产品可设置独立的 `welcome_message`，优先级高于此全局配置 |

API Key 在保存时会自动使用 AES-256-GCM 加密，加密密钥通过环境变量 `HELPDESK_ENCRYPTION_KEY` 设置。若未设置，系统会自动生成随机密钥。

### 2.3 SMTP 邮件配置

如需启用邮箱注册验证功能，需配置 SMTP：

| 配置项 | 说明 |
|--------|------|
| `smtp.host` | SMTP 服务器地址 |
| `smtp.port` | SMTP 端口，默认 587 |
| `smtp.username` | SMTP 用户名 |
| `smtp.password` | SMTP 密码 |
| `smtp.from_addr` | 发件人邮箱 |
| `smtp.from_name` | 发件人名称 |
| `smtp.use_tls` | 是否使用 TLS |

配置完成后可通过 `POST /api/email/test` 发送测试邮件验证配置是否正确。

---

## 3. 文档管理

### 3.1 上传文件

通过前端界面或 API 上传文档：

```
POST /api/documents/upload
Content-Type: multipart/form-data

file: <文件>
product_id: <产品ID>（可选，留空则导入到公共库）
```

支持格式：PDF、Word、Excel、PPT、Markdown。上传后系统自动完成解析、去重检查、分块、向量化和存储。

系统会自动进行内容去重：
- **文档级去重**：解析后计算内容 SHA-256 哈希，若已有相同内容的文档则拒绝导入并提示已有文档 ID
- **分块级去重**：分块后检查数据库中是否已有相同文本的分块，有则直接复用其向量，仅对新文本调用 Embedding API，节省 API 调用

### 3.2 通过 URL 导入

```
POST /api/documents/url
Content-Type: application/json

{
  "url": "https://example.com/document.pdf",
  "product_id": "<产品ID>"
}
```

`product_id` 为可选字段，留空则导入到公共库。

### 3.3 查看文档列表

```
GET /api/documents
GET /api/documents?product_id=<产品ID>
```

返回所有已上传文档及其状态（processing / success / failed）。支持通过 `product_id` 参数筛选特定产品的文档，列表中每个文档会显示所属产品名称或"公共库"标签。

### 3.4 删除文档

```
DELETE /api/documents/{id}
```

删除文档及其关联的所有向量数据。

### 3.5 下载原始文件

```
GET /api/documents/{id}/download
```

---

## 4. 智能问答

### 4.1 提交问题

```
POST /api/query
Content-Type: application/json

{
  "question": "如何安装产品？",
  "image_data": "",
  "product_id": "<产品ID>"
}
```

`image_data` 为可选字段，支持传入 base64 格式的图片数据进行多模态查询。`product_id` 为可选字段，指定后系统仅在该产品知识库和公共库中检索，不指定则在所有产品中检索。

### 4.2 回答流程

1. 系统对问题进行意图分类：
   - `greeting`：问候语，直接返回友好回复
   - `product`：产品相关问题，进入 RAG 检索流程
   - `irrelevant`：无关问题，提示用户提问产品相关内容
2. 将问题向量化，在知识库中检索 Top-K 相关片段
3. 将检索结果作为上下文，调用 LLM 生成回答
4. 若无匹配结果，创建待处理问题

### 4.3 响应格式

```json
{
  "answer": "根据文档，安装步骤如下...",
  "sources": [
    {
      "document_name": "安装指南.pdf",
      "chunk_index": 3,
      "snippet": "第一步：下载安装包..."
    }
  ],
  "is_pending": false
}
```

---

## 5. 待处理问题

### 5.1 查看待处理问题

```
GET /api/pending?status=pending
GET /api/pending?status=pending&product_id=<产品ID>
```

`status` 可选值：`pending`（待处理）、`answered`（已回答），留空返回全部。支持通过 `product_id` 参数筛选特定产品的问题。每个问题会显示所属产品名称。

### 5.2 回答待处理问题

```
POST /api/pending/answer
Content-Type: application/json

{
  "id": "问题ID",
  "answer": "管理员提供的回答"
}
```

回答内容会自动向量化并存入知识库。

### 5.3 删除待处理问题

```
DELETE /api/pending/{id}
```

---

## 6. 知识条目

管理员可直接添加文本和图片形式的知识条目，无需上传文档文件。

### 6.1 上传图片

```
POST /api/images/upload
Content-Type: multipart/form-data
Authorization: Bearer <session_token>

image: <图片文件>
```

支持格式：jpg、png、gif、webp、bmp。返回图片 URL。

### 6.2 添加知识条目

```
POST /api/knowledge
Content-Type: application/json
Authorization: Bearer <session_token>

{
  "title": "条目标题",
  "content": "知识内容文本",
  "image_urls": ["/api/images/xxx.png"],
  "product_id": "<产品ID>"
}
```

`product_id` 为可选字段，留空则添加到公共库。

---

## 7. 用户认证

### 7.1 管理员登录

```
POST /api/admin/login
Content-Type: application/json

{
  "username": "admin",
  "password": "your_password"
}
```

返回 session token，后续请求通过 `Authorization: Bearer <token>` 携带。

### 7.2 OAuth 社交登录

```
# 获取授权 URL
GET /api/oauth/url?provider=google

# 回调处理
POST /api/oauth/callback
{
  "provider": "google",
  "code": "authorization_code"
}
```

支持的 OAuth 提供商：Google、Apple、Amazon、Facebook。需在配置文件中设置对应的 `client_id`、`client_secret` 等参数。

### 7.3 邮箱注册与登录

```
# 获取验证码
GET /api/captcha

# 注册
POST /api/auth/register
{
  "email": "user@example.com",
  "password": "password",
  "captcha_id": "xxx",
  "captcha_answer": 42
}

# 邮箱验证
GET /api/auth/verify?token=xxx

# 登录
POST /api/auth/login
{
  "email": "user@example.com",
  "password": "password",
  "captcha_id": "xxx",
  "captcha_answer": 42
}
```

---

## 8. 管理员账户管理

超级管理员可创建和管理子管理员账户。

### 8.1 查看子管理员列表

```
GET /api/admin/users
Authorization: Bearer <super_admin_token>
```

### 8.2 创建子管理员

```
POST /api/admin/users
Authorization: Bearer <super_admin_token>

{
  "username": "editor1",
  "password": "password",
  "role": "editor",
  "product_ids": ["<产品ID1>", "<产品ID2>"]
}
```

`product_ids` 为可选字段，指定该子管理员负责的产品。不指定或为空数组时，该管理员可访问所有产品。

### 8.3 删除子管理员

```
DELETE /api/admin/users/{id}
Authorization: Bearer <super_admin_token>
```

### 8.4 查看当前角色

```
GET /api/admin/role
Authorization: Bearer <token>
```

---

## 9. 产品管理

超级管理员可创建和管理产品，每个产品拥有独立的知识库。

### 9.1 获取产品列表

```
GET /api/products
Authorization: Bearer <admin_token>
```

返回所有产品列表，包含 ID、名称、描述、欢迎信息等。

### 9.2 获取当前管理员的产品

```
GET /api/products/my
Authorization: Bearer <admin_token>
```

返回当前管理员被分配的产品列表。若管理员未分配任何产品，则返回所有产品。

### 9.3 创建产品

```
POST /api/products
Authorization: Bearer <super_admin_token>

{
  "name": "产品名称",
  "description": "产品描述",
  "welcome_message": "欢迎使用本产品，有什么可以帮您？"
}
```

产品名称必须唯一且非空。`welcome_message` 为可选字段，用于设置该产品的专属欢迎信息。

### 9.4 更新产品

```
PUT /api/products/{id}
Authorization: Bearer <super_admin_token>

{
  "name": "新名称",
  "description": "新描述",
  "welcome_message": "更新后的欢迎信息"
}
```

### 9.5 删除产品

```
DELETE /api/products/{id}
Authorization: Bearer <super_admin_token>
```

删除产品时，关联的文档和知识条目将解除产品关联（变为公共库内容）。

### 9.6 获取产品欢迎信息

```
GET /api/product-intro?product_id=<产品ID>
```

返回指定产品的欢迎信息。若该产品未设置欢迎信息，则返回全局 `product_intro` 配置。

---

## 10. 系统配置（API）

### 10.1 获取当前配置

```
GET /api/config
Authorization: Bearer <admin_token>
```

返回脱敏后的配置信息（API Key 部分隐藏）。

### 10.2 更新配置

```
PUT /api/config
Authorization: Bearer <super_admin_token>

{
  "llm": {
    "temperature": 0.5
  },
  "vector": {
    "top_k": 10
  }
}
```

仅超级管理员可修改配置。修改后自动热重载，无需重启服务。

---

## 11. 数据目录结构

```
data/
├── config.json          # 系统配置文件
├── encryption.key       # AES 加密密钥文件
├── helpdesk.db          # SQLite 数据库
├── uploads/             # 上传的原始文档
│   └── {doc_id}/        # 每个文档一个目录
│       └── filename.pdf
└── images/              # 知识条目图片
    └── {hash}.png
```
