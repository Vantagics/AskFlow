# 问渠 AskFlow — 功能与技术概览

> 基于 RAG（检索增强生成）技术的智能客服知识库系统。Go 单二进制部署，SQLite 存储，开箱即用。

---

## 一、核心功能

### 1. 智能问答（RAG 管线）

- **3 级文本匹配**：Level 1 本地文本匹配（零 API 开销）→ Level 2 向量确认 + 缓存复用（仅 Embedding）→ Level 3 完整 RAG（Embedding + LLM），逐级递进节省 API 成本
- **意图分类**：自动识别问候、产品相关、无关问题，避免无效 LLM 调用
- **来源引用**：回答附带文档来源和视频时间定位
- **产品隔离检索**：用户提问时仅在所选产品知识库 + 公共库中检索

### 2. 多模态文档处理

- **支持格式**：PDF、Word、Excel、PPT、Markdown、HTML
- **视频处理**：MP4/AVI/MKV/MOV/WebM，自动提取音频转录（Whisper）+ 关键帧抽取（ffmpeg），支持时间定位检索
- **图片问答**：用户可粘贴图片提问，系统通过视觉 LLM 结合知识库生成回答
- **URL 导入**：通过 URL 抓取网页内容入库
- **批量导入**：命令行递归扫描目录，批量导入文档，支持指定目标产品
- **内容去重**：文档级 SHA-256 哈希去重 + 分块级向量复用

### 3. 多产品管理

- 每个产品拥有独立知识库，支持公共知识库跨产品共享
- 产品专属欢迎信息
- 管理员按产品分配管理权限
- 产品类型支持：知识库型、服务型

### 4. 知识条目管理

- 管理员可直接添加文本 + 图片 + 视频知识条目
- 按产品分类管理
- 多模态向量化（文本与图片均可被检索）

### 5. 待处理问题

- 无法回答的问题自动排队并标记所属产品
- 用户可主动标记"不太满意"将问题转交人工
- 管理员回答后自动向量化入库，供后续检索

### 6. 用户认证

- **OAuth 2.0**：Google、Apple、Amazon、Facebook 社交登录
- **邮箱密码注册**：含邮箱验证流程和数学验证码
- **管理员体系**：超级管理员 + 子管理员（编辑角色）
- **Session 管理**：24 小时过期，数据库持久化，自动清理

### 7. 客户管理

- 客户列表分页显示，支持按邮箱搜索
- 总客户数 / 已禁用数统计
- 手动验证邮箱、封禁 / 解封、删除客户
- 登录限流：每分钟 10 次尝试，连续 5 次失败自动锁定 1 小时

### 8. 系统配置

- Web 界面可视化配置 LLM、Embedding、向量检索、SMTP、OAuth 等参数
- API Key 使用 AES-256-GCM 加密存储
- 配置修改后热重载，无需重启服务

### 9. 数据备份与恢复

- 全量备份：数据库快照 + 上传文件 + 配置
- 增量备份：基于 manifest 仅备份新增数据
- 单命令恢复

### 10. 邮件服务

- SMTP（TLS）邮箱验证
- 管理员可测试 SMTP 配置

### 11. 前端功能

- 产品切换选择器
- 图片粘贴 / 拖拽上传
- 聊天历史记录与时间戳
- Markdown 渲染
- 照片墙画廊：多图回复以轮播方式展示，支持前后翻页和圆点导航，点击可全屏查看
- 媒体弹窗播放：视频/音频以紧凑播放按钮展示，点击弹窗播放，支持流式播放和进度跳转
- 引用来源播放按钮：参考资料中的视频/音频旁显示播放按钮，点击即可弹窗播放
- 流式视频播放：支持 HTTP Range 请求，视频可边下载边播放，媒体文件缓存 1 小时
- 文档拖拽上传
- 中英文双语界面（i18n）
- 响应式设计（画廊和媒体弹窗支持移动端自适应）
- 键盘快捷键：Escape 键关闭全屏图片和媒体弹窗

---

## 二、技术架构

```
用户浏览器 (SPA)
    │ HTTP API
    ▼
Go HTTP Server (单二进制)
    ├── Auth (OAuth 2.0 / Session / bcrypt / 登录限流)
    ├── Document Manager (上传 / 解析 / 分块 / 向量化)
    ├── Query Engine (意图分类 → 3级匹配 → LLM 生成)
    ├── Product Service (多产品隔离)
    ├── Pending Manager (待处理问题队列)
    ├── Config Manager (加密存储 / 热重载)
    ├── Email Service (SMTP / TLS)
    ├── Backup Service (全量 / 增量)
    └── Video Parser (ffmpeg + Whisper)
            │
            ▼
    SQLite (WAL) + 内存向量缓存 (SIMD 加速)
            │
            ▼
    OpenAI 兼容 API (LLM + Embedding)
```

### 技术选型

| 组件 | 技术 |
|------|------|
| 后端语言 | Go 1.25+ |
| 数据库 | SQLite（WAL 模式，外键约束） |
| 向量存储 | SQLite 持久化 + 内存缓存，SIMD 加速余弦相似度 |
| LLM | OpenAI 兼容 Chat Completion API（支持视觉模型） |
| Embedding | OpenAI 兼容 Embedding API（支持多模态：文本 + 图片） |
| 文档解析 | GoPDF2、GoWord、GoExcel、GoPPT（纯 Go 实现） |
| 视频处理 | ffmpeg（音频提取 + 关键帧）+ Whisper / RapidSpeech（语音转录） |
| 前端 | 原生 JavaScript SPA，无框架依赖 |
| 认证 | OAuth 2.0 + bcrypt + Session |
| 加密 | AES-256-GCM |
| 邮件 | SMTP（TLS） |

