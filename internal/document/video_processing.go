// Package document — video processing pipeline with concurrent phases.
package document

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"askflow/internal/errlog"
	"askflow/internal/vectorstore"
	"askflow/internal/video"
)

// videoOCRResult holds the LLM OCR+description result for a single keyframe.
type videoOCRResult struct {
	frameIndex int
	text       string
	timestamp  float64
}

// processVideo handles video file processing with three concurrent phases:
//   - Phase 1: ASR transcript → chunk → embed → store
//   - Phase 2: Keyframe image embedding (worker pool)
//   - Phase 3: LLM keyframe OCR + scene description (worker pool with per-frame timeout)
//
// Each phase is independent and fault-tolerant: one phase failing does not block others.
func (dm *DocumentManager) processVideo(docID, docName string, fileData []byte, productID string) error {
	dm.mu.RLock()
	cfg := dm.videoConfig
	dm.mu.RUnlock()

	// Locate or save the video file
	uploadDir := filepath.Join(".", "data", "uploads", docID)
	videoPath := dm.findSavedFile(uploadDir)
	if videoPath == "" {
		if err := os.MkdirAll(uploadDir, 0755); err != nil {
			return fmt.Errorf("创建上传目录失败: %w", err)
		}
		safeName := sanitizeFilename(docName, docID)
		videoPath = filepath.Join(uploadDir, safeName)
		if err := os.WriteFile(videoPath, fileData, 0644); err != nil {
			return fmt.Errorf("保存视频文件失败: %w", err)
		}
	}

	if cfg.FFmpegPath == "" && cfg.RapidSpeechPath == "" {
		log.Printf("视频检索工具未配置，仅存储文件名作为可搜索文本: %s", docName)
		fallbackText := fmt.Sprintf("视频文件: %s", docName)
		if err := dm.chunkEmbedStore(docID, docName, fallbackText, productID); err != nil {
			return fmt.Errorf("存储视频文件名向量失败: %w", err)
		}
		return nil
	}

	vp := video.NewParser(cfg)
	parseResult, err := vp.Parse(videoPath)
	if err != nil {
		errlog.Logf("[Video] parse failed doc=%s file=%q: %v", docID, docName, err)
		return fmt.Errorf("视频解析失败: %w", err)
	}

	log.Printf("视频解析完成 doc=%s: %d 段转录, %d 个关键帧", docID, len(parseResult.Transcript), len(parseResult.Keyframes))

	// Pre-compute which keyframes need OCR before any phase starts
	ocrEnabled := cfg.KeyframeOCREnabled
	ocrMaxFrames := cfg.KeyframeOCRMaxFrames
	if ocrMaxFrames <= 0 {
		ocrMaxFrames = 20
	}
	ocrIndices := make(map[int]bool)
	if ocrEnabled && len(parseResult.Keyframes) > 0 {
		dm.mu.RLock()
		hasLLM := dm.llmService != nil
		dm.mu.RUnlock()
		if hasLLM {
			total := len(parseResult.Keyframes)
			if total <= ocrMaxFrames {
				for i := 0; i < total; i++ {
					ocrIndices[i] = true
				}
			} else if ocrMaxFrames == 1 {
				// Special case: only sample the middle frame
				ocrIndices[total/2] = true
			} else {
				for j := 0; j < ocrMaxFrames; j++ {
					idx := j * (total - 1) / (ocrMaxFrames - 1)
					ocrIndices[idx] = true
				}
			}
		}
	}

	// ── Phase 1: Transcript (ASR) — runs concurrently ──
	type transcriptResult struct {
		chunkCount int
		err        error
	}
	transcriptCh := make(chan transcriptResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				transcriptCh <- transcriptResult{err: fmt.Errorf("转录处理panic: %v", r)}
			}
		}()
		count, tErr := dm.processTranscript(docID, docName, productID, parseResult)
		transcriptCh <- transcriptResult{chunkCount: count, err: tErr}
	}()

	// ── Phase 2: Keyframe embedding — concurrent worker pool ──
	type keyframeEmbedResult struct {
		storedCount int
		err         error
	}
	keyframeEmbedCh := make(chan keyframeEmbedResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				keyframeEmbedCh <- keyframeEmbedResult{err: fmt.Errorf("关键帧embedding panic: %v", r)}
			}
		}()
		count, kErr := dm.processKeyframeEmbeddings(docID, docName, productID, parseResult.Keyframes)
		keyframeEmbedCh <- keyframeEmbedResult{storedCount: count, err: kErr}
	}()

	// ── Phase 3: LLM keyframe OCR + scene description — concurrent worker pool ──
	var ocrResults []videoOCRResult
	if len(ocrIndices) > 0 {
		ocrResults = dm.processKeyframeDescriptions(docID, parseResult.Keyframes, ocrIndices)
	}

	// ── Collect results from all phases ──
	tResult := <-transcriptCh
	chunkIndex := 0
	if tResult.err != nil {
		log.Printf("Warning: 转录处理失败 doc=%s: %v", docID, tResult.err)
		errlog.Logf("[Video] transcript failed for doc=%s: %v", docID, tResult.err)
	} else {
		chunkIndex = tResult.chunkCount
	}

	kResult := <-keyframeEmbedCh
	if kResult.err != nil {
		log.Printf("Warning: 关键帧embedding失败 doc=%s: %v", docID, kResult.err)
		errlog.Logf("[Video] keyframe embedding failed for doc=%s: %v", docID, kResult.err)
	}

	// Release keyframe image data to free memory (all phases have completed)
	for i := range parseResult.Keyframes {
		parseResult.Keyframes[i].Data = nil
	}

	// ── Store OCR+description texts as searchable chunks ──
	// Use offset 20000+ for OCR description chunks to avoid collision with
	// transcript chunks (0..N) and keyframe embedding chunks (10000+i)
	if len(ocrResults) > 0 {
		dm.storeKeyframeDescriptions(docID, docName, productID, ocrResults, 20000, len(ocrIndices))
	}

	// Fallback: if nothing was stored at all, store filename as searchable text
	if chunkIndex == 0 && kResult.storedCount == 0 && len(ocrResults) == 0 {
		log.Printf("视频 %s 未提取到任何可检索内容，存储文件名作为可搜索文本", docID)
		fallbackText := fmt.Sprintf("视频文件: %s", docName)
		if err := dm.chunkEmbedStore(docID, docName, fallbackText, productID); err != nil {
			return fmt.Errorf("存储视频文件名向量失败: %w", err)
		}
	}

	return nil
}

