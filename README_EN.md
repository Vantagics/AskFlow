# Helpdesk

[🇨🇳 中文](./README.md) | 🇬🇧 English

An AI-powered helpdesk knowledge base system built on RAG (Retrieval-Augmented Generation). Upload product documents, and the system automatically retrieves relevant content and generates accurate answers via LLM when users ask questions.

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

- **Smart Q&A**: Intent classification → vector retrieval → LLM answer generation with source citations
- **Multi-format Documents**: Upload and parse PDF, Word, Excel, PPT, and Markdown files
- **URL Import**: Fetch and index web page content via URL
- **Batch Import**: CLI recursive directory scan for bulk document import
- **Multimodal Retrieval**: Vectorize and search both text and images
- **Knowledge Entries**: Admins can directly add text + image knowledge entries
- **Content Deduplication**: Document-level SHA-256 hash dedup + chunk-level embedding reuse to prevent duplicate imports and redundant API calls
- **Pending Questions**: Unanswered questions are automatically queued; admin answers are auto-indexed
- **User Authentication**: OAuth 2.0 (Google / Apple / Amazon / Facebook) + email/password registration
- **Admin Hierarchy**: Super admin + sub-admins (editor role) with role-based access control
- **Hot Reload**: Modify LLM / Embedding / SMTP settings via web UI without restart
- **Encrypted Storage**: API keys stored with AES-256-GCM encryption in config file
- **Email Service**: SMTP email verification and test email sending

---

## Architecture

```
┌─────────────────────────────────────────────────────┐
│                   Frontend SPA                       │
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
│              │  (WAL mode + in-memory cache +      │  │
│              │   cosine similarity search)          │  │
│              └────────────────────────────────────┘  │
└─────────────────────────────────────────────────────┘
                       │
          ┌────────────▼────────────┐
          │  OpenAI-compatible API   │
          │  (LLM + Embedding)       │
          └─────────────────────────┘
```

| Component | Technology |
|-----------|------------|
| Backend | Go 1.25+ |
| Database | SQLite (WAL mode, foreign keys) |
| Vector Store | SQLite persistence + in-memory cache, concurrent cosine similarity search |
| LLM | OpenAI-compatible Chat Completion API (default: VolcEngine ARK) |
| Embedding | OpenAI-compatible Embedding API (multimodal support) |
| Document Parsing | GoPDF2, GoWord, GoExcel, GoPPT |
| Frontend | SPA (compiled assets in frontend/dist) |
| Authentication | OAuth 2.0 + bcrypt + Session |
| Encryption | AES-256-GCM |
| Email | SMTP (TLS) |

---

## Project Structure

```
helpdesk/
├── main.go                      # Entry point: init, routing, HTTP server
├── app.go                       # API facade: aggregates all service components
├── go.mod / go.sum              # Go module dependencies
├── build_local.sh               # Local build script (Linux/macOS)
├── build.cmd                    # Remote deploy script (Windows → Linux server)
│
├── internal/
│   ├── auth/
│   │   ├── oauth.go             # OAuth 2.0 multi-provider authentication
│   │   └── session.go           # Session management (create/validate/cleanup)
│   ├── config/
│   │   └── config.go            # Config load/save/encrypt/hot-reload
│   ├── db/
│   │   └── db.go                # SQLite init, table creation, migrations
│   ├── document/
│   │   └── manager.go           # Document upload/parse/chunk/embed/store
│   ├── parser/
│   │   └── parser.go            # Multi-format parsing (PDF/Word/Excel/PPT/MD)
│   ├── chunker/
│   │   └── chunker.go           # Text chunking (fixed size + overlap)
│   ├── embedding/
│   │   └── service.go           # Embedding API client (text/image/batch)
│   ├── llm/
│   │   └── service.go           # LLM Chat Completion API client
│   ├── vectorstore/
│   │   └── store.go             # Vector storage & similarity search (in-memory cache)
│   ├── query/
│   │   └── engine.go            # RAG query engine (classify → retrieve → generate)
│   ├── pending/
│   │   └── manager.go           # Pending question management
│   └── email/
│       └── service.go           # SMTP email sending (verification/test)
│
├── frontend/
│   └── dist/                    # Frontend compiled assets (SPA)
│       ├── index.html
│       ├── app.js
│       └── styles.css
│
└── data/
    ├── config.json              # System config (API keys encrypted)
    ├── encryption.key           # AES-256 encryption key
    ├── helpdesk.db              # SQLite database
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
go build -o helpdesk.exe .
```

### Run

