# Helpdesk

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
- **多格式文档**：支持 PDF、Word、Excel、PPT、Markdown 上传与解析
- **URL 导入**：通过 URL 抓取网页内容入库
- **批量导入**：命令行递归扫描目录，批量导入文档
- **多模态检索**：支持文本和图片的向量化与检索
- **知识条目**：管理员可直接添加文本 + 图片知识条目
- **待处理问题**：无法回答的问题自动排队，管理员回答后自动入库
- **用户认证**：OAuth 2.0（Google / Apple / Amazon / Facebook）+ 邮箱密码注册
- **管理员体系**：超级管理员 + 子管理员（编辑角色），权限分级
- **配置热重载**：Web 界面修改 LLM / Embedding / SMTP 等配置，无需重启
- **加密存储**：API Key 使用 AES-256-GCM 加密存储在配置文件中
- **邮件服务**：SMTP 邮箱验证、测试邮件发送

---

## 技术架构

```
┌─────────────────────────────────────────────────────┐
│                   前端 SPA                           │
│              (frontend/dist)                         │
└──────────────────────┬──────────────────────────────┘
                       │ HTTP API
┌──────────────────────▼──────────────────────────────┐
│                   Go HTTP Server                     │
│  ┌─────────┐ ┌──────────┐ ┌────────┐ ┌───────────┐ │
│  │  Auth   │ │ Document │ │ Query  │ │  Config   │ │
│  │ (OAuth  │ │ Manager  │ │ Engine │ │  Manager  │ │
│  │ Session)│ │          │ │ (RAG)  │ │           │ │
│  └─────────┘ └────┬─────┘ └───┬────┘ └───────────┘ │
│                    │           │                      │
│  ┌─────────┐ ┌────▼─────┐ ┌──▼─────┐ ┌───────────┐ │
│  │ Parser  │ │ Chunker  │ │Embedding│ │   LLM     │ │
│  │(PDF/Word│ │(512/128) │ │ Service │ │  Service  │ │
│  │Excel/PPT│ │          │ │         │ │           │ │
│  └─────────┘ └──────────┘ └────┬────┘ └───────────┘ │
│                                │                      │
│              ┌─────────────────▼──────────────────┐  │
│              │     SQLite + Vector Store           │  │
│              │  (WAL 模式 + 内存缓存 + 余弦相似度)  │  │
│              └────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
                       │
          ┌────────────▼────────────┐
          │  OpenAI 兼容 API 端点    │
          │  (LLM + Embedding)      │
          └─────────────────────────┘
```

| 组件 | 技术选型 |
|------|----------|
| 后端 | Go 1.25+ |
| 数据库 | SQLite（WAL 模式，外键约束） |
| 向量存储 | SQLite 持久化 + 内存缓存，并发余弦相似度检索 |
| LLM | OpenAI 兼容 Chat Completion API（默认火山引擎 ARK） |
| Embedding | OpenAI 兼容 Embedding API（支持多模态） |
| 文档解析 | GoPDF2、GoWord、GoExcel、GoPPT |
| 前端 | SPA 单页应用（编译产物位于 frontend/dist） |
| 认证 | OAuth 2.0 + bcrypt + Session |
| 加密 | AES-256-GCM |
| 邮件 | SMTP（TLS） |

---

## 项目结构

```
helpdesk/
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
    ├── helpdesk.db              # SQLite 数据库
    ├── uploads/                 # 上传的原始文档（按文档 ID 分目录）
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
go build -o helpdesk.exe .
```

### 启动

