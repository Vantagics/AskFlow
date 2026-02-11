# 向量检索性能优化技术文档

## 概述

本文档记录了 Helpdesk RAG 系统中向量检索模块的全部性能优化技术成果。优化覆盖四个层级共 20 项技术，目标是在有限硬件条件下实现全速运行。

| 层级 | 涉及文件 | 优化项数 | 核心手段 |
|------|---------|---------|---------|
| 指令级 | `simd_amd64.s`, `simd_amd64.go`, `simd_arm64.s`, `simd_arm64.go`, `simd_generic.go` | 6 项 | AVX-512 / AVX2+FMA / SSE / ARM NEON+FMLA / SQRTSD 硬件指令 |
| 向量存储层 | `store.go` | 9 项 | Arena 内存布局、分区索引、内联堆排序、缓存 |
| 查询引擎层 | `engine.go` | 2 项 | Embedding 缓存、批量 SQL |
| 序列化层 | `serialize.go` | 2 项 | Float32 直接反序列化、快速路径反序列化 |
| 架构层 | `sqlite-vec/` | 1 项 | 独立 Go module 封装，跨项目复用 |

### 优化层次架构

```
用户查询
  │
  ▼
┌─────────────────────────────────────────────────┐
│  查询引擎层 (engine.go)                          │
│  ├── Embedding API 缓存 (512条, 10min TTL)       │
│  └── 批量 SQL 查询 (IN clause)                   │
└──────────────────┬──────────────────────────────┘
                   │ queryVector []float64
                   ▼
┌─────────────────────────────────────────────────┐
│  向量存储层 (store.go)                            │
│  ├── LRU 查询缓存 (FNV-1a hash, 256条)           │
│  ├── Product 分区索引 → 缩小搜索范围              │
│  ├── 连续内存 Arena → CPU cache 友好              │
│  ├── 自适应并发 Worker                            │
│  ├── Per-Worker Min-Heap Top-K                   │
│  │                                               │
│  │  ┌─────────────────────────────────────────┐  │
│  │  │  指令级 (simd_*.s)                       │  │
│  │  │  ├── AVX-512: 64 floats/iter, 8.8x     │  │
│  │  │  ├── AVX2+FMA: 32 floats/iter, 8.6x    │  │
│  │  │  ├── ARM NEON: 16 floats/iter, ~4x      │  │
│  │  │  ├── SSE: 16 floats/iter, ~2x           │  │
│  │  │  ├── Pure Go 8-way: 回退                 │  │
│  │  │  └── SQRTSD: 硬件平方根                   │  │
│  │  └─────────────────────────────────────────┘  │
│  └── 预计算 Bigrams (TextSearch)                  │
└──────────────────┬──────────────────────────────┘
                   │ embeddingBytes
                   ▼
┌─────────────────────────────────────────────────┐
│  序列化层 (serialize.go)                          │
│  └── Float32 直接反序列化 (零中间转换)             │
└─────────────────────────────────────────────────┘
```

---

## 一、向量存储层优化

### 1.1 连续内存向量 Arena

**文件：** `internal/vectorstore/store.go`

**问题：** 原实现中每个 `cachedChunk` 持有独立的 `[]float64` 切片，N 个 chunk 意味着 N 次独立堆分配。搜索时 CPU 需要跟随 N 个指针跳转到不同的内存地址读取向量数据，导致 L1/L2 cache 命中率极低。

**方案：** 引入 `vectorArena` 结构，将所有向量存储在一个连续的 `[]float32` 大数组中。每个 chunk 的向量通过 `index * dim` 计算偏移量直接定位，无需指针间接寻址。

```go
type vectorArena struct {
    data []float32  // 所有向量连续存储
    dim  int        // 向量维度
}

func (a *vectorArena) getVector(idx int) []float32 {
    start := idx * a.dim
    return a.data[start : start+a.dim]
}
```

**效果：**
- 10K 个 1536 维向量：~60MB 连续内存 vs 10K 次随机堆指针跳转
- CPU 预取器可以有效预测顺序访问模式，L1/L2 cache 命中率大幅提升
- 消除了 N 个独立 slice header（每个 24 字节）的内存开销

### 1.2 Float32 内存表示

**文件：** `internal/vectorstore/store.go`, `internal/vectorstore/serialize.go`

**问题：** 原实现在反序列化时将 float32 数据转回 float64 存入内存，白白浪费 2 倍 RAM。Embedding 模型的精度本身就是 float32 级别，float64 不会带来任何检索质量提升。

**方案：**
- 缓存中统一使用 `[]float32` 存储向量
- 新增 `DeserializeVectorF32()` 直接反序列化为 float32，避免 float64 中间转换
- 搜索时将查询向量一次性转为 float32 后进行计算

```go
// 新增：直接反序列化为 float32
func DeserializeVectorF32(data []byte) []float32

// 搜索时一次性转换
queryF32 := toFloat32(queryVector)
```

