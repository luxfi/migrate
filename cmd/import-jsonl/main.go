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
	"path/filepath"
	"time"
)

// BlockData represents a block from JSONL export
type BlockData struct {
	Height       uint64                      `json:"height"`
	Hash         string                      `json:"hash"`
	Header       string                      `json:"header"`
	Body         string                      `json:"body"`
	Receipts     string                      `json:"receipts"`
	StateChanges map[string]*ImportAccountState `json:"stateChanges,omitempty"`
}

// ImportAccountState represents account state for import
type ImportAccountState struct {
	Balance  string            `json:"balance"`
	Nonce    uint64            `json:"nonce"`
	Code     string            `json:"code,omitempty"`
	Storage  map[string]string `json:"storage,omitempty"`
	CodeHash string            `json:"code_hash,omitempty"`
}

// ImportBlockEntry represents a block for the migrate API
type ImportBlockEntry struct {
	Height       uint64                         `json:"height"`
	Hash         string                         `json:"hash"`
	Header       string                         `json:"header"`
	Body         string                         `json:"body"`
	Receipts     string                         `json:"receipts"`
	StateChanges map[string]*ImportAccountState `json:"stateChanges,omitempty"`
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

func main() {
	rpcURL := flag.String("rpc", "http://127.0.0.1:9630/ext/bc/C/rpc", "C-Chain RPC URL")
	inputPath := flag.String("input", "", "Path to JSONL file or directory of part files")
	batchSize := flag.Int("batch", 100, "Number of blocks per batch")
	startBlock := flag.Uint64("start", 0, "Start block (0 = from beginning)")
	endBlock := flag.Uint64("end", 0, "End block (0 = to end)")
	flag.Parse()

	if *inputPath == "" {
		log.Fatal("Usage: import-jsonl -input <jsonl-file-or-dir> [-rpc <url>] [-batch <size>] [-start <n>] [-end <n>]")
	}

	// Check if input is a directory or file
	info, err := os.Stat(*inputPath)
	if err != nil {
		log.Fatalf("Failed to stat input path: %v", err)
	}

	var files []string
	if info.IsDir() {
		// Directory mode - find all part files
		entries, err := os.ReadDir(*inputPath)
		if err != nil {
			log.Fatalf("Failed to read directory: %v", err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			// Match blocks-part-* or blocks.jsonl
			if filepath.Ext(name) == ".jsonl" || (len(name) > 12 && name[:12] == "blocks-part-") {
				files = append(files, filepath.Join(*inputPath, name))
			}
		}

		if len(files) == 0 {
			log.Fatal("No block files found in directory")
		}

		log.Printf("Found %d block files in directory\n", len(files))
	} else {
		// Single file mode
		files = []string{*inputPath}
	}

	totalImported := 0
	totalFailed := 0
	startTime := time.Now()

	for _, file := range files {
		log.Printf("Processing file: %s\n", file)

		imported, failed, err := processFile(file, *rpcURL, *batchSize, *startBlock, *endBlock)
		if err != nil {
			log.Printf("Error processing file %s: %v\n", file, err)
			continue
		}

		totalImported += imported
		totalFailed += failed
	}

	duration := time.Since(startTime)
	log.Printf("\n=== Import Complete ===\n")
	log.Printf("Total Imported: %d\n", totalImported)
	log.Printf("Total Failed: %d\n", totalFailed)
	log.Printf("Duration: %s\n", duration)
	log.Printf("Rate: %.2f blocks/sec\n", float64(totalImported)/duration.Seconds())
}

func processFile(filename, rpcURL string, batchSize int, startBlock, endBlock uint64) (int, int, error) {
	file, err := os.Open(filename)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	totalImported := 0
	totalFailed := 0
	scanner := bufio.NewScanner(file)

	// Increase buffer size for large blocks
	const maxSize = 100 * 1024 * 1024 // 100MB
	buf := make([]byte, maxSize)
	scanner.Buffer(buf, maxSize)

	batch := []ImportBlockEntry{}
	lineNum := 0

	for scanner.Scan() {
		lineNum++

		var block BlockData
		if err := json.Unmarshal(scanner.Bytes(), &block); err != nil {
			log.Printf("Line %d: Failed to parse JSON: %v\n", lineNum, err)
			continue
		}

		// Skip if outside range
		if startBlock > 0 && block.Height < startBlock {
			continue
		}
		if endBlock > 0 && block.Height > endBlock {
			break
		}

		// Convert to import format (add 0x prefix if missing)
		entry := ImportBlockEntry{
			Height:       block.Height,
			Hash:         addHexPrefix(block.Hash),
			Header:       addHexPrefix(block.Header),
			Body:         addHexPrefix(block.Body),
			Receipts:     addHexPrefix(block.Receipts),
			StateChanges: block.StateChanges,
		}

		batch = append(batch, entry)

		// Send batch when full
		if len(batch) >= batchSize {
			imported, failed, err := sendBatch(rpcURL, batch)
			if err != nil {
				return totalImported, totalFailed, fmt.Errorf("failed to send batch: %w", err)
			}
			totalImported += imported
			totalFailed += failed

			log.Printf("Progress: blocks %d-%d, imported: %d, failed: %d\n",
				batch[0].Height, batch[len(batch)-1].Height, imported, failed)

			batch = batch[:0] // Clear batch
		}
	}

	// Send remaining blocks
	if len(batch) > 0 {
		imported, failed, err := sendBatch(rpcURL, batch)
		if err != nil {
			return totalImported, totalFailed, fmt.Errorf("failed to send final batch: %w", err)
		}
		totalImported += imported
		totalFailed += failed

		log.Printf("Final batch: blocks %d-%d, imported: %d, failed: %d\n",
			batch[0].Height, batch[len(batch)-1].Height, imported, failed)
	}

	if err := scanner.Err(); err != nil {
		return totalImported, totalFailed, fmt.Errorf("scanner error: %w", err)
	}

	return totalImported, totalFailed, nil
}

func sendBatch(rpcURL string, blocks []ImportBlockEntry) (int, int, error) {
	// Create RPC request
	req := RPCRequest{
		JSONRPC: "2.0",
		Method:  "migrate_importBlocks",
		Params:  []interface{}{blocks},
		ID:      1,
	}

	body, err := json.Marshal(req)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Send HTTP request
	httpResp, err := http.Post(rpcURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, 0, fmt.Errorf("failed to send HTTP request: %w", err)
	}
	defer httpResp.Body.Close()

	// Read response
	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return 0, 0, fmt.Errorf("failed to read response: %w", err)
	}

	// Parse RPC response
	var rpcResp RPCResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return 0, 0, fmt.Errorf("failed to parse response: %w", err)
	}

	if rpcResp.Error != nil {
		return 0, 0, fmt.Errorf("RPC error: %s", rpcResp.Error.Message)
	}

	// Parse import response
	var importResp ImportBlocksResponse
	if err := json.Unmarshal(rpcResp.Result, &importResp); err != nil {
		return 0, 0, fmt.Errorf("failed to parse import response: %w", err)
	}

	// Log any errors from the import
	if len(importResp.Errors) > 0 {
		for _, errMsg := range importResp.Errors {
			log.Printf("Import error: %s\n", errMsg)
		}
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