```bash
./helpdesk
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
  -F "file=@./product-manual.pdf"

# Batch import from directories
./helpdesk import ./docs ./manuals
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
| `llm.api_key` | — | API key (auto AES-encrypted on save) |
| `llm.model_name` | — | Model name / Endpoint ID |
| `llm.temperature` | `0.3` | Generation temperature (0–1) |
| `llm.max_tokens` | `2048` | Max generation tokens |

### Embedding

| Field | Default | Description |
|-------|---------|-------------|
| `embedding.endpoint` | VolcEngine ARK | OpenAI-compatible API URL |
| `embedding.api_key` | — | API key (auto AES-encrypted on save) |
| `embedding.model_name` | — | Model name / Endpoint ID |
| `embedding.use_multimodal` | `true` | Enable image embedding |

### Vector Search

| Field | Default | Description |
|-------|---------|-------------|
| `vector.db_path` | `./data/helpdesk.db` | SQLite database path |
| `vector.chunk_size` | `512` | Text chunk size (characters) |
| `vector.overlap` | `128` | Overlap between adjacent chunks |
| `vector.top_k` | `5` | Number of top results to retrieve |
| `vector.threshold` | `0.5` | Cosine similarity threshold (0–1) |

### SMTP Email

| Field | Default | Description |
|-------|---------|-------------|
| `smtp.host` | — | SMTP server address |
| `smtp.port` | `587` | SMTP port |
| `smtp.username` | — | SMTP username |
| `smtp.password` | — | SMTP password |
| `smtp.from_addr` | — | Sender email address |
| `smtp.from_name` | — | Sender display name |
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
| `product_intro` | Product introduction text (used for intent classification context) |

### Environment Variables

| Variable | Description |
|----------|-------------|
| `HELPDESK_ENCRYPTION_KEY` | AES-256 encryption key (32-byte hex). Auto-generated and saved to `data/encryption.key` if not set |

---

## CLI Usage

```
helpdesk                          Start HTTP server
helpdesk import <dir> [...]       Batch import documents into knowledge base
helpdesk help                     Show help information
```

### Batch Import

Recursively scans specified directories and imports supported files into the vector database.

```bash
helpdesk import ./docs
helpdesk import ./docs ./manuals /path/to/files
```

Supported file extensions: `.pdf` `.doc` `.docx` `.xls` `.xlsx` `.ppt` `.pptx` `.md` `.markdown`

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
| `POST` | `/api/query` | Submit question, get RAG answer | Public |
| `GET` | `/api/product-intro` | Get product introduction | Public |

### Document Management

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `POST` | `/api/documents/upload` | Upload file (multipart/form-data) | Admin |
| `POST` | `/api/documents/url` | Import from URL | Admin |
| `GET` | `/api/documents` | List all documents | Admin |
| `DELETE` | `/api/documents/{id}` | Delete document | Admin |
| `GET` | `/api/documents/{id}/download` | Download original file | Admin |

### Pending Questions

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `GET` | `/api/pending?status=xxx` | List pending questions | Admin |
| `POST` | `/api/pending/answer` | Answer a pending question | Admin |
| `DELETE` | `/api/pending/{id}` | Delete a pending question | Admin |

### Knowledge Entries

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `POST` | `/api/knowledge` | Add knowledge entry | Admin |
| `POST` | `/api/images/upload` | Upload image | Admin |
| `GET` | `/api/images/{filename}` | Get image | Public |

### Admin Accounts

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `GET` | `/api/admin/users` | List sub-admins | Super Admin |
| `POST` | `/api/admin/users` | Create sub-admin | Super Admin |
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
User Question
   │
   ▼
Intent Classification (LLM)
   │
   ├── greeting → Return friendly greeting
   ├── irrelevant → Prompt user to ask product-related questions
   └── product ──▼
                  │
            Vectorize Question (Embedding API)
                  │
                  ▼
            Vector Similarity Search (Top-K, cosine similarity ≥ threshold)
                  │
           ┌──────┴──────┐
           │             │
       Results found   No results
           │             │
           ▼             ▼
     Build context   Create pending question
     Call LLM        Notify user to wait
     Generate answer
           │
           ▼
     Return answer + source citations
```

### Document Processing Pipeline

```
File Upload / URL Import / CLI Batch Import
   │
   ▼
Document Parsing (PDF/Word/Excel/PPT/Markdown)
   │
   ▼
Content Dedup Check (SHA-256 hash against existing documents)
   │
   ├── Duplicate → Reject import, return existing document ID
   └── New content ──▼
                      │
                Text Chunking (512 chars, 128 char overlap)
                      │
                      ▼
                Chunk-level Dedup (reuse embeddings for identical text)
                      │
                      ▼
                Call Embedding API only for new chunks
                      │
                      ▼
                Store in SQLite + In-memory Cache
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
go build -o helpdesk .

# Run (can be managed with systemd or similar)
./helpdesk
```

### Remote Deploy (Windows → Linux)

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

Critical data files:
- `data/config.json` — System configuration
- `data/helpdesk.db` — Database (documents, vectors, users, sessions, etc.)
- `data/encryption.key` — Encryption key (loss prevents decryption of encrypted API keys)
- `data/uploads/` — Uploaded original documents
- `data/images/` — Knowledge entry images

---

## Database Schema

| Table | Description |
|-------|-------------|
| `documents` | Document metadata (ID, name, type, status, content hash, created_at) |
| `chunks` | Document chunks (text, vector, parent document, image URL) |
| `pending_questions` | Pending questions (question, status, answer, user ID) |
| `users` | Registered users (email, password hash, verification status) |
| `sessions` | User sessions (session ID, user ID, expiry) |
| `email_tokens` | Email verification tokens |
| `admin_users` | Sub-admin accounts (username, password hash, role) |

---

## Related Documentation

- [introduce.md](./introduce.md) — Product Introduction (Chinese)
- [manual.md](./manual.md) — Detailed User Manual with full API examples (Chinese)
