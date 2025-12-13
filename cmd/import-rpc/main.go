// import-rpc imports JSONL blocks via RPC to C-Chain migrate_importBlocks
// Supports both legacy format (PascalCase + base64) and canonical format (camelCase + hex)
package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/luxfi/migrate"
)

// ImportBlockEntry is the format expected by migrate_importBlocks RPC (lowercase, hex)
type ImportBlockEntry struct {
	Height       uint64                 `json:"height"`
	Hash         string                 `json:"hash"`
	Header       string                 `json:"header"`              // hex-encoded RLP
	Body         string                 `json:"body"`                // hex-encoded RLP
	Receipts     string                 `json:"receipts"`            // hex-encoded RLP
	StateChanges map[string]*StateEntry `json:"stateChanges,omitempty"`
}

// StateEntry is the state format for migrate_importBlocks RPC
type StateEntry struct {
	Balance string            `json:"balance,omitempty"`
	Nonce   uint64            `json:"nonce,omitempty"`
	Code    string            `json:"code,omitempty"`
	Storage map[string]string `json:"storage,omitempty"`
}

// RPCRequest represents a JSON-RPC request
type RPCRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

// RPCResponse represents a JSON-RPC response
type RPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError represents a JSON-RPC error
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ImportBlocksResponse is the response from migrate_importBlocks
type ImportBlocksResponse struct {
	Imported int      `json:"imported"`
	Failed   int      `json:"failed"`
	Errors   []string `json:"errors,omitempty"`
}

// ensureHexPrefix ensures the string has 0x prefix
func ensureHexPrefix(s string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		return s
	}
	return "0x" + s
}

// convertBlockData converts migrate.BlockData to ImportBlockEntry for the RPC
// BlockData.Header/Body/Receipts are already []byte (HexBytes type handles JSON encoding)
func convertBlockData(block *migrate.BlockData) *ImportBlockEntry {
	entry := &ImportBlockEntry{
		Height:   block.Number,
		Hash:     block.Hash.Hex(),
		Header:   "0x" + hex.EncodeToString(block.Header),
		Body:     "0x" + hex.EncodeToString(block.Body),
		Receipts: "0x" + hex.EncodeToString(block.Receipts),
	}

	// Convert state changes if present
	if len(block.StateChanges) > 0 {
		entry.StateChanges = make(map[string]*StateEntry)
		for addr, account := range block.StateChanges {
			se := &StateEntry{
				Nonce: account.Nonce,
			}
			if account.Balance != nil {
				se.Balance = "0x" + account.Balance.Text(16)
			}
			if len(account.Code) > 0 {
				se.Code = "0x" + hex.EncodeToString(account.Code)
			}
			if len(account.Storage) > 0 {
				se.Storage = make(map[string]string)
				for k, v := range account.Storage {
					se.Storage[k.Hex()] = v.Hex()
				}
			}
			entry.StateChanges[addr.Hex()] = se
		}
	}

	return entry
}