```bash
./helpdesk
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
  -F "file=@./产品手册.pdf"

# 批量导入目录
./helpdesk import ./docs ./manuals
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
| `vector.db_path` | `./data/helpdesk.db` | SQLite 数据库路径 |
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
| `product_intro` | 产品介绍文本，用于意图分类上下文 |

### 环境变量

| 变量 | 说明 |
|------|------|
| `HELPDESK_ENCRYPTION_KEY` | AES-256 加密密钥（32 字节 hex）。未设置时自动生成并保存到 `data/encryption.key` |

---

## 命令行用法

```
helpdesk                          启动 HTTP 服务
helpdesk import <目录> [...]       批量导入文档到知识库
helpdesk help                     显示帮助信息
```

### 批量导入

递归扫描指定目录，将支持的文件解析后存入向量数据库。

```bash
helpdesk import ./docs
helpdesk import ./docs ./manuals /path/to/files
```

支持的文件扩展名：`.pdf` `.doc` `.docx` `.xls` `.xlsx` `.ppt` `.pptx` `.md` `.markdown`

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
| `POST` | `/api/query` | 提交问题，获取 RAG 回答 | 公开 |
| `GET` | `/api/product-intro` | 获取产品介绍 | 公开 |

### 文档管理

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `POST` | `/api/documents/upload` | 上传文件（multipart/form-data） | 管理员 |
| `POST` | `/api/documents/url` | 通过 URL 导入 | 管理员 |
| `GET` | `/api/documents` | 列出所有文档 | 管理员 |
| `DELETE` | `/api/documents/{id}` | 删除文档 | 管理员 |
| `GET` | `/api/documents/{id}/download` | 下载原始文件 | 管理员 |

### 待处理问题

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `GET` | `/api/pending?status=xxx` | 列出待处理问题 | 管理员 |
| `POST` | `/api/pending/answer` | 回答待处理问题 | 管理员 |
| `DELETE` | `/api/pending/{id}` | 删除待处理问题 | 管理员 |

### 知识条目

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `POST` | `/api/knowledge` | 添加知识条目 | 管理员 |
| `POST` | `/api/images/upload` | 上传图片 | 管理员 |
| `GET` | `/api/images/{filename}` | 获取图片 | 公开 |

### 管理员账户

| 方法 | 路径 | 说明 | 权限 |
|------|------|------|------|
| `GET` | `/api/admin/users` | 列出子管理员 | 超级管理员 |
| `POST` | `/api/admin/users` | 创建子管理员 | 超级管理员 |
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
用户提问
   │
   ▼
意图分类（LLM）
   │
   ├── greeting → 返回问候回复
   ├── irrelevant → 提示提问产品相关问题
   └── product ──▼
                  │
            问题向量化（Embedding API）
                  │
                  ▼
            向量相似度检索（Top-K, 余弦相似度 ≥ threshold）
                  │
           ┌──────┴──────┐
           │             │
        有结果         无结果
           │             │
           ▼             ▼
     构建上下文      创建待处理问题
     调用 LLM       通知用户等待
     生成回答
           │
           ▼
     返回回答 + 来源引用
```

### 文档处理流程

```
文件上传 / URL 导入 / 命令行批量导入
   │
   ▼
文档解析（PDF/Word/Excel/PPT/Markdown）
   │
   ▼
文本分块（512 字符，128 字符重叠）
   │
   ▼
向量化（Embedding API，支持多模态）
   │
   ▼
存储到 SQLite + 内存缓存
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
go build -o helpdesk .

# 启动（可配合 systemd 管理进程）
./helpdesk
```

### 远程部署（Windows → Linux）

项目提供 `build.cmd` 脚本，通过 PuTTY 工具链（plink/pscp）实现一键打包、上传、远程编译和重启：

```cmd
build.cmd
```

该脚本会：
1. 打包项目文件为 `deploy.tar.gz`
2. 通过 SCP 上传到远程服务器
3. 在远程服务器上解压并编译
4. 执行 `start.sh` 重启服务

### 数据备份

关键数据文件：
- `data/config.json` — 系统配置
- `data/helpdesk.db` — 数据库（文档记录、向量、用户、会话等）
- `data/encryption.key` — 加密密钥（丢失后无法解密已加密的 API Key）
- `data/uploads/` — 上传的原始文档
- `data/images/` — 知识条目图片

---

## 数据库表结构

| 表名 | 说明 |
|------|------|
| `documents` | 文档元数据（ID、名称、类型、状态、创建时间） |
| `chunks` | 文档分块（文本、向量、所属文档、图片 URL） |
| `pending_questions` | 待处理问题（问题、状态、回答、用户 ID） |
| `users` | 注册用户（邮箱、密码哈希、验证状态） |
| `sessions` | 用户会话（Session ID、用户 ID、过期时间） |
| `email_tokens` | 邮箱验证令牌 |
| `admin_users` | 子管理员账户（用户名、密码哈希、角色） |

---

## 相关文档

- [introduce.md](./introduce.md) — 产品介绍
- [manual.md](./manual.md) — 详细使用手册（含完整 API 示例）