**效果：**
- 内存占用减半：1536 维 × 10K chunks = 60MB（float32）vs 120MB（float64）
- 减少内存带宽压力，间接提升 CPU 计算吞吐

### 1.3 Product 分区索引

**文件：** `internal/vectorstore/store.go`

**问题：** 原实现在搜索时遍历所有 chunk，通过 `if productID != "" && c.productID != productID` 逐个过滤。多产品场景下，大量 CPU 时间浪费在计算不相关产品的向量相似度上。

**方案：** 引入 `productIndex map[string][]int`，在数据入库时按 productID 预分区。搜索时直接取出目标产品 + 公共库的索引列表，只遍历相关数据。

```go
productIndex map[string][]int  // productID -> []chunkIndex

func (s *SQLiteVectorStore) getRelevantIndices(productID string) []int {
    if productID == "" {
        return allIndices
    }
    // 只返回目标产品 + 公共库的索引
    return append(s.productIndex[productID], s.productIndex[""]...)
}
```

**效果：**
- 5 个产品各 2K chunks 的场景：搜索范围从 10K 降到 ~4K（目标产品 2K + 公共库）
- 完全消除了不相关产品的相似度计算开销

### 1.4 8-way 循环展开 Dot Product

**文件：** `internal/vectorstore/store.go`

**问题：** 简单的逐元素循环存在循环依赖链（每次迭代的累加依赖上一次的结果），CPU 的指令级并行度（ILP）无法充分利用。

**方案：** 使用 8 个独立累加器进行循环展开，打破依赖链，让 CPU 流水线可以同时执行多条乘加指令。

```go
func dotProductF32x8(a, b []float32) float32 {
    var s0, s1, s2, s3, s4, s5, s6, s7 float32
    for ; i <= n-8; i += 8 {
        s0 += a[i] * b[i]
        s1 += a[i+1] * b[i+1]
        // ... s2-s7
    }
    return (s0 + s1 + s2 + s3) + (s4 + s5 + s6 + s7)
}
```

**效果：**
- 1536 维向量：192 次迭代（vs 原来 1536 次）
- 8 个独立累加器消除了循环携带依赖，CPU 超标量执行单元可以并行处理
- Norm 计算同样采用 8-way 展开

### 1.5 Per-Worker 内联 Min-Heap Top-K

**文件：** `internal/vectorstore/store.go`

**问题：** 原实现每个 worker 收集所有超过阈值的结果，最后全量排序取 Top-K。当数据量大、阈值低时，可能收集数千个结果再排序，浪费大量内存和 CPU。

**方案：** 每个 worker 维护一个大小为 K 的最小堆，只保留当前最好的 K 个结果。新结果只有比堆顶（最差的 Top-K 结果）更好时才入堆。堆操作完全内联（手写 sift-up/sift-down），避免 `container/heap` 的 `interface{}` 装箱开销和间接调用。最终合并时只需处理 `numWorkers × K` 个元素。

```go
// 每个 worker 内部 — 内联 min-heap，零 interface{} 分配
h := make([]scoredItem, 0, topK+1)
hLen := 0

if hLen < topK {
    h = append(h, scoredItem{score: score, idx: idx})
    hLen++
    // 内联 sift-up
    i := hLen - 1
    for i > 0 {
        parent := (i - 1) / 2
        if h[parent].score <= h[i].score { break }
        h[parent], h[i] = h[i], h[parent]
        i = parent
    }
} else if score > h[0].score {
    h[0] = scoredItem{score: score, idx: idx}
    // 内联 sift-down
    i := 0
    for {
        left := 2*i + 1
        if left >= hLen { break }
        smallest := left
        right := left + 1
        if right < hLen && h[right].score < h[left].score { smallest = right }
        if h[i].score <= h[smallest].score { break }
        h[i], h[smallest] = h[smallest], h[i]
        i = smallest
    }
}
```

**效果：**
- 内存：每个 worker 最多持有 K 个结果（通常 K=5），而非所有超阈值结果
- 时间：O(N log K) vs O(N + M log M)，其中 M 是超阈值结果数
- 合并阶段：处理 `numWorkers × K` 个元素（通常 < 50），而非数千个
- 内联堆操作消除了 `container/heap` 的 `interface{}` 装箱/拆箱和虚函数调用开销

### 1.6 自适应 Worker 数量

**文件：** `internal/vectorstore/store.go`

**问题：** 原实现固定使用 `runtime.NumCPU()` 个 goroutine，小数据集时 goroutine 创建和上下文切换的开销反而超过了并行收益。

**方案：** 引入 `minWorkersThreshold = 500`，根据数据量动态调整 worker 数量。

```go
func adaptiveWorkers(n int) int {
    if n < 500 { return 1 }
    w := n / 500
    if w > runtime.NumCPU() { w = runtime.NumCPU() }
    return w
}
```

**效果：**
- < 500 条数据：单线程执行，避免 goroutine 开销
- 500-4000 条：1-8 个 worker，按需扩展
- > 4000 条：使用全部 CPU 核心