---

## 三、向量检索性能优化（20 项）

### 指令级（SIMD）

| 指令集 | 吞吐量 | 加速比 | 适用平台 |
|--------|--------|--------|----------|
| AVX-512 | 64 floats/iter | 8.8x | Intel Xeon, Zen 4+ |
| AVX2+FMA | 32 floats/iter | 8.6x | Haswell+, Zen+ |
| ARM NEON+FMLA | 16 floats/iter | ~4x | Apple Silicon, Graviton |
| SSE | 16 floats/iter | ~2x | 所有 x86-64 |
| Pure Go 8-way | 回退方案 | 1x | 通用 |

### 存储层

- 连续内存 Arena：消除指针跳转，提升 CPU 缓存命中率
- Float32 表示：内存占用减半
- Product 分区索引：缩小搜索范围
- Per-Worker Min-Heap Top-K：O(N log K) 复杂度
- LRU 查询缓存：256 条，FNV-1a 哈希

### 查询引擎层

- Embedding API 缓存：512 条，10 分钟 TTL
- 批量 SQL 查询：减少数据库往返

### 序列化层

- Float32 直接反序列化：零中间转换
- 快速路径反序列化：跳过格式检测

### 性能参考

- 10K chunks × 1536 dims：5.93ms (Pure Go) → 0.67ms (AVX-512)
- 启用产品分区（50% 过滤）：进一步减半
- LRU 缓存命中：0ms

---

## 四、安全机制

- API Key AES-256-GCM 加密存储
- 密码 bcrypt 哈希 + 复杂度要求
- Session 24 小时过期 + 自动清理
- OAuth state 防 CSRF
- 每 IP 请求限流（认证 10/min，通用 60/min）
- 连续失败自动锁定 + 手动封禁管理
- 文件上传扩展名白名单
- 参数化 SQL 查询防注入
- 安全响应头（CSP、X-Frame-Options 等）
- 文件路径遍历防护

---

## 五、部署方式

### 控制台模式

```bash
./askflow [--bind=<addr>] [--port=<port>] [--datadir=<path>]
```

### Windows 服务

```bash
askflow install [--bind=<addr>] [--port=<port>]
askflow start / stop / remove
```

### CLI 命令

```bash
askflow import [--product <id>] <dirs>    # 批量导入文档
askflow backup [--output <dir>] [--incremental]  # 数据备份
askflow restore <backup.tar.gz>           # 恢复数据
askflow products                          # 列出所有产品
```

### 部署特征

- 单二进制文件，无外部依赖（SQLite 内嵌）
- 数据目录：`./data/`（数据库、配置、上传文件）
- 启动时间 < 2 秒
- 内存占用：~100-200MB 基线 + 向量缓存
- 自适应并发 Worker（1-8 个，根据数据量调整）
- SQLite WAL 模式支持并发读 + 单写

---

## 六、API 接口（60+）

| 分类 | 数量 | 说明 |
|------|------|------|
| 认证 | 8 | 管理员登录、OAuth、用户注册/登录、邮箱验证 |
| 问答 | 2 | 提交问题、获取产品欢迎信息 |
| 文档管理 | 6 | 上传、URL 导入、列表、删除、下载、预览 |
| 产品管理 | 5 | CRUD、管理员产品列表 |
| 待处理问题 | 4 | 列表、创建、回答、删除 |
| 管理员管理 | 8 | 子管理员 CRUD、客户管理（分页/搜索/封禁） |
| 系统配置 | 2 | 获取/更新配置 |
| 知识条目 | 3 | 添加条目、上传图片/视频 |
| 系统 | 5 | 健康检查、验证码、邮件测试、视频依赖检查 |
| 媒体服务 | 3 | 图片/视频/媒体流服务 |

---

## 七、项目结构

```
askflow/
├── main.go                  # 入口：路由注册、HTTP 服务
├── app.go                   # API 门面：聚合所有服务
├── internal/
│   ├── auth/                # OAuth 2.0、Session、登录限流
│   ├── config/              # 配置加载/保存/加密/热重载
│   ├── db/                  # SQLite 初始化、建表、迁移
│   ├── document/            # 文档上传/解析/分块/向量化
│   ├── parser/              # 多格式解析（PDF/Word/Excel/PPT/MD）
│   ├── chunker/             # 文本分块（512 字符 + 128 重叠）
│   ├── embedding/           # Embedding API 客户端（文本/图片/批量）
│   ├── llm/                 # LLM Chat Completion 客户端
│   ├── vectorstore/         # 向量存储与 SIMD 加速检索
│   ├── query/               # RAG 查询引擎
│   ├── pending/             # 待处理问题管理
│   ├── product/             # 产品管理
│   ├── video/               # 视频解析（ffmpeg + Whisper）
│   ├── email/               # SMTP 邮件服务
│   ├── backup/              # 数据备份与恢复
│   ├── captcha/             # 数学验证码
│   ├── service/             # 服务编排层
│   └── svc/                 # Windows/Linux 服务支持
├── sqlite-vec/              # 独立 SIMD 向量库模块（可跨项目复用）
└── frontend/dist/           # 前端 SPA 编译产物
```
