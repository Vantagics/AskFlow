# AskFlow 问渠

> Ask, and clarity flows.

[🇨🇳 中文](./README.md) | 🇬🇧 English

An AI-powered knowledge base system built on RAG (Retrieval-Augmented Generation). Upload product documents, and the system automatically retrieves relevant content and generates accurate answers via LLM when users ask questions.

Single Go binary deployment, SQLite storage, ready out of the box.

---

## Table of Contents

- [Features](#features)
- [Architecture](#architecture)
- [Project Structure](#project-structure)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [CLI Usage](#cli-usage)
- [API Reference](#api-reference)
- [RAG Workflow](#rag-workflow)
- [Security](#security)
- [Deployment](#deployment)
- [Related Documentation](#related-documentation)

---

## Features

- **Smart Q&A**: Intent classification �?vector retrieval �?LLM answer generation with source citations
- **Multimodal Retrieval**: Vectorize and search text, images, and video content with cross-modal matching
- **Video Search**: Upload videos for automatic audio transcription and keyframe extraction, with precise timestamp localization in search results
- **Image Q&A**: Users can paste images with questions; the system uses vision LLM combined with the knowledge base to generate answers
- **Multi-Product Support**: Manage multiple product lines, each with its own knowledge base, plus a shared Public Library accessible across all products
- **Multi-format Documents**: Upload and parse PDF, Word, Excel, PPT, Markdown, and video files (MP4/AVI/MKV/MOV/WebM)
- **URL Import**: Fetch and index web page content via URL
- **Batch Import**: CLI recursive directory scan for bulk document import, with optional product targeting
- **Knowledge Entries**: Admins can directly add text + image knowledge entries, categorized by product
- **Product-Scoped Search**: User queries search only within the selected product's knowledge base and the Public Library, ensuring accurate answers
- **Content Deduplication**: Document-level SHA-256 hash dedup + chunk-level embedding reuse to prevent duplicate imports and redundant API calls
- **3-Level Text Matching**: Level 1 text matching (zero API cost) �?Level 2 vector confirmation + cache reuse (Embedding only) �?Level 3 full RAG (Embedding + LLM), progressively escalating to save API costs
- **Pending Questions**: Unanswered questions are automatically queued with product association; admin answers are auto-indexed
- **User Authentication**: OAuth 2.0 (Google / Apple / Amazon / Facebook) + email/password registration
- **Admin Hierarchy**: Super admin + sub-admins (editor role) with per-product permission assignment
- **Per-Product Welcome Messages**: Each product can have its own welcome message displayed to users
- **Hot Reload**: Modify LLM / Embedding / SMTP settings via web UI without restart
- **Encrypted Storage**: API keys stored with AES-256-GCM encryption in config file
- **Email Service**: SMTP email verification and test email sending

---

## Architecture

```
┌─────────────────────────────────────────────────────�?
�?                  Frontend SPA                       �?
�?             (frontend/dist)                         �?
└──────────────────────┬──────────────────────────────�?
                       �?HTTP API
┌──────────────────────▼──────────────────────────────�?
�?                  Go HTTP Server                     �?
�? ┌─────────�?┌──────────�?┌────────�?┌───────────�?�?
�? �? Auth   �?�?Document �?�?Query  �?�? Config   �?�?
�? �?(OAuth  �?�?Manager  �?�?Engine �?�? Manager  �?�?
�? �?Session)�?�?         �?�?(RAG)  �?�?          �?�?
�? └─────────�?└────┬─────�?└───┬────�?└───────────�?�?
�? ┌─────────�?     �?          �?     ┌───────────�? �?
�? �?Product �?     �?          �?     �? Pending   �?�?
�? �?Service �?     �?          �?     �? Manager   �?�?
�? └─────────�?     �?          �?     └───────────�? �?
�? ┌─────────�?┌────▼─────�?┌──▼─────�?┌───────────�?�?
�? �?Parser  �?�?Chunker  �?│Embedding�?�?  LLM     �?�?
�? �?PDF/Word�?�?512/128) �?�?Service �?�? Service  �?�?
�? ┌─────────�?┌────▼─────�?┌──▼─────�?┌───────────�?�?
�? �?Parser  �?�?Chunker  �?│Embedding�?�?  LLM     �?�?
�? �?PDF/Word�?�?512/128) �?�?Service �?�? Service  �?�?
�? │Excel/PPT�?�?         �?�?txt+img)�?�?(vision)  �?�?
�? └─────────�?└──────────�?└────┬────�?└───────────�?�?
�? ┌─────────�?                  �?                     �?
�? �?Video   �?                  �?                     �?
�? �?Parser  �?                  �?                     �?
�? �?ffmpeg+ �?                  �?                     �?
�? │whisper) �?                  �?                     �?
�? └─────────�?                  �?                     �?
�?             ┌─────────────────▼──────────────────�? �?
�?             �?    SQLite + Vector Store           �? �?
�?             �? (WAL mode + in-memory cache +      �? �?
�?             �?  cosine similarity search)          �? �?
�?             └────────────────────────────────────�? �?
└─────────────────────────────────────────────────────�?
                       �?
          ┌────────────▼────────────�?
          �? OpenAI-compatible API   �?
          �? (LLM + Embedding)       �?
          └─────────────────────────�?
```

| Component | Technology |
|-----------|------------|
| Backend | Go 1.25+ |
| Database | SQLite (WAL mode, foreign keys) |
| Vector Store | SQLite persistence + in-memory cache, concurrent cosine similarity search |
| LLM | OpenAI-compatible Chat Completion API (supports vision models) |
| Embedding | OpenAI-compatible Embedding API (multimodal: text + image) |
| Document Parsing | GoPDF2, GoWord, GoExcel, GoPPT |
| Video Processing | ffmpeg (audio extraction + keyframe sampling) + whisper (speech transcription) |
| Frontend | SPA (compiled assets in frontend/dist) |
| Authentication | OAuth 2.0 + bcrypt + Session |
| Encryption | AES-256-GCM |
| Email | SMTP (TLS) |

---

## Project Structure

```
askflow/
├── main.go                      # Entry point: init, routing, HTTP server
├── app.go                       # API facade: aggregates all service components
├── go.mod / go.sum              # Go module dependencies
├── build_local.sh               # Local build script (Linux/macOS)
├── build.cmd                    # Remote deploy script (Windows �?Linux server)
�?
├── internal/
�?  ├── auth/
�?  �?  ├── oauth.go             # OAuth 2.0 multi-provider authentication
�?  �?  └── session.go           # Session management (create/validate/cleanup)
�?  ├── config/
�?  �?  └── config.go            # Config load/save/encrypt/hot-reload
�?  ├── db/
�?  �?  └── db.go                # SQLite init, table creation, migrations
�?  ├── document/
�?  �?  └── manager.go           # Document upload/parse/chunk/embed/store
�?  ├── parser/
�?  �?  └── parser.go            # Multi-format parsing (PDF/Word/Excel/PPT/MD)
�?  ├── chunker/
�?  �?  └── chunker.go           # Text chunking (fixed size + overlap)
�?  ├── embedding/
�?  �?  └── service.go           # Embedding API client (text/image/batch)
�?  ├── llm/
�?  �?  └── service.go           # LLM Chat Completion API client
�?  ├── vectorstore/
�?  �?  └── store.go             # Vector storage & similarity search (in-memory cache)
�?  ├── query/
�?  �?  └── engine.go            # RAG query engine (classify �?retrieve �?generate)
�?  ├── pending/
�?  �?  └── manager.go           # Pending question management
�?  ├── product/
�?  �?  └── service.go           # Product management (CRUD, admin-product assignment)
�?  ├── backup/
�?  �?  └── backup.go            # Data backup & restore (full/incremental)
�?  ├── video/
�?  �?  └── parser.go            # Video parsing (ffmpeg keyframes + whisper transcription)
�?  └── email/
�?      └── service.go           # SMTP email sending (verification/test)
�?
├── frontend/
�?  └── dist/                    # Frontend compiled assets (SPA)
�?      ├── index.html
�?      ├── app.js
�?      └── styles.css
�?
└── data/
    ├── config.json              # System config (API keys encrypted)
    ├── encryption.key           # AES-256 encryption key
    ├── askflow.db              # SQLite database
    ├── uploads/                 # Uploaded original documents (by doc ID)
    └── images/                  # Knowledge entry images
```

---

## Quick Start

### Prerequisites

- Go 1.25+ (build time only)
- Accessible LLM and Embedding API endpoints (OpenAI-compatible)

### Build

```bash
# Linux / macOS
chmod +x build_local.sh
./build_local.sh

# Windows
go build -o askflow.exe .
```

### Run

```bash
./askflow
```

The server listens on `0.0.0.0:8080`. Open `http://localhost:8080` in your browser.

### Initialize Admin

On first launch, set up the super admin via the frontend or API:

```bash
curl -X POST http://localhost:8080/api/admin/setup \
  -H "Content-Type: application/json" \
  -d '{"username": "admin", "password": "your_password"}'
```

### Import Documents

```bash
# Upload a single file via API
curl -X POST http://localhost:8080/api/documents/upload \
  -F "file=@./product-manual.pdf" \
  -F "product_id=<product_id>"

# Batch import from directories
./askflow import ./docs

# Batch import targeting a specific product
./askflow import --product <product_id> ./docs ./manuals
```

### Ask a Question

```bash
curl -X POST http://localhost:8080/api/query \
  -H "Content-Type: application/json" \
  -d '{"question": "How do I install the product?"}'
```

---

## Configuration

The config file is located at `data/config.json`. It can be modified via the web admin UI or API.

### Server

| Field | Default | Description |
|-------|---------|-------------|
| `server.port` | `8080` | HTTP listen port |

### LLM

| Field | Default | Description |
|-------|---------|-------------|
| `llm.endpoint` | VolcEngine ARK | OpenAI-compatible API URL |
| `llm.api_key` | �?| API key (auto AES-encrypted on save) |
| `llm.model_name` | �?| Model name / Endpoint ID |
| `llm.temperature` | `0.3` | Generation temperature (0�?) |
| `llm.max_tokens` | `2048` | Max generation tokens |

### Embedding

| Field | Default | Description |
|-------|---------|-------------|
| `embedding.endpoint` | VolcEngine ARK | OpenAI-compatible API URL |
| `embedding.api_key` | �?| API key (auto AES-encrypted on save) |
| `embedding.model_name` | �?| Model name / Endpoint ID |
| `embedding.use_multimodal` | `true` | Enable image embedding |

### Vector Search

| Field | Default | Description |
|-------|---------|-------------|
| `vector.db_path` | `./data/askflow.db` | SQLite database path |
| `vector.chunk_size` | `512` | Text chunk size (characters) |
| `vector.overlap` | `128` | Overlap between adjacent chunks |
| `vector.top_k` | `5` | Number of top results to retrieve |
| `vector.threshold` | `0.5` | Cosine similarity threshold (0�?) |

### SMTP Email

| Field | Default | Description |
|-------|---------|-------------|
| `smtp.host` | �?| SMTP server address |
| `smtp.port` | `587` | SMTP port |
| `smtp.username` | �?| SMTP username |
| `smtp.password` | �?| SMTP password |
| `smtp.from_addr` | �?| Sender email address |
| `smtp.from_name` | �?| Sender display name |
| `smtp.use_tls` | `true` | Enable TLS |

### OAuth

Configure providers under `oauth.providers`:

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

Supported providers: `google`, `apple`, `amazon`, `facebook`.

### Other

| Field | Description |
|-------|-------------|
| `admin.username` | Super admin username (set during initialization) |
| `admin.password_hash` | Super admin password hash (bcrypt) |
| `admin.login_route` | Admin login route, default `/admin` |
| `product_intro` | Global product introduction text (used for intent classification context). Each product can have its own `welcome_message` set via product management, which takes priority over this global setting |

### Video Processing

| Field | Default | Description |
|-------|---------|-------------|
| `video.ffmpeg_path` | �?| ffmpeg executable path; empty disables video support |
| `video.whisper_path` | �?| whisper CLI executable path; empty skips speech transcription |
| `video.keyframe_interval` | `10` | Keyframe sampling interval in seconds |
| `video.whisper_model` | `base` | whisper model name |

Video features require external tools. With only `ffmpeg_path` configured, only keyframe extraction is performed. Adding `whisper_path` enables speech transcription as well.

### Advanced Vector Search Options

| Field | Default | Description |
|-------|---------|-------------|
| `vector.content_priority` | `image_text` | Result ordering: `image_text` prioritizes image-containing results, `text_only` prioritizes pure text |
| `vector.text_match_enabled` | `true` | Enable 3-level text matching to reduce API calls via local text matching and cache reuse |
| `vector.debug_mode` | `false` | When enabled, query responses include search diagnostic information |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ASKFLOW_ENCRYPTION_KEY` | AES-256 encryption key (32-byte hex). Auto-generated and saved to `data/encryption.key` if not set |

---

## CLI Usage

```
askflow                                              Start HTTP server
askflow import [--product <product_id>] <dir> [...]  Batch import documents into knowledge base
askflow backup [options]                              Backup all site data
askflow restore <backup_file>                         Restore data from backup
askflow help                                         Show help information
```

### Batch Import

Recursively scans specified directories and imports supported files into the vector database.

```bash
askflow import ./docs
askflow import ./docs ./manuals /path/to/files

# Import into a specific product
askflow import --product <product_id> ./docs
```

When `--product` is omitted, documents are imported into the Public Library. If the specified product ID does not exist, the system reports an error and aborts.

Supported file extensions: `.pdf` `.doc` `.docx` `.xls` `.xlsx` `.ppt` `.pptx` `.md` `.markdown` `.mp4` `.avi` `.mkv` `.mov` `.webm`

### Data Backup & Restore

The system provides a data-level backup mechanism with full and incremental modes.

Backup filename format: `askflow_<mode>_<hostname>_<date-time>.tar.gz`, e.g. `askflow_full_myserver_20260212-143000.tar.gz`.

#### Full Backup

Packages a complete database snapshot, all uploaded files, config, and encryption key into a tar.gz archive.

```bash
# Backup to current directory
askflow backup

# Backup to a specific directory
askflow backup --output ./backups
```

#### Incremental Backup

Based on a previous manifest file, exports only new database rows and newly uploaded files. Mutable tables (users, pending questions, products, etc.) are fully dumped to ensure updates are not lost.

```bash
askflow backup --incremental --base ./backups/askflow_full_myserver_20260212-143000.manifest.json
```

Incremental backup works at the data level, not the file level:
- Insert-only tables (documents, chunks, etc.): only rows with `created_at` after the last backup
- Mutable tables (users, pending_questions, products, etc.): full table dump (rows may be updated)
- Ephemeral tables (sessions, email_tokens): skipped (no need to backup)
- Upload files: only new directories since last backup

#### Restore

```bash
# Restore from a full backup
askflow restore askflow_full_myserver_20260212-143000.tar.gz

# Restore to a specific directory
askflow restore --target ./data-new backup.tar.gz
```

Incremental restore workflow: first restore the full backup, then apply each incremental `db_delta.sql` in order:

```bash
askflow restore full-backup.tar.gz
askflow restore incremental-backup.tar.gz
sqlite3 ./data/askflow.db < ./data/db_delta.sql
```

---

## API Reference

All APIs return JSON. Authenticated endpoints require `Authorization: Bearer <session_token>` header.

### Authentication

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `POST` | `/api/admin/setup` | Initialize super admin | Public (first time only) |
| `POST` | `/api/admin/login` | Admin login | Public |
| `GET` | `/api/admin/status` | Check if admin is configured | Public |
| `GET` | `/api/oauth/url?provider=xxx` | Get OAuth authorization URL | Public |
| `POST` | `/api/oauth/callback` | Handle OAuth callback | Public |
| `POST` | `/api/auth/register` | Email registration (captcha required) | Public |
| `POST` | `/api/auth/login` | Email login (captcha required) | Public |
| `GET` | `/api/auth/verify?token=xxx` | Email verification | Public |
| `GET` | `/api/captcha` | Get math captcha | Public |

### Smart Q&A

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `POST` | `/api/query` | Submit question, get RAG answer (supports `product_id` to scope search) | Public |
| `GET` | `/api/product-intro` | Get product introduction (supports `product_id` for per-product welcome message) | Public |

### Product Management

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `GET` | `/api/products` | List all products | Admin |
| `POST` | `/api/products` | Create a product | Super Admin |
| `PUT` | `/api/products/{id}` | Update a product | Super Admin |
| `DELETE` | `/api/products/{id}` | Delete a product | Super Admin |
| `GET` | `/api/products/my` | List products assigned to current admin | Admin |

### Document Management

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `POST` | `/api/documents/upload` | Upload file (multipart/form-data, supports `product_id` field) | Admin |
| `POST` | `/api/documents/url` | Import from URL (supports `product_id` parameter) | Admin |
| `GET` | `/api/documents` | List documents (supports `product_id` filter) | Admin |
| `DELETE` | `/api/documents/{id}` | Delete document | Admin |
| `GET` | `/api/documents/{id}/download` | Download original file | Admin |

### Pending Questions

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `GET` | `/api/pending?status=xxx` | List pending questions (supports `product_id` filter) | Admin |
| `POST` | `/api/pending/answer` | Answer a pending question | Admin |
| `DELETE` | `/api/pending/{id}` | Delete a pending question | Admin |

### Knowledge Entries

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `POST` | `/api/knowledge` | Add knowledge entry (supports `product_id` parameter) | Admin |
| `POST` | `/api/images/upload` | Upload image | Admin |
| `GET` | `/api/images/{filename}` | Get image | Public |

### Admin Accounts

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `GET` | `/api/admin/users` | List sub-admins | Super Admin |
| `POST` | `/api/admin/users` | Create sub-admin (supports `product_ids` for product assignment) | Super Admin |
| `DELETE` | `/api/admin/users/{id}` | Delete sub-admin | Super Admin |
| `GET` | `/api/admin/role` | Get current user role | Admin |

### System Configuration

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `GET` | `/api/config` | Get config (API keys masked) | Admin |
| `PUT` | `/api/config` | Update config (hot reload) | Super Admin |

### Email

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `POST` | `/api/email/test` | Send test email | Admin |

---

## RAG Workflow

```
User Question (text / text+image)
   �?
   �?
Intent Classification (LLM) �?skipped when image is attached
   �?
   ├── greeting �?Return friendly greeting
   ├── irrelevant �?Prompt user to ask product-related questions
   └── product ──�?
                  �?
            ┌─────┴─────�?
            �?          �?
         Text query  Image query (if any)
            �?          �?
            �?          �?
      Embed question  Embed image
      (Embedding)    (Multimodal Embedding)
            �?          �?
            �?          �?
      Vector search  Vector search (lower threshold)
      (Top-K)       (threshold × 0.6)
            �?          �?
            └─────┬─────�?
                  �?
            Merge & deduplicate results
                  �?
            Reorder by content_priority
                  �?
            Enrich with video timestamps
                  �?
           ┌──────┴──────�?
           �?            �?
       Results found   No results
           �?            �?
           �?            �?
     Build context   Create pending question
     Call LLM        Notify user to wait
     (Vision LLM
      if image)
           �?
           �?
     Return answer + source citations + video timestamps
```

### 3-Level Text Matching (API Cost Optimization)

```
User Question
   �?
   �?
Level 1: Local text matching (zero API cost)
   �? Character bigram similarity search against chunk cache
   �?
   ├── Hit + cached answer �?Return immediately (zero cost)
   └── Hit but no cache ──�?
                           �?
Level 2: Vector confirmation + cache reuse (Embedding API only)
   �? Call Embedding API for vector, search to confirm
   �?
   ├── Confirmed + cached answer �?Return (Embedding cost only)
   └── No cached answer ──�?
                           �?
Level 3: Full RAG (Embedding + LLM)
   �? Vector search �?Build context �?Call LLM to generate answer
   └── Return answer (full cost)
```

### Document Processing Pipeline

```
File Upload / URL Import / CLI Batch Import
   �?
   �?
File Type Detection
   �?
   ├── Documents (PDF/Word/Excel/PPT/Markdown/HTML)
   �?    �?
   �?    �?
   �?  Parse document (extract text + images)
   �?    �?
   �?    �?
   �?  Content dedup check (SHA-256 hash)
   �?    �?
   �?    ├── Duplicate �?Reject import
   �?    └── New content ──�?
   �?                       �?
   �?                 Text chunking + chunk-level dedup
   �?                       �?
   �?                       �?
   �?                 Text embedding + image multimodal embedding
   �?                       �?
   �?                       �?
   �?                 Store in SQLite + in-memory cache
   �?
   └── Video (MP4/AVI/MKV/MOV/WebM)
         �?
         ├── ffmpeg extract audio �?whisper transcription
         �?    �?
         �?    �?
         �?  Chunk transcript text �?embed �?store
         �?  Create video_segments records (with time ranges)
         �?
         └── ffmpeg extract keyframes at intervals
               �?
               �?
             Keyframes �?multimodal embedding �?store
             Create video_segments records (with timestamps)
```

---

## Security

- **API Key Encryption**: API keys in config are AES-256-GCM encrypted, ciphertext prefixed with `enc:`
- **Password Hashing**: Admin and user passwords stored as bcrypt hashes
- **Session Management**: 24-hour expiry, database-backed, with validation and cleanup
- **Captcha**: Math captcha required for email registration and login
- **Email Verification**: Registered users must verify email via link
- **Role-based Access**: Super admin / editor admin / regular user, API endpoints enforce role checks
- **File Type Validation**: Upload files and images validated against extension whitelist
- **SQLite WAL Mode**: Concurrent read support, foreign key constraints ensure data integrity

---

## Deployment

### Standalone

```bash
# Build
go build -o askflow .

# Run (can be managed with systemd or similar)
./askflow
```

### Remote Deploy (Windows �?Linux)

The `build.cmd` script uses PuTTY tools (plink/pscp) for one-click package, upload, remote build, and restart:

```cmd
build.cmd
```

This script will:
1. Package project files into `deploy.tar.gz`
2. Upload to remote server via SCP
3. Extract and compile on the remote server
4. Execute `start.sh` to restart the service

### Data Backup

The system includes a built-in CLI backup tool supporting full and incremental modes. See the [CLI Usage](#cli-usage) section for details.

Critical data files:
- `data/config.json` �?System configuration
- `data/askflow.db` �?Database (documents, vectors, users, sessions, etc.)
- `data/encryption.key` �?Encryption key (loss prevents decryption of encrypted API keys)
- `data/uploads/` �?Uploaded original documents
- `data/images/` �?Knowledge entry images

Backup examples:

```bash
# Full backup
askflow backup --output ./backups

# Incremental backup (based on previous full)
askflow backup --output ./backups --incremental --base ./backups/askflow_full_myserver_20260212-143000.manifest.json

# Restore
askflow restore ./backups/askflow_full_myserver_20260212-143000.tar.gz
```

---

## Database Schema

| Table | Description |
|-------|-------------|
| `products` | Product information (ID, name, description, welcome_message, created_at, updated_at) |
| `admin_user_products` | Admin-product junction table (admin_user_id, product_id, composite primary key) |
| `documents` | Document metadata (ID, name, type, status, content hash, product_id, created_at). Types include pdf/word/excel/ppt/markdown/html/video/url |
| `chunks` | Document chunks (text, vector, parent document, image URL, product_id). Video keyframe image_url stores base64 data |
| `video_segments` | Video segment timeline (document_id, segment_type, start_time, end_time, content, chunk_id). segment_type is "transcript" or "keyframe" |
| `pending_questions` | Pending questions (question, status, answer, user ID, image data, product_id) |
| `users` | Registered users (email, password hash, verification status) |
| `sessions` | User sessions (session ID, user ID, expiry) |
| `email_tokens` | Email verification tokens |
| `admin_users` | Sub-admin accounts (username, password hash, role) |

An empty or NULL `product_id` indicates the record belongs to the Public Library, which is accessible across all product searches.

---

## Related Documentation

- [introduce.md](./introduce.md) �?Product Introduction (Chinese)
- [manual.md](./manual.md) �?Detailed User Manual with full API examples (Chinese)