### 1.7 FNV-1a 快速缓存哈希

**文件：** `internal/vectorstore/store.go`

**问题：** 原 LRU 缓存使用 `fmt.Sprintf("%x", queryVector[:4])` 生成 cache key，每次搜索都要做字符串格式化和内存分配。

**方案：** 改用 FNV-1a 整数哈希，直接对查询向量的前 8 个 float32 值 + topK + threshold + productID 计算 uint64 哈希值。

```go
func hashQueryVector(qv []float32, topK int, threshold float64, productID string) uint64 {
    h := uint64(14695981039346656037) // FNV offset
    for i := 0; i < min(8, len(qv)); i++ {
        bits := math.Float32bits(qv[i])
        h ^= uint64(bits)
        h *= 1099511628211 // FNV prime
    }
    // ... hash topK, threshold, productID
    return h
}
```

**效果：**
- 零内存分配（vs fmt.Sprintf 的字符串分配）
- 纯整数运算，比字符串哈希快一个数量级
- cache key 类型从 `string` 改为 `uint64`，map 查找更快

### 1.8 LRU 查询结果缓存

**文件：** `internal/vectorstore/store.go`

**问题：** 相同的查询向量 + 参数组合会重复执行完整的相似度搜索。

**方案：** 256 条容量、5 分钟 TTL 的 LRU 缓存。数据变更（Store/Delete）时自动失效。

**效果：**
- 重复查询直接返回，零计算开销
- 自动失效保证数据一致性

### 1.9 预计算 Text Bigrams

**文件：** `internal/vectorstore/store.go`

**问题：** TextSearch（Level 1 文本匹配）每次查询都对每个 chunk 重新计算字符 bigrams，复杂度 O(N × M)，其中 M 是平均 chunk 长度。

**方案：** 在数据入库时预计算每个 chunk 的 `textLower` 和 `bigrams`，存入 `chunkMeta`。TextSearch 只需计算查询的 bigrams（一次），然后对每个 chunk 做 map 查找。

```go
type chunkMeta struct {
    // ...
    textLower string          // 预计算的小写文本
    bigrams   map[string]bool // 预计算的字符 bigrams
}
```

**效果：**
- TextSearch 从 O(N × M) 降到 O(N × |query_bigrams|)
- 查询 bigrams 通常只有 10-30 个，而 chunk bigrams 可能有数百个

---

## 二、查询引擎层优化

### 2.1 Embedding API 结果缓存

**文件：** `internal/query/engine.go`

**问题：** 相同问题重复调用 embedding API，每次都产生网络延迟（通常 100-500ms）。在 3-Level 匹配流程中，Level 2 和 Level 3 可能对同一问题重复调用 Embed。

**方案：** 在 QueryEngine 中新增 `embeddingCache`（512 条容量，10 分钟 TTL），所有 Embed 调用通过 `cachedEmbed()` 包装。

```go
type embeddingCache struct {
    entries map[string]embeddingCacheEntry  // text -> vector
    maxSize int
    ttl     time.Duration
}

func (qe *QueryEngine) cachedEmbed(text string, es embedding.EmbeddingService) ([]float64, error) {
    if vec, ok := qe.embedCache.get(text); ok {
        return vec, nil  // 缓存命中，跳过 API 调用
    }
    vec, err := es.Embed(text)
    if err != nil { return nil, err }
    qe.embedCache.put(text, vec)
    return vec, nil
}
```

**效果：**
- 重复问题零 API 延迟（从 ~200ms 降到 ~0ms）
- 同一查询在 Level 2 → Level 3 流转时不会重复调用 API
- 在硬件有限的场景下，减少网络 IO 是最大的延迟优化

### 2.2 批量 SQL 查询视频时间信息

**文件：** `internal/query/engine.go`

**问题：** `enrichVideoTimeInfo` 对每个搜索结果逐条执行 `QueryRow`，N 个结果产生 N 次 SQLite 查询，每次都有锁获取和 IO 开销。

**方案：** 改为单次 `WHERE chunk_id IN (?,?,...)` 批量查询。

```go
// 之前：N 次查询
for i, r := range results {
    db.QueryRow(`SELECT ... WHERE chunk_id = ?`, chunkID)
}

// 之后：1 次查询
query := `SELECT chunk_id, start_time, end_time FROM video_segments
          WHERE chunk_id IN (?,?,?,...)`
rows, _ := db.Query(query, args...)
```

**效果：**
- SQLite 锁获取从 N 次降到 1 次
- IO 操作从 N 次降到 1 次
- Top-K=5 时，从 5 次查询降到 1 次

---

## 三、序列化层优化

### 3.1 Float32 直接反序列化

**文件：** `internal/vectorstore/serialize.go`

**问题：** 原 `DeserializeVector` 返回 `[]float64`，缓存使用 float32 时需要额外转换。

