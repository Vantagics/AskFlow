# Askflow 使用手册

## 1. 构建与启动

### 1.1 本地构建

```bash
# Linux / macOS
chmod +x build_local.sh
./build_local.sh

# 构建完成后启动
./askflow
```

```cmd
# Windows（需要 Go 环境）
go build -o askflow.exe .
askflow.exe
```

服务默认监听 `0.0.0.0:8080`，在浏览器访问 `http://localhost:8080` 即可使用。

### 1.2 命令行用法

```
askflow                                              启动 HTTP 服务（默认端口 8080）
askflow import [--product <product_id>] <目录> [...]  批量导入目录下的文档到知识库
askflow backup [选项]                                 备份整站数据
askflow restore <备份文件>                             从备份恢复数据
askflow help                                         显示帮助信息
```

### 1.3 批量导入文档

```bash
# 导入单个目录（导入到公共库）
askflow import ./docs

# 导入多个目录
askflow import ./docs ./manuals /path/to/files

# 导入到指定产品
askflow import --product <product_id> ./docs
```

使用 `--product` 参数可将导入的文档关联到指定产品。不指定时，文档将导入到公共库。若指定的产品 ID 不存在，系统将报错并中止导入。

支持的文件格式：`.pdf` `.doc` `.docx` `.xls` `.xlsx` `.ppt` `.pptx` `.md` `.markdown` `.mp4` `.avi` `.mkv` `.mov` `.webm`

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

API Key 在保存时会自动使用 AES-256-GCM 加密，加密密钥通过环境变量 `ASKFLOW_ENCRYPTION_KEY` 设置。若未设置，系统会自动生成随机密钥。

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

### 2.4 视频处理配置

如需启用视频检索功能，需配置外部工具路径：

| 配置项 | 说明 |
|--------|------|
| `video.ffmpeg_path` | ffmpeg 可执行文件路径（必需，为空则不支持视频） |
| `video.whisper_path` | whisper CLI 可执行文件路径（可选，为空则跳过语音转录） |
| `video.keyframe_interval` | 关键帧抽样间隔，默认 10 秒 |
| `video.whisper_model` | whisper 模型名称，默认 "base" |

配置示例：
```json
{
  "video": {
    "ffmpeg_path": "/usr/bin/ffmpeg",
    "whisper_path": "/usr/local/bin/whisper",
    "keyframe_interval": 10,
    "whisper_model": "base"
  }
}
```

仅配置 `ffmpeg_path` 时，系统只提取关键帧进行视觉检索；同时配置 `whisper_path` 后还会进行语音转录，支持基于语音内容的文本检索。

### 2.5 多模态与检索优化配置

| 配置项 | 说明 |
|--------|------|
| `embedding.use_multimodal` | 是否启用多模态 Embedding（图片向量化），默认 true |
| `vector.content_priority` | 检索结果排序：`image_text`（默认，优先含图片结果）或 `text_only`（优先纯文本） |
| `vector.text_match_enabled` | 启用 3 级文本匹配优化，默认 true。通过本地文本匹配和缓存复用减少 API 调用 |
| `vector.debug_mode` | 启用后查询响应中包含完整的检索诊断信息（意图、向量维度、各步骤耗时等） |

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

支持格式：PDF、Word、Excel、PPT、Markdown、视频（MP4、AVI、MKV、MOV、WebM）。上传后系统自动完成解析、去重检查、分块、向量化和存储。

视频文件上传后，系统会：
- 调用 ffmpeg 提取音频，再通过 whisper 进行语音转录，转录文本分块后嵌入向量库
- 调用 ffmpeg 按配置间隔抽取关键帧，通过多模态 Embedding 嵌入向量库
- 在 video_segments 表中记录每个分块对应的视频时间区间，检索时返回精确时间定位

视频功能需要在配置中设置 `video.ffmpeg_path`（必需）和 `video.whisper_path`（可选，不配置则跳过语音转录）。

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

`image_data` 为可选字段，支持传入 base64 格式的图片数据（如 `data:image/png;base64,...`）进行多模态查询。附带图片时，系统会：
- 跳过意图分类（图片可能包含产品信息）
- 同时进行文本向量检索和图片向量检索（图片使用较低阈值 × 0.6）
- 合并去重两路检索结果
- 使用视觉 LLM（GenerateWithImage）结合图片和知识库生成回答

`product_id` 为可选字段，指定后系统仅在该产品知识库和公共库中检索，不指定则在所有产品中检索。

### 4.2 回答流程

系统采用多级递进策略处理查询，在保证回答质量的同时尽量减少 API 调用成本。

#### 4.2.1 意图分类

- 附带图片的查询跳过意图分类，直接进入检索流程（图片可能包含产品信息）
- 纯文本查询先经过 LLM 意图分类：
  - `greeting`：问候语，返回产品介绍（自动翻译为用户语言）
  - `irrelevant`：无关问题，提示用户提问产品相关内容
  - `product`：产品相关问题，进入检索流程

#### 4.2.2 3 级文本匹配（仅纯文本查询）

启用 `vector.text_match_enabled` 后，系统在调用完整 RAG 之前先尝试低成本匹配：

1. **Level 1 — 本地文本匹配（零 API 开销）**：使用字符 bigram 相似度在分块缓存中搜索，命中且有缓存回答时直接返回
2. **Level 2 — 向量确认 + 缓存复用（仅 Embedding API）**：调用 Embedding API 生成向量进行搜索确认，命中且有缓存回答时返回
3. **Level 3 — 完整 RAG（Embedding + LLM）**：前两级未命中时进入完整流程