// processTranscript handles ASR transcript: join → chunk → embed → store → create video_segments.
// Returns the number of chunks stored.
func (dm *DocumentManager) processTranscript(docID, docName, productID string, parseResult *video.ParseResult) (int, error) {
	if len(parseResult.Transcript) == 0 {
		return 0, nil
	}

	var fullText strings.Builder
	for _, seg := range parseResult.Transcript {
		if fullText.Len() > 0 {
			fullText.WriteString(" ")
		}
		fullText.WriteString(strings.TrimSpace(seg.Text))
	}
	if fullText.Len() == 0 {
		return 0, nil
	}

	chunks := dm.chunker.Split(fullText.String(), docID)
	if len(chunks) == 0 {
		return 0, nil
	}

	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	embeddings, err := dm.embeddingService.EmbedBatch(texts)
	if err != nil {
		errlog.Logf("[Video] transcript embedding failed doc=%s file=%q: %v", docID, docName, err)
		return 0, fmt.Errorf("转录文本嵌入失败: %w", err)
	}

	vectorChunks := make([]vectorstore.VectorChunk, len(chunks))
	for i, c := range chunks {
		vectorChunks[i] = vectorstore.VectorChunk{
			ChunkText:    c.Text,
			ChunkIndex:   i,
			DocumentID:   docID,
			DocumentName: docName,
			Vector:       embeddings[i],
			ProductID:    productID,
		}
	}
	if err := dm.vectorStore.Store(docID, vectorChunks); err != nil {
		errlog.Logf("[Video] transcript store failed doc=%s file=%q: %v", docID, docName, err)
		return 0, fmt.Errorf("转录向量存储失败: %w", err)
	}

	// Create video_segments records
	segTx, txErr := dm.db.Begin()
	if txErr != nil {
		return len(chunks), fmt.Errorf("开始 video_segments 事务失败: %w", txErr)
	}
	defer segTx.Rollback()

	stmt, stmtErr := segTx.Prepare(
		`INSERT INTO video_segments (id, document_id, segment_type, start_time, end_time, content, chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
	)
	if stmtErr != nil {
		return len(chunks), fmt.Errorf("准备 video_segments 语句失败: %w", stmtErr)
	}
	defer stmt.Close()

	for i, c := range chunks {
		startTime, endTime := dm.mapChunkToTimeRange(c.Text, parseResult.Transcript)
		segID, err := generateID()
		if err != nil {
			log.Printf("Warning: 生成 segment ID 失败: %v", err)
			continue
		}
		chunkID := fmt.Sprintf("%s-%d", docID, i)
		if _, err := stmt.Exec(segID, docID, "transcript", startTime, endTime, c.Text, chunkID); err != nil {
			log.Printf("Warning: 插入 video_segments 记录失败: %v", err)
		}
	}

	if err := segTx.Commit(); err != nil {
		return len(chunks), fmt.Errorf("提交 video_segments 事务失败: %w", err)
	}

	return len(chunks), nil
}

// processKeyframeEmbeddings embeds keyframe images concurrently using a worker pool.
// Each frame has a per-frame timeout. Returns the number of successfully stored keyframes.
func (dm *DocumentManager) processKeyframeEmbeddings(docID, docName, productID string, keyframes []video.Keyframe) (int, error) {
	if len(keyframes) == 0 {
		return 0, nil
	}

	type embedJob struct {
		index    int
		keyframe video.Keyframe
	}
	type embedResult struct {
		index int
		ok    bool
	}

	const maxWorkers = 4
	jobs := make(chan embedJob, len(keyframes))
	results := make(chan embedResult, len(keyframes))

	workerCount := maxWorkers
	if len(keyframes) < workerCount {
		workerCount = len(keyframes)
	}

	var wg sync.WaitGroup
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				ok := dm.embedSingleKeyframe(docID, docName, productID, job.index, job.keyframe)
				results <- embedResult{index: job.index, ok: ok}
			}
		}()
	}

	for i, kf := range keyframes {
		jobs <- embedJob{index: i, keyframe: kf}
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	storedCount := 0
	for res := range results {
		if res.ok {
			storedCount++
		}
	}
	log.Printf("关键帧embedding完成 doc=%s: %d/%d 帧成功", docID, storedCount, len(keyframes))
	return storedCount, nil
}

// embedSingleKeyframe embeds one keyframe image with a per-frame timeout,
// stores the vector, and creates a video_segments record. Returns true on success.
func (dm *DocumentManager) embedSingleKeyframe(docID, docName, productID string, i int, kf video.Keyframe) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Warning: keyframe %d embedding panic: %v", i, r)
			errlog.Logf("[Video] keyframe %d embed panic doc=%s file=%q: %v", i, docID, docName, r)
			ok = false
		}
	}()

	if len(kf.Data) == 0 {
		return false
	}

	dataURL := imageToBase64DataURL(resizeImageForEmbedding(kf.Data))

	// Per-frame timeout for embedding API call
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	type embedResp struct {
		vec []float64
		err error
	}
	ch := make(chan embedResp, 1)
	go func() {
		vec, err := dm.embeddingService.EmbedImageURL(dataURL)
		ch <- embedResp{vec, err}
	}()

	var vec []float64
	select {
	case resp := <-ch:
		if resp.err != nil {
			log.Printf("Warning: failed to embed keyframe %d (%.1fs): %v", i, kf.Timestamp, resp.err)
			errlog.Logf("[Video] keyframe %d embed failed doc=%s file=%q: %v", i, docID, docName, resp.err)
			return false
		}
		vec = resp.vec
	case <-ctx.Done():
		log.Printf("Warning: keyframe %d embedding timeout (%.1fs)", i, kf.Timestamp)
		errlog.Logf("[Video] keyframe %d embed timeout doc=%s file=%q", i, docID, docName)
		return false
	}

	// Save keyframe image to disk instead of storing large base64 in vector store
	savedURL, saveErr := dm.saveExtractedImage(kf.Data)
	imageURL := savedURL
	if saveErr != nil {
		log.Printf("Warning: failed to save keyframe %d image to disk: %v", i, saveErr)
		// Fallback: use file path if available, otherwise skip image URL
		if kf.FilePath != "" {
			imageURL = kf.FilePath
		} else {
			imageURL = ""
		}
	}

	// Use a large offset for keyframe chunk indices to avoid collision with transcript
	frameChunkIndex := 10000 + i
	frameChunk := []vectorstore.VectorChunk{{
		ChunkText:    fmt.Sprintf("[视频关键帧: %.1fs]", kf.Timestamp),
		ChunkIndex:   frameChunkIndex,
		DocumentID:   docID,
		DocumentName: docName,
		Vector:       vec,
		ImageURL:     imageURL,
		ProductID:    productID,
	}}
	if err := dm.vectorStore.Store(docID, frameChunk); err != nil {
		log.Printf("Warning: failed to store keyframe vector %d: %v", i, err)
		errlog.Logf("[Video] keyframe %d store failed doc=%s file=%q: %v", i, docID, docName, err)
		return false
	}

	segID, err := generateID()
	if err != nil {
		log.Printf("Warning: failed to generate segment ID for keyframe %d: %v", i, err)
		return true // vector already stored, segment record is non-critical
	}
	chunkID := fmt.Sprintf("%s-%d", docID, frameChunkIndex)
	_, dbErr := dm.db.Exec(
		`INSERT INTO video_segments (id, document_id, segment_type, start_time, end_time, content, chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		segID, docID, "keyframe", kf.Timestamp, kf.Timestamp, kf.FilePath, chunkID,
	)
	if dbErr != nil {
		log.Printf("Warning: failed to insert keyframe video_segment %d: %v", i, dbErr)
	}
	return true
}

// processKeyframeDescriptions runs LLM OCR+scene description on sampled keyframes
// concurrently with a worker pool and per-frame timeout. Returns collected results
// sorted by frame index for deterministic output.
func (dm *DocumentManager) processKeyframeDescriptions(docID string, keyframes []video.Keyframe, ocrIndices map[int]bool) []videoOCRResult {
	type descJob struct {
		index    int
		keyframe video.Keyframe
	}
	var jobList []descJob
	for i, kf := range keyframes {
		if ocrIndices[i] && len(kf.Data) > 0 {
			jobList = append(jobList, descJob{index: i, keyframe: kf})
		}
	}
	if len(jobList) == 0 {
		return nil
	}

	const maxWorkers = 3
	jobs := make(chan descJob, len(jobList))
	resultsCh := make(chan videoOCRResult, len(jobList))

	workerCount := maxWorkers
	if len(jobList) < workerCount {
		workerCount = len(jobList)
	}

	var wg sync.WaitGroup
	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				dm.describeSingleKeyframe(docID, job.index, job.keyframe, resultsCh)
			}
		}()
	}

	for _, j := range jobList {
		jobs <- j
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(resultsCh)
	}()

	var collected []videoOCRResult
	for r := range resultsCh {
		collected = append(collected, r)
	}
	sort.Slice(collected, func(i, j int) bool {
		return collected[i].frameIndex < collected[j].frameIndex
	})
	return collected
}