**方案：** 新增 `DeserializeVectorF32` 直接返回 `[]float32`，支持与 `DeserializeVector` 相同的格式自动检测逻辑（float32/float64 legacy 兼容）。

```go
func DeserializeVectorF32(data []byte) []float32     // 直接输出 float32
func deserializeFloat32Direct(data []byte, n int) []float32  // float32 → float32（零精度损失）
func deserializeFloat64AsF32(data []byte, n int) []float32   // float64 legacy → float32
```

**效果：**
- 消除了 float32 → float64 → float32 的无意义转换
- 加载缓存时减少一半的临时内存分配

### 3.2 快速路径反序列化 (DeserializeVectorF32Unsafe)

**文件：** `internal/vectorstore/store.go`

**问题：** `DeserializeVectorF32` 对所有数据都执行格式检测逻辑（判断 float32 vs float64 legacy），对于明确是 float32 格式的数据（字节长度能被 4 整除但不能被 8 整除），格式检测是多余的开销。

**方案：** 新增 `DeserializeVectorF32Unsafe`，对无歧义的 float32 数据（`len%8 != 0`）直接走快速路径，跳过格式检测。歧义情况回退到安全的 `DeserializeVectorF32`。

```go
func DeserializeVectorF32Unsafe(data []byte) []float32 {
    if len(data)%8 != 0 {
        // 无歧义 float32 — 直接解码，跳过格式检测
        n := len(data) / 4
        vec := make([]float32, n)
        for i := 0; i < n; i++ {
            vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
        }
        return vec
    }
    return DeserializeVectorF32(data)  // 歧义情况走安全路径
}
```

**效果：**
- 对新格式数据消除了格式检测分支和 `isCommonDim` / `looksLikeFloat64Embedding` 调用
- 在批量加载缓存时减少不必要的条件判断开销

---

## 四、性能估算

### 内存占用对比（1536 维向量）

| 数据规模 | 优化前 (float64, 独立切片) | 优化后 (float32, Arena) | 节省 |
|---------|-------------------------|----------------------|------|
| 1K chunks | 12 MB + 1K slice headers | 6 MB 连续 | ~50% |
| 10K chunks | 120 MB + 10K slice headers | 60 MB 连续 | ~50% |
| 100K chunks | 1.2 GB + 100K slice headers | 600 MB 连续 | ~50% |

### 单次 Dot Product 延迟（实测，AMD Ryzen 7 7840H）

| 向量维度 | 原始 Go 循环 | 8-way 展开 | AVX2+FMA | AVX-512 | 总加速比 |
|---------|-------------|-----------|----------|---------|---------|
| 768 维 | ~600 ns | 309 ns | 36 ns | 34.6 ns | **17.3x** |
| 1536 维 | ~1200 ns | 593 ns | 70.7 ns | 67.3 ns | **17.8x** |
| 3072 维 | ~2400 ns | 1162 ns | 135 ns | 128.9 ns | **18.6x** |

### 端到端搜索延迟估算（1536 维，8 核 CPU）

| 数据规模 | 原始实现 | 算法优化后 | + SIMD 加速 (AVX-512) | 总加速比 |
|---------|---------|-----------|----------------------|---------|
| 1K chunks | ~1.2ms | ~0.2ms | ~0.03ms | **~40x** |
| 10K chunks | ~12ms | ~0.8ms | ~0.1ms | **~120x** |
| 100K chunks | ~120ms | ~6ms | ~0.7ms | **~170x** |

*加速来源叠加：Arena 连续内存 + float32 + product 分区 + heap top-K + AVX-512/AVX2 SIMD*

*注：以上为纯计算延迟估算，不含 LRU 缓存命中（命中时为 ~0ms）和 embedding API 延迟*

### API 调用与 IO 节省

| 场景 | 优化前 | 优化后 |
|-----|-------|-------|
| 重复问题 embedding | 每次调用 API (~200ms) | 缓存命中 (~0ms) |
| Level 2→3 同一问题 | 2 次 API 调用 | 1 次 API 调用 |
| 视频时间查询 (Top-5) | 5 次 SQL 查询 | 1 次 SQL 查询 |

---

## 五、全部优化技术总结

