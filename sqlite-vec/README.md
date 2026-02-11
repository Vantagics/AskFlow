# sqlite-vec

高性能向量存储与相似度检索库，基于 SQLite 持久化，支持 SIMD 加速。

## 特性

- **SIMD 加速**: 自动检测并使用 AVX-512 / AVX2+FMA / NEON / SSE 指令集
- **内存缓存**: 连续 float32 向量 arena，CPU 缓存友好
- **分区索引**: 支持按 partition 隔离检索，O(partition_size) 复杂度
- **文本检索**: 基于关键词重叠 + 字符 bigram Jaccard 相似度
- **LRU 缓存**: 查询结果缓存，避免重复计算
- **并发搜索**: 自适应 worker 数量，per-worker top-K 堆

## 安装

```bash
go get github.com/nicexipi/sqlite-vec
```

## 使用

```go
import (
    "database/sql"
    sqlitevec "github.com/nicexipi/sqlite-vec"
    _ "github.com/mattn/go-sqlite3"
)

// 打开数据库
db, _ := sql.Open("sqlite3", "data.db")

// 创建表（如果不存在）
sqlitevec.EnsureTable(db)

// 创建向量存储
store := sqlitevec.NewSQLiteVectorStore(db)

// 存储向量
chunks := []sqlitevec.VectorChunk{
    {
        ChunkText:    "hello world",
        ChunkIndex:   0,
        DocumentID:   "doc1",
        DocumentName: "test.pdf",
        Vector:       []float64{0.1, 0.2, 0.3},
    },
}
store.Store("doc1", chunks)

// 向量检索
results, _ := store.Search([]float64{0.1, 0.2, 0.3}, 5, 0.5, "")

// 文本检索
results, _ = store.TextSearch("hello", 5, 0.3, "")

// 查看 SIMD 加速状态
fmt.Println(sqlitevec.SIMDCapability())
```

## API

### 类型

- `VectorChunk` - 文档分块与嵌入向量
- `SearchResult` - 检索结果（含相似度分数）
- `VectorStore` - 向量存储接口

### 函数

- `NewSQLiteVectorStore(db)` - 创建向量存储实例
- `EnsureTable(db)` - 创建 chunks 表和索引
- `SIMDCapability()` - 返回当前 SIMD 加速状态
- `SerializeVector(vec)` / `DeserializeVector(data)` - 向量序列化
- `CosineSimilarity(a, b)` - 余弦相似度计算