// describeSingleKeyframe calls LLM vision API for one keyframe with a per-frame
// timeout and panic recovery. Sends result to ch on success.
func (dm *DocumentManager) describeSingleKeyframe(docID string, i int, kf video.Keyframe, ch chan<- videoOCRResult) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Warning: keyframe %d LLM描述 panic: %v", i, r)
			errlog.Logf("[Video OCR] keyframe %d panic for doc=%s: %v", i, docID, r)
		}
	}()

	// Per-frame timeout: 3 minutes for LLM vision call
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	type llmResp struct {
		text string
		err  error
	}
	llmCh := make(chan llmResp, 1)
	go func() {
		text, err := dm.describeKeyframeViaLLM(kf.Data)
		llmCh <- llmResp{text, err}
	}()

	select {
	case res := <-llmCh:
		if res.err != nil {
			log.Printf("Warning: keyframe %d OCR+描述失败 (%.1fs): %v", i, kf.Timestamp, res.err)
			errlog.Logf("[Video OCR] keyframe %d failed for doc=%s: %v", i, docID, res.err)
		} else if res.text != "" {
			ch <- videoOCRResult{
				frameIndex: i,
				text:       res.text,
				timestamp:  kf.Timestamp,
			}
		}
	case <-ctx.Done():
		log.Printf("Warning: keyframe %d LLM描述超时 (%.1fs)", i, kf.Timestamp)
		errlog.Logf("[Video OCR] keyframe %d timed out for doc=%s", i, docID)
	}
}