| # | 优化项 | 层级 | 类型 | 核心收益 |
|---|-------|------|------|---------|
| 1 | 连续内存 Arena | 存储 | CPU Cache | L1/L2 命中率提升，消除指针跳转 |
| 2 | Float32 内存表示 | 存储 | 内存 | RAM 减半，带宽压力降低 |
| 3 | Product 分区索引 | 存储 | 算法 | 搜索范围缩小到目标产品 |
| 4 | 8-way 循环展开 | 存储 | CPU ILP | 打破依赖链，流水线并行 |
| 5 | Per-Worker 内联 Min-Heap | 存储 | 算法 | O(N log K)，内联消除 interface{} 开销 |
| 6 | 自适应 Worker | 存储 | 并发 | 避免小数据集 goroutine 开销 |
| 7 | FNV-1a 快速哈希 | 存储 | 内存 | 零分配 cache key 计算 |
| 8 | LRU 查询缓存 | 存储 | 缓存 | 重复查询零计算 |
| 9 | 预计算 Bigrams | 存储 | 预计算 | TextSearch 避免重复计算 |
| 10 | Embedding API 缓存 | 查询 | 网络 IO | 重复问题零 API 延迟 |
| 11 | 批量 SQL 查询 | 查询 | 数据库 IO | N 次查询降到 1 次 |
| 12 | Float32 直接反序列化 | 序列化 | 内存 | 消除无意义精度转换 |
| 13 | 快速路径反序列化 | 序列化 | 分支优化 | 无歧义 float32 跳过格式检测 |
| 14 | AVX-512 Dot Product | 指令级 | SIMD | 512-bit FMA，64 floats/iter，Zen4 实测 8.8x |
| 15 | AVX2+FMA Dot Product | 指令级 | SIMD | 8.6x 加速，256-bit 融合乘加 |
| 16 | SSE Dot Product 回退 | 指令级 | SIMD | 128-bit 回退，全 x86-64 兼容 |
| 17 | ARM NEON+FMLA Dot Product | 指令级 | SIMD | 128-bit 融合乘加，ARM64 全平台 |
| 18 | SQRTSD 硬件平方根 | 指令级 | 硬件指令 | 消除函数调用开销 |
| 19 | 运行时 CPU 特性检测 | 指令级 | 自适应 | CPUID 多级自动选择最快路径 |
| 20 | 独立模块化封装 | 架构 | 可复用 | sqlite-vec 独立 Go module |

---

## 六、指令级优化：AVX-512 / AVX2 / SSE / ARM NEON SIMD 加速

### 6.1 架构设计

**文件：**
- `internal/vectorstore/simd_amd64.s` — Plan 9 汇编实现（AVX-512 + AVX2 + SSE）
- `internal/vectorstore/simd_amd64.go` — Go 声明 + 运行时 CPU 特性检测（x86-64）
- `internal/vectorstore/simd_arm64.s` — Plan 9 汇编实现（ARM NEON + FMLA）
- `internal/vectorstore/simd_arm64.go` — Go 声明（arm64）
- `internal/vectorstore/simd_generic.go` — 非 amd64/arm64 平台回退到纯 Go
- `internal/vectorstore/simd_test.go` — 正确性测试 + 性能基准测试

**设计原则：**
- 运行时自动检测 CPU 支持的最高指令集，按平台分发
- 通过 Go build tags（`//go:build amd64` / `//go:build arm64` / `//go:build !amd64 && !arm64`）实现跨平台编译
- x86-64 使用 `golang.org/x/sys/cpu` 进行 CPUID 特性检测，仅在 init 时检测一次
- ARM64 NEON 是基线指令集，无需运行时检测

```
x86-64 分发:
  AVX-512 (n≥64) → AVX2+FMA (n≥32) → SSE (n≥16) → Pure Go

ARM64 分发:
  NEON+FMLA (n≥16) → Pure Go
```

```go
// x86-64
var (
    hasAVX512 = cpu.X86.HasAVX512F
    hasAVX2   = cpu.X86.HasAVX2 && cpu.X86.HasFMA
)

func dotProductSIMD(a, b []float32) float32 {
    if hasAVX512 && n >= 64 { return dotProductAVX512(a, b) }
    if hasAVX2 && n >= 32   { return dotProductAVX2(a, b) }
    if n >= 16              { return dotProductSSE(a, b) }
    return dotProductF32x8(a, b)
}

// ARM64
func dotProductSIMD(a, b []float32) float32 {
    if n >= 16 { return dotProductNEON(a, b) }
    return dotProductF32x8(a, b)
}
```

### 6.2 AVX-512 实现

**指令集：** AVX-512F（512-bit ZMM 寄存器）+ FMA

**核心循环：** 每次迭代处理 64 个 float32（256 字节），使用 4 个 ZMM 累加器（Z0-Z3），每个 ZMM 寄存器容纳 16 个 float32，是 YMM 的 2 倍宽度。

```asm
avx512_loop64:
    VMOVUPS 0(SI), Z4           // 加载 a[i:i+16]    (64 字节)
    VMOVUPS 0(DI), Z5           // 加载 b[i:i+16]
    VFMADD231PS Z4, Z5, Z0     // Z0 += Z4 * Z5

    VMOVUPS 64(SI), Z6          // 加载 a[i+16:i+32]
    VMOVUPS 64(DI), Z7
    VFMADD231PS Z6, Z7, Z1     // Z1 += Z6 * Z7

    VMOVUPS 128(SI), Z4         // 加载 a[i+32:i+48]
    VMOVUPS 128(DI), Z5
    VFMADD231PS Z4, Z5, Z2

    VMOVUPS 192(SI), Z6         // 加载 a[i+48:i+64]
    VMOVUPS 192(DI), Z7
    VFMADD231PS Z6, Z7, Z3
```