#### 4.2.3 向量检索

- **文本检索**：将问题向量化，在知识库中检索 Top-K 相关片段
- **图片检索**（附带图片时）：将图片通过多模态 Embedding 向量化，使用较低阈值（`threshold × 0.6`）进行检索，与文本检索结果合并去重
- **宽松检索**：若标准检索无结果，降低阈值重试（接受 score ≥ 0.3 的结果）

#### 4.2.4 结果后处理

1. **内容优先级排序**：根据 `vector.content_priority` 配置调整结果顺序（`image_text` 优先含图片结果，`text_only` 优先纯文本结果）
2. **视频时间定位**：查询 `video_segments` 表，为来自视频的分块补充 `start_time` / `end_time` 信息
3. **关联图片补充**：查找同一文档中的图片分块，附加到检索结果中

#### 4.2.5 回答生成

- 有检索结果时：构建上下文调用 LLM 生成回答。附带图片的查询使用视觉 LLM（`GenerateWithImage`）
- 无检索结果时：创建待处理问题，通知用户等待人工回复
- LLM 回答被判定为"无法回答"时：同样创建待处理问题

回答自动使用与用户提问相同的语言。

### 4.3 响应格式

```json
{
  "answer": "根据文档，安装步骤如下...",
  "sources": [
    {
      "document_name": "安装指南.pdf",
      "chunk_index": 3,
      "snippet": "第一步：下载安装包...",
      "image_url": "",
      "start_time": 0,
      "end_time": 0
    },
    {
      "document_name": "产品演示.mp4",
      "chunk_index": 5,
      "snippet": "[视频关键帧 30.0s]",
      "image_url": "data:image/jpeg;base64,...",
      "start_time": 30.0,
      "end_time": 30.0
    }
  ],
  "is_pending": false
}
```

当检索结果来源于视频文档时，`start_time` 和 `end_time` 字段包含视频中的时间位置（秒）。对于转录文本分块，这是一个时间区间；对于关键帧，两者相等。

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
├── askflow.db          # SQLite 数据库
├── uploads/             # 上传的原始文件
│   └── {doc_id}/        # 每个文档一个目录
│       └── filename.pdf
└── images/              # 知识条目图片
    └── {hash}.png
```

---

## 12. 数据备份与恢复

系统内置命令行备份工具，按数据类型分层备份，支持全量和增量两种模式。

### 12.1 备份文件命名

备份文件自动命名，包含关键信息便于管理：

```
askflow_<模式>_<主机名>_<日期-时间>.tar.gz
askflow_<模式>_<主机名>_<日期-时间>.manifest.json
```

示例：
- `askflow_full_myserver_20260212-143000.tar.gz` — 全量备份归档
- `askflow_full_myserver_20260212-143000.manifest.json` — 全量备份元数据
- `askflow_incremental_myserver_20260213-020000.tar.gz` — 增量备份归档

### 12.2 全量备份

将完整数据库快照、全部上传文件、配置和加密密钥打包为 tar.gz 归档。

```bash
# 备份到当前目录
askflow backup

# 备份到指定目录
askflow backup --output ./backups
```

归档内容：
- `askflow.db` — 完整数据库拷贝
- `uploads/` — 全部上传文件
- `config.json` — 系统配置
- `encryption.key` — AES 加密密钥
- `manifest.json` — 备份元数据（内嵌在归档中）

### 12.3 增量备份

基于上次备份的 manifest 文件，按数据级别导出变更内容，避免冗余。

```bash
askflow backup --output ./backups --incremental \
  --base ./backups/askflow_full_myserver_20260212-143000.manifest.json
```

增量备份策略：

| 数据类型 | 备份方式 | 说明 |
|----------|----------|------|
| 仅追加表（documents、chunks、video_segments、admin_users） | 按时间增量 | 只导出 `created_at` 晚于上次备份的新行 |
| 可变表（users、pending_questions、products、admin_user_products） | 全表导出 | 行可能被更新，需完整导出 |
| 临时表（sessions、email_tokens） | 跳过 | 会话和令牌为临时数据，无需备份 |
| 上传文件 | 目录级增量 | 只打包上次备份后新增的上传目录 |
| 配置和密钥 | 每次包含 | 体积小，始终包含 |

增量归档内容：
- `db_delta.sql` — SQL 增量语句（INSERT OR REPLACE）
- `uploads/` — 新增的上传文件
- `config.json` + `encryption.key` — 配置和密钥

### 12.4 恢复

#### 从全量备份恢复

```bash
# 恢复到默认 data 目录
askflow restore askflow_full_myserver_20260212-143000.tar.gz

# 恢复到指定目录
askflow restore --target ./data-new askflow_full_myserver_20260212-143000.tar.gz
```

全量恢复后即可直接启动服务。

#### 应用增量备份

增量恢复需先恢复全量备份，再依次应用增量备份：

```bash
# 1. 先恢复全量备份
askflow restore askflow_full_myserver_20260212-143000.tar.gz

# 2. 解压增量备份（上传文件和配置会自动覆盖）
askflow restore askflow_incremental_myserver_20260213-020000.tar.gz

# 3. 应用数据库增量 SQL
sqlite3 ./data/askflow.db < ./data/db_delta.sql
```

### 12.5 备份策略建议

- 每日执行一次全量备份
- 每小时或每次重要操作后执行增量备份
- 保留最近 7 天的全量备份和对应的增量链
- 将备份文件存储到异地或云存储
- 定期验证备份可恢复性（恢复到临时目录测试）
