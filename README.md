# 问渠 AskFlow

> 问渠那得清如许？为有源头活水来。 —— 朱熹《观书有感》

🇨🇳 中文 | [🇬🇧 English](./README_EN.md)

基于 RAG（检索增强生成）技术的智能客服知识库系统。上传产品文档，用户提问时自动检索相关内容并通过大语言模型生成准确回答。

Go 单二进制部署，SQLite 存储，开箱即用。

---

## 目录

- [功能特性](#功能特性)
- [技术架构](#技术架构)
- [项目结构](#项目结构)
- [快速开始](#快速开始)
- [配置说明](#配置说明)
- [命令行用法](#命令行用法)
- [API 参考](#api-参考)
- [RAG 工作流程](#rag-工作流程)
- [安全机制](#安全机制)
- [部署](#部署)
- [相关文档](#相关文档)

---

## 功能特性

- **智能问答**：意图分类 → 向量检索 → LLM 生成回答，附带来源引用
- **多模态检索**：支持文本、图片和视频内容的向量化与跨模态检索
- **视频检索**：上传视频后自动提取音频转录和关键帧，支持语义检索并返回精确时间定位
- **图片问答**：用户可粘贴图片提问，系统通过视觉 LLM 结合知识库生成回答
- **多产品支持**：管理多个产品线，每个产品拥有独立知识库，支持公共知识库跨产品共享
- **多格式文档**：支持 PDF、Word、Excel、PPT、Markdown、视频（MP4/AVI/MKV/MOV/WebM）上传与解析
- **URL 导入**：通过 URL 抓取网页内容入库
- **批量导入**：命令行递归扫描目录，批量导入文档，支持指定目标产品
- **知识条目**：管理员可直接添加文本 + 图片知识条目，按产品分类
- **产品隔离检索**：用户提问时仅在所选产品知识库和公共库中检索，确保回答准确性
- **内容去重**：文档级 SHA-256 哈希去重 + 分块级向量复用，避免重复导入和冗余 API 调用
- **3 级文本匹配**：Level 1 文本匹配（零 API 开销）→ Level 2 向量确认 + 缓存复用（仅 Embedding）→ Level 3 完整 RAG（Embedding + LLM），逐级递进节省 API 成本
- **待处理问题**：无法回答的问题自动排队并标记所属产品，管理员回答后自动入库
- **用户认证**：OAuth 2.0（Google / Apple / Amazon / Facebook） + 邮箱密码注册
- **管理员体系**：超级管理员 + 子管理员（编辑角色），支持按产品分配管理权限
- **产品专属欢迎信息**：每个产品可设置独立的欢迎信息，用户进入时展示对应介绍
- **配置热重载**：Web 界面修改 LLM / Embedding / SMTP 等配置，无需重启
- **加密存储**：API Key 使用 AES-256-GCM 加密存储在配置文件中
- **邮件服务**：SMTP 邮箱验证、测试邮件发送
- **照片墙画廊**：多图回复以轮播方式展示，支持前后翻页、圆点导航，点击可全屏查看
- **媒体弹窗播放**：视频/音频以紧凑播放按钮展示，点击弹窗播放，支持时间段跳转
- **流式视频播放**：支持 HTTP Range 请求，视频可边下载边播放，媒体文件自动缓存
- **引用来源播放**：参考资料中的视频/音频旁显示播放按钮，点击即可弹窗播放
- **移动端适配**：画廊和媒体弹窗在小屏设备上自适应布局

---

## 技术架构

```
┌─────────────────────────────────────────────────────┐
│                  前端 SPA                           │
│             (frontend/dist)                         │
└──────────────────────┬──────────────────────────────┘
                       │ HTTP API
┌──────────────────────▼──────────────────────────────┐
│                  Go HTTP Server                     │
│ ┌─────────┐ ┌──────────┐ ┌────────┐ ┌───────────┐ │
│ │ Auth   │ │ Document │ │ Query  │ │ Config   │ │
│ │(OAuth  │ │ Manager  │ │ Engine │ │ Manager  │ │
│ │Session)│ │         │ │(RAG)  │ │          │ │
│ └─────────┘ └────┬─────┘ └───┬────┘ └───────────┘ │
│ ┌─────────┐     │          │     ┌───────────┐  │
│ │ Product │     │          │     │ Pending   │ │
│ │ Service │     │          │     │ Manager   │ │
│ └─────────┘     │          │     └───────────┘  │
│ ┌─────────┐ ┌────▼─────┐ ┌──▼─────┐ ┌───────────┐ │
│ │ Parser  │ │ Chunker  │ │Embedding│ │  LLM     │ │
│ │PDF/Word│ │(512/128) │ │ Service │ │ Service  │ │
│ │Excel/PPT│ │         │ │(文本+图片)│ │(含视觉模型)│ │
│ └─────────┘ └──────────┘ └────┬────┘ └───────────┘ │
│ ┌─────────┐                  │                     │
│ │ Video   │                  │                     │
│ │ Parser  │                  │                     │
│ │(ffmpeg+ │                  │                     │
│ │whisper) │                  │                     │
│ └─────────┘                  │                     │
│             ┌─────────────────▼──────────────────┐  │
│             │    SQLite + Vector Store           │  │
│             │ (WAL 模式 + 内存缓存 + 余弦相似度) │  │
│             └────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
                       │
          ┌────────────▼────────────┐
          │ OpenAI 兼容 API 端点    │
          │ (LLM + Embedding)      │
          └─────────────────────────┘
```

| 组件 | 技术选型 |
|------|----------|
| 后端 | Go 1.25+ |
| 数据库 | SQLite（WAL 模式，外键约束） |
| 向量存储 | SQLite 持久化 + 内存缓存，并发余弦相似度检索 |
| LLM | OpenAI 兼容 Chat Completion API（支持视觉模型） |
| Embedding | OpenAI 兼容 Embedding API（支持多模态：文本 + 图片） |
| 文档解析 | GoPDF2、GoWord、GoExcel、GoPPT |
| 视频处理 | ffmpeg（音频提取 + 关键帧抽取）+ whisper（语音转录） |
| 前端 | SPA 单页应用（照片墙画廊、媒体弹窗播放、流式视频，编译产物位于 frontend/dist） |
| 认证 | OAuth 2.0 + bcrypt + Session |
| 加密 | AES-256-GCM |
| 邮件 | SMTP（TLS） |

---

## 项目结构

```
askflow/
├── main.go                      # 入口：初始化、路由注册、HTTP 服务启动
├── app.go                       # API 门面：聚合所有服务组件
├── go.mod / go.sum              # Go 模块依赖
├── build_local.sh               # 本地构建脚本（Linux/macOS）
├── build.cmd                    # 远程部署脚本（Windows → Linux 服务器）
│
├── internal/
│   ├── auth/
│   │   ├── oauth.go             # OAuth 2.0 多提供商认证
│   │   └── session.go           # Session 管理（创建/验证/清理）
│   ├── config/
│   │   └── config.go            # 配置加载/保存/加密/热重载
│   ├── db/
│   │   └── db.go                # SQLite 初始化、建表、迁移
│   ├── document/
│   │   └── manager.go           # 文档上传/解析/分块/向量化/存储
│   ├── parser/
│   │   └── parser.go            # 多格式文档解析（PDF/Word/Excel/PPT/MD）
│   ├── chunker/
│   │   └── chunker.go           # 文本分块（固定大小 + 重叠）
│   ├── embedding/
│   │   └── service.go           # Embedding API 客户端（文本/图片/批量）
│   ├── llm/
│   │   └── service.go           # LLM Chat Completion API 客户端
│   ├── vectorstore/
│   │   └── store.go             # 向量存储与相似度检索（内存缓存）
│   ├── query/
│   │   └── engine.go            # RAG 查询引擎（意图分类→检索→生成）
│   ├── pending/
│   │   └── manager.go           # 待处理问题管理
│   ├── product/
│   │   └── service.go           # 产品管理（CRUD、管理员产品分配）
│   ├── backup/
│   │   └── backup.go            # 数据备份与恢复（全量/增量）
│   ├── video/
│   │   └── parser.go            # 视频解析（ffmpeg 关键帧 + whisper 语音转录）
│   └── email/
│       └── service.go           # SMTP 邮件发送（验证/测试）
│
├── frontend/
│   └── dist/                    # 前端编译产物（SPA）
│       ├── index.html
│       ├── app.js
│       └── styles.css
│
└── data/
    ├── config.json              # 系统配置（API Key 加密存储）
    ├── encryption.key           # AES-256 加密密钥
    ├── askflow.db              # SQLite 数据库
    ├── uploads/                 # 上传的原始文档（按文件 ID 分目录）
    └── images/                  # 知识条目图片
```

---

## 快速开始

### 环境要求

- Go 1.25+（仅构建时需要）
- 可访问的 LLM 和 Embedding API 端点（OpenAI 兼容）

### 构建

```bash
# Linux / macOS
chmod +x build_local.sh
./build_local.sh

# Windows
go build -o askflow.exe .
```

### 启动

```bash
./askflow
```

服务启动后监听 `0.0.0.0:8080`，浏览器访问 `http://localhost:8080`。

### 初始化管理员

首次访问时，通过前端界面或 API 设置超级管理员：

```bash
curl -X POST http://localhost:8080/api/admin/setup \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "your_password"}'
```

### 导入文档

```bash
# 上传单个文件（通过 API）
curl -X POST http://localhost:8080/api/documents/upload \
  -F "file=@./产品手册.pdf" \
  -F "product_id=<product_id>"

# 批量导入目录
./askflow import ./docs

# 批量导入到指定产品
./askflow import --product <product_id> ./docs ./manuals
```

### 提问

```bash
curl -X POST http://localhost:8080/api/query \
  -H "Content-Type: application/json" \
  -d '{"question": "如何安装产品？"}'
```

---

## 配置说明

配置文件位于 `data/config.json`，支持通过 Web 管理界面或 API 修改。

### 服务器

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `server.port` | `8080` | HTTP 监听端口 |

### LLM

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `llm.endpoint` | 火山引擎 ARK | OpenAI 兼容 API 地址 |
| `llm.api_key` | — | API 密钥（自动 AES 加密存储） |
| `llm.model_name` | — | 模型名称 / Endpoint ID |
| `llm.temperature` | `0.3` | 生成温度（0-1） |
| `llm.max_tokens` | `2048` | 最大生成 token 数 |

### Embedding

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `embedding.endpoint` | 火山引擎 ARK | OpenAI 兼容 API 地址 |
| `embedding.api_key` | — | API 密钥（自动 AES 加密存储） |
| `embedding.model_name` | — | 模型名称 / Endpoint ID |
| `embedding.use_multimodal` | `true` | 启用图片向量化 |

### 向量检索

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `vector.db_path` | `./data/askflow.db` | SQLite 数据库路径 |
| `vector.chunk_size` | `512` | 文本分块大小（字符数） |
| `vector.overlap` | `128` | 相邻分块重叠字符数 |
| `vector.top_k` | `5` | 检索返回的最相关片段数 |
| `vector.threshold` | `0.5` | 余弦相似度阈值（0-1） |

### SMTP 邮件

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `smtp.host` | — | SMTP 服务器地址 |
| `smtp.port` | `587` | SMTP 端口 |
| `smtp.username` | — | SMTP 用户名 |
| `smtp.password` | — | SMTP 密码 |
| `smtp.from_addr` | — | 发件人邮箱 |
| `smtp.from_name` | — | 发件人名称 |
| `smtp.use_tls` | `true` | 启用 TLS |

### OAuth

在 `oauth.providers` 下按提供商名称配置：

```json
{
  "oauth": {
    "providers": {
      "google": {
        "client_id": "xxx",
        "client_secret": "xxx",
        "auth_url": "https://accounts.google.com/o/oauth2/auth",
        "token_url": "https://oauth2.googleapis.com/token",
        "redirect_url": "https://your-domain.com/oauth/callback",
        "scopes": ["openid", "email", "profile"]
      }
    }
  }
}
```

支持的提供商：`google`、`apple`、`amazon`、`facebook`。

### 其他

| 字段 | 说明 |
|------|------|
| `admin.username` | 超级管理员用户名（初始化时设置） |
| `admin.password_hash` | 超级管理员密码哈希（bcrypt） |
| `admin.login_route` | 管理员登录路由，默认 `/admin` |
| `product_intro` | 全局产品介绍文本，用于意图分类上下文。各产品可在产品管理中设置独立的 `welcome_message`，优先级高于此全局配置 |

### 视频处理

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `video.ffmpeg_path` | — | ffmpeg 可执行文件路径，为空则不支持视频 |
| `video.whisper_path` | — | whisper CLI 可执行文件路径，为空则跳过语音转录 |
| `video.keyframe_interval` | `10` | 关键帧抽样间隔（秒） |
| `video.whisper_model` | `base` | whisper 模型名称 |

视频功能需要外部工具支持。仅配置 `ffmpeg_path` 时只提取关键帧；同时配置 `whisper_path` 后还会进行语音转录。

### 向量检索高级选项

| 字段 | 默认值 | 说明 |
|------|--------|------|
| `vector.content_priority` | `image_text` | 检索结果排序优先级：`image_text` 优先展示含图片的结果，`text_only` 优先展示纯文本结果 |
| `vector.text_match_enabled` | `true` | 启用 3 级文本匹配，通过本地文本匹配和缓存复用减少 API 调用 |
| `vector.debug_mode` | `false` | 启用后查询响应中包含检索诊断信息 |

### 环境变量

| 变量 | 说明 |
|------|------|
| `ASKFLOW_ENCRYPTION_KEY` | AES-256 加密密钥（32 字节 hex）。未设置时自动生成并保存到 `data/encryption.key` |

---

## 命令行用法

```
askflow                                              启动 HTTP 服务
askflow import [--product <product_id>] <目录> [...]  批量导入文档到知识库
askflow backup [选项]                                 备份整站数据
askflow restore <备份文件>                             从备份恢复数据
askflow help                                         显示帮助信息
```

### 批量导入

递归扫描指定目录，将支持的文件解析后存入向量数据库。

```bash
askflow import ./docs
askflow import ./docs ./manuals /path/to/files

# 指定目标产品（文档将关联到该产品）
askflow import --product <product_id> ./docs
```

不指定 `--product` 时，文档将导入到公共库。若指定的产品 ID 不存在，系统将报错并中止导入。

支持的文件扩展名：`.pdf` `.doc` `.docx` `.xls` `.xlsx` `.ppt` `.pptx` `.md` `.markdown` `.mp4` `.avi` `.mkv` `.mov` `.webm`

### 数据备份与恢复

系统提供按数据类型分层的备份机制，支持全量和增量两种模式。

备份文件命名格式：`askflow_<模式>_<主机名>_<日期-时间>.tar.gz`，例如 `askflow_full_myserver_20260212-143000.tar.gz`。

#### 全量备份

将完整数据库快照、全部上传文件、配置和加密密钥打包为 tar.gz 归档。

```bash
# 备份到当前目录
askflow backup

# 备份到指定目录
askflow backup --output ./backups
```

#### 增量备份

基于上次备份的 manifest 文件，仅导出新增的数据库行和新上传的文件。可变数据表（用户、待处理问题、产品等）会全量导出以确保更新不丢失。

```bash
askflow backup --incremental --base ./backups/askflow_full_myserver_20260212-143000.manifest.json
```

增量备份按数据级别工作，而非文件级别：
- 仅追加表（documents、chunks 等）：只导出 `created_at` 晚于上次备份的新行
- 可变表（users、pending_questions、products 等）：全表导出（行可能被更新）
- 临时表（sessions、email_tokens）：跳过（无需备份）
- 上传文件：只打包新增的目录

#### 恢复

```bash
# 从全量备份恢复
askflow restore askflow_full_myserver_20260212-143000.tar.gz

# 恢复到指定目录
askflow restore --target ./data-new backup.tar.gz
```

增量恢复流程：先恢复全量备份，再依次应用增量备份中的 `db_delta.sql`。

```bash
askflow restore full-backup.tar.gz
askflow restore incremental-backup.tar.gz
sqlite3 ./data/askflow.db < ./data/db_delta.sql
```

---

## API 参考

所有 API 返回 JSON 格式。需要认证的接口通过 `Authorization: Bearer <session_token>` 鉴权。

### 认证

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `POST` | `/api/admin/setup` | 初始化超级管理员 | 公开（仅首次） |
| `POST` | `/api/admin/login` | 管理员登录 | 公开 |
| `GET` | `/api/admin/status` | 查询管理员是否已配置 | 公开 |
| `GET` | `/api/oauth/url?provider=xxx` | 获取 OAuth 授权 URL | 公开 |
| `POST` | `/api/oauth/callback` | OAuth 回调处理 | 公开 |
| `POST` | `/api/auth/register` | 邮箱注册（需验证码） | 公开 |
| `POST` | `/api/auth/login` | 邮箱登录（需验证码） | 公开 |
| `GET` | `/api/auth/verify?token=xxx` | 邮箱验证 | 公开 |
| `GET` | `/api/captcha` | 获取数学验证码 | 公开 |

### 智能问答

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `POST` | `/api/query` | 提交问题，获取 RAG 回答（支持 `product_id` 参数限定检索范围） | 公开 |
| `GET` | `/api/product-intro` | 获取产品介绍（支持 `product_id` 参数获取指定产品欢迎信息） | 公开 |

### 产品管理

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `GET` | `/api/products` | 获取所有产品列表 | 管理员 |
| `POST` | `/api/products` | 创建产品 | 超级管理员 |
| `PUT` | `/api/products/{id}` | 更新产品信息 | 超级管理员 |
| `DELETE` | `/api/products/{id}` | 删除产品 | 超级管理员 |
| `GET` | `/api/products/my` | 获取当前管理员被分配的产品列表 | 管理员 |

### 文档管理

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `POST` | `/api/documents/upload` | 上传文件（multipart/form-data，支持 `product_id` 字段） | 管理员 |
| `POST` | `/api/documents/url` | 通过 URL 导入（支持 `product_id` 参数） | 管理员 |
| `GET` | `/api/documents` | 列出文档（支持 `product_id` 参数筛选） | 管理员 |
| `DELETE` | `/api/documents/{id}` | 删除文档 | 管理员 |
| `GET` | `/api/documents/{id}/download` | 下载原始文件 | 管理员 |

### 待处理问题

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `GET` | `/api/pending?status=xxx` | 列出待处理问题（支持 `product_id` 参数筛选） | 管理员 |
| `POST` | `/api/pending/answer` | 回答待处理问题 | 管理员 |
| `DELETE` | `/api/pending/{id}` | 删除待处理问题 | 管理员 |

### 知识条目

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `POST` | `/api/knowledge` | 添加知识条目（支持 `product_id` 参数） | 管理员 |
| `POST` | `/api/images/upload` | 上传图片 | 管理员 |
| `GET` | `/api/images/{filename}` | 获取图片 | 公开 |

### 管理员账户

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `GET` | `/api/admin/users` | 列出子管理员 | 超级管理员 |
| `POST` | `/api/admin/users` | 创建子管理员（支持 `product_ids` 参数分配产品） | 超级管理员 |
| `DELETE` | `/api/admin/users/{id}` | 删除子管理员 | 超级管理员 |
| `GET` | `/api/admin/role` | 查询当前角色 | 管理员 |

### 系统配置

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `GET` | `/api/config` | 获取配置（API Key 脱敏） | 管理员 |
| `PUT` | `/api/config` | 更新配置（热重载） | 超级管理员 |

### 邮件

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `POST` | `/api/email/test` | 发送测试邮件 | 管理员 |

---

## RAG 工作流程

```
用户提问（文本 / 文本+图片）
   │
   ▼
意图分类（LLM）── 附带图片时跳过，直接进入检索
   │
   ├── greeting → 返回问候回复
   ├── irrelevant → 提示提问产品相关问题
   └── product ──┐
                  │
            ┌─────┴─────┐
            │          │
         文本查询    图片查询（如有）
            │          │
            ▼          ▼
      问题向量化   图片向量化
      (Embedding)  (多模态 Embedding)
            │          │
            ▼          ▼
      向量检索      向量检索（低阈值）
      (Top-K)       (阈值 × 0.6)
            │          │
            └─────┬─────┘
                  │
            合并去重结果
                  │
            按 content_priority 排序
                  │
            补充视频时间定位信息
                  │
           ┌──────┴──────┐
           │            │
        有结果        无结果
           │            │
           ▼            ▼
     构建上下文     创建待处理问题
     调用 LLM       通知用户等待
     (视觉 LLM
      如有图片)
           │
           ▼
     返回回答 + 来源引用 + 视频时间戳
```

### 3 级文本匹配（API 成本优化）

```
用户提问
   │
   ▼
Level 1: 本地文本匹配（零 API 开销）
   │ 使用字符 bigram 相似度在分块缓存中搜索
   │
   ├── 命中 + 有缓存回答 → 直接返回（零成本）
   └── 命中但无缓存 ──┐
                       │
Level 2: 向量确认 + 缓存复用（仅 Embedding API）
   │ 调用 Embedding API 生成向量，搜索确认
   │
   ├── 确认 + 有缓存回答 → 返回（仅 Embedding 成本）
   └── 无缓存回答 ──┐
                     │
Level 3: 完整 RAG（Embedding + LLM）
   │ 向量检索 → 构建上下文 → 调用 LLM 生成回答
   └── 返回回答（完整成本）
```

### 文档处理流程

```
文件上传 / URL 导入 / 命令行批量导入
   │
   ▼
文件类型识别
   │
   ├── 文档（PDF/Word/Excel/PPT/Markdown/HTML）
   │    │
   │    ▼
   │  文档解析（提取文本 + 图片）
   │    │
   │    ▼
   │  内容去重检查（SHA-256 哈希）
   │    │
   │    ├── 重复 → 拒绝导入
   │    └── 不重复 ──┐
   │                  │
   │            文本分块 + 分块级去重
   │                  │
   │                  ▼
   │            文本嵌入 + 图片多模态嵌入
   │                  │
   │                  ▼
   │            存储到 SQLite + 内存缓存
   │
   └── 视频（MP4/AVI/MKV/MOV/WebM）
         │
         ├── ffmpeg 提取音频 → whisper 语音转录
         │    │
         │    ▼
         │  转录文本分块 → 嵌入 → 存储
         │  创建 video_segments 记录（含时间区间）
         │
         └── ffmpeg 按间隔抽取关键帧
               │
               ▼
             关键帧 → 多模态嵌入 → 存储
             创建 video_segments 记录（含时间戳）
```

---

## 安全机制

- **API Key 加密**：配置文件中的 API Key 使用 AES-256-GCM 加密，密文以 `enc:` 前缀标识
- **密码哈希**：管理员和用户密码使用 bcrypt 哈希存储
- **Session 管理**：24 小时过期，存储在数据库中，支持验证和清理
- **验证码**：邮箱注册和登录需通过数学验证码
- **邮箱验证**：注册用户需通过邮件链接验证邮箱
- **权限分级**：超级管理员 / 编辑管理员 / 普通用户，API 按角色鉴权
- **文件类型校验**：上传文件和图片均进行扩展名白名单校验
- **SQLite WAL 模式**：支持并发读取，外键约束保证数据完整性

---

## 部署

### 单机部署

```bash
# 构建
go build -o askflow .

# 启动（可配合 systemd 管理进程）
./askflow
```

### 远程部署（Windows → Linux）

项目提供 `build.cmd` 脚本，通过 PuTTY 工具链（plink/pscp）实现一键打包、上传、远程编译和重启。

```cmd
build.cmd
```

该脚本会：
1. 打包项目文件为 `deploy.tar.gz`
2. 通过 SCP 上传到远程服务器
3. 在远程服务器上解压并编译
4. 执行 `start.sh` 重启服务

### 数据备份

系统内置命令行备份工具，支持全量和增量备份。详见[命令行用法](#命令行用法)中的"数据备份与恢复"章节。

关键数据文件：
- `data/config.json` — 系统配置
- `data/askflow.db` — 数据库（文档记录、向量、用户、会话等）
- `data/encryption.key` — 加密密钥（丢失后无法解密已加密的 API Key）
- `data/uploads/` — 上传的原始文件
- `data/images/` — 知识条目图片

备份示例：

```bash
# 全量备份
askflow backup --output ./backups

# 增量备份（基于上次全量）
askflow backup --output ./backups --incremental --base ./backups/askflow_full_myserver_20260212-143000.manifest.json

# 恢复
askflow restore ./backups/askflow_full_myserver_20260212-143000.tar.gz
```

---

## 数据库表结构

| 表名 | 说明 |
|------|------|
| `products` | 产品信息（ID、名称、描述、欢迎信息、创建/更新时间） |
| `admin_user_products` | 管理员-产品关联表（admin_user_id、product_id，联合主键） |
| `documents` | 文档元数据（ID、名称、类型、状态、内容哈希、product_id、创建时间）。类型包含 pdf/word/excel/ppt/markdown/html/video/url |
| `chunks` | 文档分块（文本、向量、所属文档、图片 URL、product_id）。视频关键帧的 image_url 存储 base64 数据 |
| `video_segments` | 视频片段时间轴（document_id、segment_type、start_time、end_time、content、chunk_id）。segment_type 为 "transcript" 或 "keyframe" |
| `pending_questions` | 待处理问题（问题、状态、回答、用户 ID、图片数据、product_id） |
| `users` | 注册用户（邮箱、密码哈希、验证状态） |
| `sessions` | 用户会话（Session ID、用户 ID、过期时间） |
| `email_tokens` | 邮箱验证令牌 |
| `admin_users` | 子管理员账户（用户名、密码哈希、角色） |

`product_id` 为空字符串或 NULL 表示该记录属于公共库（Public Library），所有产品检索时均可访问。

---

## 相关文档

- [introduce.md](./introduce.md) — 产品介绍
- [manual.md](./manual.md) — 详细使用手册（含完整 API 示例）