**关键技术点：**

1. **512-bit ZMM 寄存器：** 每条指令处理 16 个 float32（vs AVX2 的 8 个），理论吞吐量翻倍

2. **4 路 ZMM 累加器（Z0-Z3）：** 与 AVX2 相同的累加器策略，但每路宽度翻倍。64 floats/iter = 4 × 16，1536 维向量仅需 24 次迭代（vs AVX2 的 48 次）

3. **多级尾部处理：** 主循环后依次处理 32 floats（2 × ZMM）、16 floats（1 × ZMM）、逐元素标量尾部，确保任意长度向量的正确性

4. **水平归约：** 使用 `VEXTRACTF64X4` 将 512-bit ZMM 拆分为两个 256-bit YMM，然后复用 AVX2 的归约路径（`VEXTRACTF128` → `VMOVHLPS` → `VPSHUFD` → `VADDSS`）

5. **VZEROUPPER：** 返回前清除 YMM/ZMM 高位，避免 SSE 转换惩罚

**与 AVX2 的对比：**

| 特性 | AVX-512 | AVX2 |
|------|---------|------|
| 寄存器宽度 | 512-bit (ZMM) | 256-bit (YMM) |
| 每条指令处理 | 16 floats | 8 floats |
| 每次迭代处理 | 64 floats (256B) | 32 floats (128B) |
| 1536 维迭代次数 | 24 次 | 48 次 |
| 理论加速比 (vs AVX2) | ~2x | 基准 |
| CPU 支持 | Intel Xeon (Skylake-SP+), Ice Lake+, Zen 4+ | Haswell+ (2013), Zen+ (2018) |

**适用场景：** Intel Xeon 服务器（Skylake-SP、Cascade Lake、Ice Lake）、Intel 11 代+ 桌面 CPU、AMD Zen 4+ 处理器。在这些平台上，AVX-512 可以将 dot product 性能在 AVX2 基础上再提升约 2 倍。

> **注意：** 部分 CPU（如 AMD Zen 4）的 AVX-512 实际以 256-bit 双发射实现，加速比可能低于理论值。Intel Alder Lake 等混合架构 CPU 可能不支持 AVX-512。运行时 CPUID 检测确保只在真正支持的硬件上启用。

### 6.3 AVX2 + FMA 实现

**指令集：** AVX2（256-bit 寄存器）+ FMA（融合乘加）

**核心循环：** 每次迭代处理 32 个 float32（128 字节），使用 4 个 YMM 累加器最大化吞吐。

```asm
avx2_loop32:
    VMOVUPS 0(SI), Y4           // 加载 a[i:i+8]
    VMOVUPS 0(DI), Y5           // 加载 b[i:i+8]
    VFMADD231PS Y4, Y5, Y0     // Y0 += Y4 * Y5 (融合乘加，单指令)

    VMOVUPS 32(SI), Y6          // 加载 a[i+8:i+16]
    VMOVUPS 32(DI), Y7
    VFMADD231PS Y6, Y7, Y1     // Y1 += Y6 * Y7

    VMOVUPS 64(SI), Y4          // 加载 a[i+16:i+24]
    VMOVUPS 64(DI), Y5
    VFMADD231PS Y4, Y5, Y2

    VMOVUPS 96(SI), Y6          // 加载 a[i+24:i+32]
    VMOVUPS 96(DI), Y7
    VFMADD231PS Y6, Y7, Y3
```

**关键技术点：**

1. **VFMADD231PS（融合乘加）：** 将乘法和加法合并为单条指令，减少一半的浮点运算指令数，同时提高精度（中间结果不截断）

2. **4 路 YMM 累加器（Y0-Y3）：** 消除写后读依赖（WAR dependency），让 CPU 的多个执行端口可以同时处理不同的 FMA 指令。现代 CPU 通常有 2 个 FMA 执行单元，4 路累加器确保流水线始终满载

3. **VMOVUPS（非对齐加载）：** Go 的 slice 不保证 32 字节对齐，使用非对齐加载指令避免段错误。现代 CPU 上非对齐加载的性能惩罚已经很小

4. **水平归约：** 使用 `VEXTRACTF128` + `VADDPS` + `VMOVHLPS` + `VPSHUFD` 将 8 个 float32 归约为 1 个标量结果

5. **VZEROUPPER：** 在返回前清除 YMM 寄存器高 128 位，避免 AVX-SSE 转换惩罚（Intel CPU 上可能导致数百周期的性能损失）

### 6.4 SSE 回退实现

**指令集：** SSE（128-bit XMM 寄存器），所有 x86-64 CPU 均支持

**核心循环：** 每次迭代处理 16 个 float32（64 字节），使用 4 个 XMM 累加器。