// storeKeyframeDescriptions combines OCR+description results into text chunks,
// embeds them, and stores as searchable vectors.
func (dm *DocumentManager) storeKeyframeDescriptions(docID, docName, productID string, results []videoOCRResult, chunkBase, totalOCRFrames int) {
	log.Printf("视频关键帧OCR+场景描述完成: doc=%s, %d/%d 帧提取到内容", docID, len(results), totalOCRFrames)

	var sb strings.Builder
	for _, r := range results {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(fmt.Sprintf("[视频 %.0f秒] %s", r.timestamp, r.text))
	}

	ocrChunks := dm.chunker.Split(sb.String(), docID)
	if len(ocrChunks) == 0 {
		return
	}

	ocrTexts := make([]string, len(ocrChunks))
	for i, c := range ocrChunks {
		ocrTexts[i] = c.Text
	}
	ocrEmbeddings, embErr := dm.embeddingService.EmbedBatch(ocrTexts)
	if embErr != nil {
		log.Printf("Warning: OCR text embedding failed for doc=%s: %v", docID, embErr)
		errlog.Logf("[Video OCR] embedding failed for doc=%s: %v", docID, embErr)
		return
	}

	ocrVectorChunks := make([]vectorstore.VectorChunk, len(ocrChunks))
	for i, c := range ocrChunks {
		ocrVectorChunks[i] = vectorstore.VectorChunk{
			ChunkText:    c.Text,
			ChunkIndex:   chunkBase + i,
			DocumentID:   docID,
			DocumentName: docName,
			Vector:       ocrEmbeddings[i],
			ProductID:    productID,
		}
	}
	if storeErr := dm.vectorStore.Store(docID, ocrVectorChunks); storeErr != nil {
		log.Printf("Warning: OCR vector store failed for doc=%s: %v", docID, storeErr)
		errlog.Logf("[Video OCR] vector store failed for doc=%s: %v", docID, storeErr)
	} else {
		log.Printf("视频关键帧OCR+描述文本已存储: doc=%s, %d 个分块", docID, len(ocrVectorChunks))
	}
}
