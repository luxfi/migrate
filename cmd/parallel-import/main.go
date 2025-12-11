package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// BlockData represents a block from JSONL export
type BlockData struct {
	Number      uint64 `json:"number"`
	Hash        string `json:"hash"`
	HeaderRLP   string `json:"header_rlp"`
	BodyRLP     string `json:"body_rlp"`
	ReceiptsRLP string `json:"receipts_rlp"`
}

// ImportBlockEntry represents a block for the migrate API
type ImportBlockEntry struct {
	Height   uint64 `json:"height"`
	Hash     string `json:"hash"`
	Header   string `json:"header"`
	Body     string `json:"body"`
	Receipts string `json:"receipts"`
}

// ImportBlocksResponse represents the API response
type ImportBlocksResponse struct {
	Imported      int      `json:"imported"`
	Failed        int      `json:"failed"`
	FirstHeight   uint64   `json:"firstHeight"`
	LastHeight    uint64   `json:"lastHeight"`
	Errors        []string `json:"errors,omitempty"`
	HeadBlockHash string   `json:"headBlockHash"`
}

// RPC request/response structures
type RPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error,omitempty"`
	ID      int             `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var (
	totalImported int64
	totalFailed   int64
	httpClient    *http.Client
)

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:9630/ext/bc/C/rpc", "C-Chain RPC URL")
	inputPath := flag.String("input", "", "Path to JSONL file")
	batchSize := flag.Int("batch", 500, "Number of blocks per batch")
	workers := flag.Int("workers", 8, "Number of parallel workers")
	startBlock := flag.Uint64("start", 0, "Start block (0 = from beginning)")
	endBlock := flag.Uint64("end", 0, "End block (0 = to end)")
	flag.Parse()

	if *inputPath == "" {
		log.Fatal("Usage: parallel-import -input <jsonl-file> [-rpc <url>] [-batch <size>] [-workers <n>]")
	}

	// Create HTTP client with connection pooling
	httpClient = &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 100,
			IdleConnTimeout:     90 * time.Second,
		},
		Timeout: 120 * time.Second,
	}

	// Read all blocks into memory first for parallel processing
	log.Printf("Loading blocks from %s...\n", *inputPath)
	blocks, err := loadBlocks(*inputPath, *startBlock, *endBlock)
	if err != nil {
		log.Fatalf("Failed to load blocks: %v", err)
	}
	log.Printf("Loaded %d blocks\n", len(blocks))

	if len(blocks) == 0 {
		log.Println("No blocks to import")
		return
	}

	// Create batches
	batches := createBatches(blocks, *batchSize)
	log.Printf("Created %d batches of up to %d blocks each\n", len(batches), *batchSize)

	// Create work channel
	work := make(chan []ImportBlockEntry, len(batches))
	for _, batch := range batches {
		work <- batch
	}
	close(work)

	// Start workers
	var wg sync.WaitGroup
	startTime := time.Now()

	// Progress reporter
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				imported := atomic.LoadInt64(&totalImported)
				failed := atomic.LoadInt64(&totalFailed)
				elapsed := time.Since(startTime).Seconds()
				rate := float64(imported) / elapsed
				remaining := float64(len(blocks)) - float64(imported) - float64(failed)
				eta := time.Duration(remaining/rate) * time.Second
				log.Printf("Progress: %d imported, %d failed, %.0f blocks/sec, ETA: %s\n",
					imported, failed, rate, eta.Round(time.Second))
			case <-done:
				return
			}
		}
	}()

	log.Printf("Starting %d workers...\n", *workers)
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go worker(&wg, *rpcURL, work)
	}

	wg.Wait()
	close(done)

	duration := time.Since(startTime)
	log.Printf("\n=== Import Complete ===\n")
	log.Printf("Total Imported: %d\n", atomic.LoadInt64(&totalImported))
	log.Printf("Total Failed: %d\n", atomic.LoadInt64(&totalFailed))
	log.Printf("Duration: %s\n", duration)
	log.Printf("Rate: %.2f blocks/sec\n", float64(atomic.LoadInt64(&totalImported))/duration.Seconds())
}

func loadBlocks(filename string, startBlock, endBlock uint64) ([]ImportBlockEntry, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var blocks []ImportBlockEntry
	scanner := bufio.NewScanner(file)

	// Increase buffer for large blocks
	const maxSize = 100 * 1024 * 1024
	buf := make([]byte, maxSize)
	scanner.Buffer(buf, maxSize)

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum%100000 == 0 {
			log.Printf("Reading line %d...\n", lineNum)
		}

		var block BlockData
		if err := json.Unmarshal(scanner.Bytes(), &block); err != nil {
			log.Printf("Line %d: parse error: %v\n", lineNum, err)
			continue
		}

		if startBlock > 0 && block.Number < startBlock {
			continue
		}
		if endBlock > 0 && block.Number > endBlock {
			break
		}

		blocks = append(blocks, ImportBlockEntry{
			Height:   block.Number,
			Hash:     addHexPrefix(block.Hash),
			Header:   addHexPrefix(block.HeaderRLP),
			Body:     addHexPrefix(block.BodyRLP),
			Receipts: addHexPrefix(block.ReceiptsRLP),
		})
	}

	return blocks, scanner.Err()
}

func createBatches(blocks []ImportBlockEntry, batchSize int) [][]ImportBlockEntry {
	var batches [][]ImportBlockEntry
	for i := 0; i < len(blocks); i += batchSize {
		end := i + batchSize
		if end > len(blocks) {
			end = len(blocks)
		}
		batches = append(batches, blocks[i:end])
	}
	return batches
}

func worker(wg *sync.WaitGroup, rpcURL string, work <-chan []ImportBlockEntry) {
	defer wg.Done()

	for batch := range work {
		imported, failed, err := sendBatch(rpcURL, batch)
		if err != nil {
			log.Printf("Batch %d-%d error: %v\n", batch[0].Height, batch[len(batch)-1].Height, err)
			atomic.AddInt64(&totalFailed, int64(len(batch)))
			continue
		}
		atomic.AddInt64(&totalImported, int64(imported))
		atomic.AddInt64(&totalFailed, int64(failed))
	}
}

func sendBatch(rpcURL string, blocks []ImportBlockEntry) (int, int, error) {
	req := RPCRequest{
		JSONRPC: "2.0",
		Method:  "migrate_importBlocks",
		Params:  []interface{}{blocks},
		ID:      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return 0, 0, fmt.Errorf("marshal: %w", err)
	}

	httpReq, err := http.NewRequest("POST", rpcURL, bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return 0, 0, fmt.Errorf("http: %w", err)
	}
	defer httpResp.Body.Close()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("read response: %w", err)
	}

	var rpcResp RPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return 0, 0, fmt.Errorf("parse response: %w (body: %s)", err, string(respBody[:min(200, len(respBody))]))
	}

	if rpcResp.Error != nil {
		return 0, 0, fmt.Errorf("RPC error: %s", rpcResp.Error.Message)
	}

	var importResp ImportBlocksResponse
	if err := json.Unmarshal(rpcResp.Result, &importResp); err != nil {
		return 0, 0, fmt.Errorf("parse import response: %w", err)
	}

	return importResp.Imported, importResp.Failed, nil
}

func addHexPrefix(s string) string {
	if s == "" {
		return "0x"
	}
	if len(s) >= 2 && s[:2] == "0x" {
		return s
	}
	return "0x" + s
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