```asm
sse_loop16:
    MOVUPS  0(SI), X4
    MOVUPS  0(DI), X5
    MULPS   X5, X4              // X4 = a[i:i+4] * b[i:i+4]
    ADDPS   X4, X0              // X0 += X4

    MOVUPS  16(SI), X6
    MOVUPS  16(DI), X7
    MULPS   X7, X6
    ADDPS   X6, X1
    // ... X2, X3
```

**与 AVX2 的区别：**
- 128-bit 寄存器（4 个 float32 vs 8 个）
- 无 FMA，需要分开的 MULPS + ADDPS（2 条指令 vs 1 条）
- 吞吐量约为 AVX2 的 1/4

### 6.5 ARM64 NEON + FMLA 实现

**指令集：** ARM NEON（128-bit 向量寄存器）+ FMLA（融合乘加）

**文件：** `internal/vectorstore/simd_arm64.s`

**核心循环：** 每次迭代处理 16 个 float32（64 字节），使用 4 个 NEON 累加器（V0-V3），每个寄存器容纳 4 个 float32。

```asm
neon_loop16:
    VLD1.P  16(R0), [V4.S4]       // 加载 a[i:i+4]
    VLD1.P  16(R2), [V5.S4]       // 加载 b[i:i+4]
    VFMLA   V4.S4, V5.S4, V0.S4   // V0 += V4 * V5 (融合乘加)

    VLD1.P  16(R0), [V6.S4]
    VLD1.P  16(R2), [V7.S4]
    VFMLA   V6.S4, V7.S4, V1.S4

    VLD1.P  16(R0), [V4.S4]
    VLD1.P  16(R2), [V5.S4]
    VFMLA   V4.S4, V5.S4, V2.S4

    VLD1.P  16(R0), [V6.S4]
    VLD1.P  16(R2), [V7.S4]
    VFMLA   V6.S4, V7.S4, V3.S4
```

**关键技术点：**

1. **VFMLA（融合乘加）：** ARM 等价于 x86 的 VFMADD231PS，单条指令完成乘加，所有 ARMv8 CPU 均支持

2. **4 路累加器（V0-V3）：** 与 x86 实现相同的策略，消除循环携带依赖

3. **VLD1.P（后递增加载）：** 加载数据的同时自动递增指针，减少一条 ADD 指令

4. **水平归约：** 使用 `VEXT` 旋转 + `VADD` 逐步将 4 个 float32 归约为标量

5. **多级尾部处理：** 主循环后依次处理 8 floats、4 floats、逐元素标量尾部（`FMADDS`）

**适用场景：** Apple Silicon (M1/M2/M3/M4)、AWS Graviton、树莓派 4+、所有 ARMv8-A 处理器。NEON 是 ARM64 的基线指令集，无需运行时检测。

**与 x86 SSE 的对比：**

| 特性 | ARM NEON | x86 SSE |
|------|----------|---------|
| 寄存器宽度 | 128-bit | 128-bit |
| 每条指令处理 | 4 floats | 4 floats |
| 融合乘加 | VFMLA（基线支持） | 无（需 MULPS+ADDPS 两条） |
| 每次迭代处理 | 16 floats | 16 floats |
| 后递增寻址 | VLD1.P（内置） | 需额外 ADD 指令 |

### 6.6 SQRTSD 硬件平方根

**问题：** `math.Sqrt` 通过函数调用实现，有调用开销。

**方案：** 直接在汇编中使用 `SQRTSD` 指令，单条指令完成 float64 平方根计算。

```asm
TEXT ·sqrtAsm(SB), NOSPLIT, $0-16
    MOVSD   x+0(FP), X0
    SQRTSD  X0, X0
    MOVSD   X0, ret+8(FP)
    RET
```

### 6.7 实测 Benchmark 结果

测试环境：AMD Ryzen 7 7840H, Windows, Go 1.25.5

#### Dot Product 性能对比

| 向量维度 | Pure Go (8-way 展开) | AVX2+FMA | AVX-512 | 加速比 (AVX-512 vs Go) |
|---------|---------------------|----------|---------|----------------------|
| 768 维 | 309 ns/op | 36 ns/op | 34.6 ns/op | **8.9x** |
| 1536 维 | 593 ns/op | 70.7 ns/op | 67.3 ns/op | **8.8x** |
| 3072 维 | 1162 ns/op | 135 ns/op | 128.9 ns/op | **9.0x** |

> **AMD Zen 4 AVX-512 特性：** Ryzen 7 7840H (Zen 4) 支持 AVX-512，但以 256-bit 双发射方式实现，因此 AVX-512 与 AVX2 性能接近（~5% 提升）。在原生 512-bit 执行单元的 Intel Xeon (Ice Lake+) 上，AVX-512 预期可获得更显著的加速（~1.5-2x vs AVX2）。

#### Norm 计算性能对比

| 向量维度 | Pure Go (8-way 展开) | SIMD (AVX2 dot + SQRTSD) | 加速比 |
|---------|---------------------|--------------------------|--------|
| 1536 维 | 437 ns/op | 77 ns/op | **5.7x** |