func sendRPCRequest(rpcURL string, method string, params []interface{}) (json.RawMessage, error) {
	req := RPCRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  method,
		Params:  params,
	}

	reqBody, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	resp, err := http.Post(rpcURL, "application/json", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("HTTP POST: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var rpcResp RPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w (body: %s)", err, string(body))
	}

	if rpcResp.Error != nil {
		return nil, fmt.Errorf("RPC error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}

	return rpcResp.Result, nil
}

func sendBatch(rpcURL string, blocks []ImportBlockEntry) (*ImportBlocksResponse, error) {
	result, err := sendRPCRequest(rpcURL, "migrate_importBlocks", []interface{}{blocks})
	if err != nil {
		return nil, err
	}

	var response ImportBlocksResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &response, nil
}

func reloadBlockchain(rpcURL string) error {
	_, err := sendRPCRequest(rpcURL, "lux_reloadBlockchain", []interface{}{})
	return err
}

func main() {
	jsonlPath := flag.String("jsonl", "", "Path to JSONL blocks file")
	rpcURL := flag.String("rpc", "http://127.0.0.1:9630/ext/bc/C/rpc", "C-Chain RPC endpoint")
	batchSize := flag.Int("batch", 100, "Number of blocks per RPC call")
	startBlock := flag.Uint64("start", 1, "Start block number (default 1, skips genesis)")
	endBlock := flag.Uint64("end", 0, "End block number (0 = all)")
	reloadInterval := flag.Int("reload", 10000, "Reload blockchain every N blocks (0 = only at end)")
	flag.Parse()

	if *jsonlPath == "" {
		fmt.Println("Usage: import-rpc -jsonl <path> [-rpc <url>] [-batch <size>] [-start <n>] [-end <n>]")
		fmt.Println()
		fmt.Println("Imports JSONL blocks to C-Chain via migrate_importBlocks RPC.")
		fmt.Println("Supports both legacy format (PascalCase+base64) and canonical format (camelCase+hex).")
		fmt.Println()
		flag.PrintDefaults()
		os.Exit(1)
	}

	// Open JSONL file
	f, err := os.Open(*jsonlPath)
	if err != nil {
		log.Fatalf("Failed to open JSONL file: %v", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Increase buffer size for large lines (blocks can be big)
	buf := make([]byte, 0, 64*1024*1024) // 64MB buffer
	scanner.Buffer(buf, 64*1024*1024)

	var batch []ImportBlockEntry
	totalImported := 0
	totalFailed := 0
	lineNum := 0
	lastReloadAt := 0
	startTime := time.Now()
	lastReport := time.Now()

	fmt.Printf("Starting import from %s to %s\n", *jsonlPath, *rpcURL)
	fmt.Printf("Batch size: %d, Start: %d, End: %d, Reload interval: %d\n", *batchSize, *startBlock, *endBlock, *reloadInterval)

	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Use migrate.BlockData which handles both legacy and canonical formats
		var block migrate.BlockData
		if err := json.Unmarshal(line, &block); err != nil {
			log.Printf("Line %d: parse error: %v", lineNum, err)
			totalFailed++
			continue
		}

		// Skip blocks outside range
		if block.Number < *startBlock {
			continue
		}
		if *endBlock > 0 && block.Number > *endBlock {
			break
		}

		// Convert to RPC format
		entry := convertBlockData(&block)
		batch = append(batch, *entry)

		// Send batch when full
		if len(batch) >= *batchSize {
			result, err := sendBatch(*rpcURL, batch)
			if err != nil {
				log.Printf("Batch at block %d failed: %v", batch[0].Height, err)
				totalFailed += len(batch)
			} else {
				totalImported += result.Imported
				totalFailed += result.Failed
				if len(result.Errors) > 0 {
					for _, e := range result.Errors {
						log.Printf("  Error: %s", e)
					}
				}
			}
			batch = batch[:0]

			// Periodic reload to sync blockchain state
			if *reloadInterval > 0 && totalImported-lastReloadAt >= *reloadInterval {
				fmt.Printf("Reloading blockchain at %d blocks...\n", totalImported)
				if err := reloadBlockchain(*rpcURL); err != nil {
					log.Printf("Warning: reload failed: %v", err)
				} else {
					lastReloadAt = totalImported
				}
			}

			// Progress report every 10 seconds
			if time.Since(lastReport) > 10*time.Second {
				elapsed := time.Since(startTime)
				rate := float64(totalImported) / elapsed.Seconds()
				fmt.Printf("Progress: %d imported, %d failed, %.1f blocks/sec\n",
					totalImported, totalFailed, rate)
				lastReport = time.Now()
			}
		}
	}

	// Send remaining batch
	if len(batch) > 0 {
		result, err := sendBatch(*rpcURL, batch)
		if err != nil {
			log.Printf("Final batch failed: %v", err)
			totalFailed += len(batch)
		} else {
			totalImported += result.Imported
			totalFailed += result.Failed
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("Scanner error: %v", err)
	}

	// Final reload to sync blockchain state
	fmt.Printf("Final blockchain reload...\n")
	if err := reloadBlockchain(*rpcURL); err != nil {
		log.Printf("Warning: final reload failed: %v", err)
	}

	elapsed := time.Since(startTime)
	rate := float64(totalImported) / elapsed.Seconds()
	fmt.Printf("\n=== Import Complete ===\n")
	fmt.Printf("Total imported: %d\n", totalImported)
	fmt.Printf("Total failed: %d\n", totalFailed)
	fmt.Printf("Time elapsed: %v\n", elapsed.Round(time.Second))
	fmt.Printf("Average rate: %.1f blocks/sec\n", rate)
}