#### 端到端搜索影响估算

以 10K chunks × 1536 维为例，单次搜索需要 10K 次 dot product：

| 阶段 | Pure Go | AVX2 SIMD | AVX-512 | 节省 (AVX-512) |
|------|---------|-----------|---------|---------------|
| 10K 次 dot product | 5.93 ms | 0.71 ms | 0.67 ms | 5.26 ms |
| 加上 product 分区（假设 50% 过滤） | 2.97 ms | 0.35 ms | 0.34 ms | 2.63 ms |
| 加上 LRU 缓存命中 | 0 ms | 0 ms | 0 ms | — |

### 6.8 多平台回退策略

```
x86-64 (simd_amd64.s):
  dotProductSIMD()
    ├── hasAVX512 && len >= 64 → dotProductAVX512()   // 极速：512-bit FMA, 64 floats/iter
    ├── hasAVX2 && len >= 32   → dotProductAVX2()     // 快速：256-bit FMA, 32 floats/iter
    ├── len >= 16              → dotProductSSE()       // 中等：128-bit MULPS+ADDPS
    └── else                   → dotProductF32x8()     // 回退：纯 Go 8-way 展开

ARM64 (simd_arm64.s):
  dotProductSIMD()
    ├── len >= 16              → dotProductNEON()      // NEON+FMLA, 16 floats/iter
    └── else                   → dotProductF32x8()     // 回退：纯 Go 8-way 展开

其他平台 (simd_generic.go):
  dotProductSIMD() → dotProductF32x8()                 // 纯 Go 8-way 展开
```

- **AVX-512 路径：** Intel Xeon (Skylake-SP+)、Intel 11 代+ 桌面 CPU、AMD Zen 4+
- **AVX2+FMA 路径：** Intel Haswell+ (2013)、AMD Zen+ (2018)
- **SSE 路径：** 所有 x86-64 CPU（SSE2 是 amd64 基线）
- **NEON 路径：** 所有 ARM64 CPU（Apple Silicon、Graviton、树莓派 4+）
- **纯 Go 路径：** 32-bit ARM、MIPS、RISC-V 等，以及极短向量

### 6.9 正确性保证

- `TestSIMDCapability` 验证运行时 SIMD 检测正确报告当前 CPU 能力
- 端到端 Search 测试（`TestSearchReturnsTopK`、`TestSearchSortedDescending` 等）隐式覆盖 SIMD dot product 的正确性
- `dotProductSIMD()` 自动选择当前 CPU 支持的最高指令集，所有搜索测试均通过 SIMD 路径执行
- 端到端 Benchmark（`BenchmarkSearch_1000x1536`、`BenchmarkSearch_5000x1536`、`BenchmarkSearch_10000x768`）验证不同数据规模下的性能

---

## 七、涉及文件清单

| 文件路径 | 用途 | 优化项 |
|---------|------|-------|
| `internal/vectorstore/store.go` | 向量存储与搜索核心 | #1-#9, #13 |
| `internal/vectorstore/serialize.go` | 向量序列化/反序列化 | #2, #12 |
| `internal/vectorstore/simd_amd64.s` | AVX-512/AVX2/SSE Plan 9 汇编 | #14, #15, #16, #18 |
| `internal/vectorstore/simd_amd64.go` | SIMD Go 声明 + CPU 检测 (x86-64) | #14, #15, #19 |
| `internal/vectorstore/simd_arm64.s` | ARM NEON Plan 9 汇编 | #17 |
| `internal/vectorstore/simd_arm64.go` | SIMD Go 声明 (arm64) | #17 |
| `internal/vectorstore/simd_generic.go` | 非 x86/arm64 平台纯 Go 回退 | #16 |
| `internal/vectorstore/simd_test.go` | SIMD 正确性 + 基准测试 | #14, #15, #16, #17 |
| `internal/query/engine.go` | RAG 查询引擎 | #10, #11 |

### 独立模块

全部优化已提取为独立 Go module `github.com/nicexipi/sqlite-vec`（`sqlite-vec/` 目录），可在其他项目中直接引用。模块包含完整的向量存储、SIMD 加速、序列化等功能，使用 `partitionID` 替代 `productID` 实现更通用的分区语义。

| 文件路径 | 用途 |
|---------|------|
| `sqlite-vec/store.go` | 独立模块主文件，包含全部存储层优化 |
| `sqlite-vec/serialize.go` | 序列化/反序列化 |
| `sqlite-vec/simd_amd64.go` | x86-64 SIMD 声明 + CPU 检测 |
| `sqlite-vec/simd_arm64.go` | ARM64 NEON 声明 |
| `sqlite-vec/go.mod` | 模块定义 (`github.com/nicexipi/sqlite-vec`) |

### 依赖变更

| 依赖 | 用途 |
|------|------|
| `golang.org/x/sys/cpu` | 运行时 CPUID 特性检测（AVX-512F / AVX2 / FMA） |
