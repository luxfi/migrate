// process-rpc imports JSONL blocks via RPC to C-Chain using migrate_processBlocks
// This processes blocks with transaction execution and state trie building
// Use this instead of import-rpc when you need live balances and state
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
	"strings"
	"time"
)

// JSONLBlock represents a block in our export JSONL format (lowercase, hex)
type JSONLBlock struct {
	Number     uint64 `json:"number"`
	Hash       string `json:"hash"`
	HeaderRLP  string `json:"header_rlp"`   // hex-encoded RLP
	BodyRLP    string `json:"body_rlp"`     // hex-encoded RLP
	ReceiptsRLP string `json:"receipts_rlp"` // hex-encoded RLP
}

// ImportBlockEntry is the format expected by migrate_processBlocks (lowercase, hex)
type ImportBlockEntry struct {
	Height   uint64 `json:"height"`
	Hash     string `json:"hash"`
	Header   string `json:"header"`   // hex-encoded RLP
	Body     string `json:"body"`     // hex-encoded RLP
	Receipts string `json:"receipts"` // hex-encoded RLP
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

// ProcessBlocksResponse is the response from migrate_processBlocks
type ProcessBlocksResponse struct {
	Processed   int      `json:"processed"`
	Failed      int      `json:"failed"`
	FirstHeight uint64   `json:"firstHeight"`
	LastHeight  uint64   `json:"lastHeight"`
	CurrentHead uint64   `json:"currentHead"`
	StateRoot   string   `json:"stateRoot"`
	Errors      []string `json:"errors,omitempty"`
}

// ensureHexPrefix ensures a hex string has 0x prefix
func ensureHexPrefix(s string) string {
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "0x") {
		return "0x" + s
	}
	return s
}

func convertBlock(jsonl *JSONLBlock) (*ImportBlockEntry, error) {
	return &ImportBlockEntry{
		Height:   jsonl.Number,
		Hash:     ensureHexPrefix(jsonl.Hash),
		Header:   ensureHexPrefix(jsonl.HeaderRLP),
		Body:     ensureHexPrefix(jsonl.BodyRLP),
		Receipts: ensureHexPrefix(jsonl.ReceiptsRLP),
	}, nil
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

func sendBatch(rpcURL string, blocks []ImportBlockEntry) (*ProcessBlocksResponse, error) {
	// Use migrate_processBlocks which executes transactions and builds state
	result, err := sendRPCRequest(rpcURL, "migrate_processBlocks", []interface{}{blocks})
	if err != nil {
		return nil, err
	}

	var response ProcessBlocksResponse
	if err := json.Unmarshal(result, &response); err != nil {
		return nil, fmt.Errorf("unmarshal result: %w", err)
	}

	return &response, nil
}

func getBalance(rpcURL, address string) (string, error) {
	result, err := sendRPCRequest(rpcURL, "eth_getBalance", []interface{}{address, "latest"})
	if err != nil {
		return "", err
	}
	var balance string
	if err := json.Unmarshal(result, &balance); err != nil {
		return "", err
	}
	return balance, nil
}

func main() {
	jsonlPath := flag.String("jsonl", "", "Path to JSONL blocks file")
	rpcURL := flag.String("rpc", "http://127.0.0.1:9630/ext/bc/C/rpc", "C-Chain RPC endpoint")
	batchSize := flag.Int("batch", 10, "Number of blocks per RPC call (smaller for state processing)")
	startBlock := flag.Uint64("start", 1, "Start block number (default 1, skips genesis)")
	endBlock := flag.Uint64("end", 0, "End block number (0 = all)")
	verifyAddress := flag.String("verify", "", "Address to verify balance after each batch")
	flag.Parse()

	if *jsonlPath == "" {
		fmt.Println("Usage: process-rpc -jsonl <path> [-rpc <url>] [-batch <size>] [-start <n>] [-end <n>] [-verify <addr>]")
		fmt.Println("\nThis tool processes blocks WITH state trie building (unlike import-rpc which only stores blocks).")
		fmt.Println("Use smaller batch sizes (10-50) as each block executes transactions.")
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
	totalProcessed := 0
	totalFailed := 0
	lineNum := 0
	startTime := time.Now()
	lastReport := time.Now()
	var lastStateRoot string
	var lastHead uint64

	fmt.Printf("Starting block processing from %s to %s\n", *jsonlPath, *rpcURL)
	fmt.Printf("Batch size: %d, Start: %d, End: %d\n", *batchSize, *startBlock, *endBlock)
	fmt.Println("NOTE: This processes blocks with state trie building (executes transactions)")

	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var jsonBlock JSONLBlock
		if err := json.Unmarshal(line, &jsonBlock); err != nil {
			log.Printf("Line %d: parse error: %v", lineNum, err)
			totalFailed++
			continue
		}

		// Skip blocks outside range
		if jsonBlock.Number < *startBlock {
			continue
		}
		if *endBlock > 0 && jsonBlock.Number > *endBlock {
			break
		}

		// Convert to API format
		entry, err := convertBlock(&jsonBlock)
		if err != nil {
			log.Printf("Block %d: convert error: %v", jsonBlock.Number, err)
			totalFailed++
			continue
		}

		batch = append(batch, *entry)

		// Send batch when full
		if len(batch) >= *batchSize {
			result, err := sendBatch(*rpcURL, batch)
			if err != nil {
				log.Printf("Batch at block %d failed: %v", batch[0].Height, err)
				totalFailed += len(batch)
			} else {
				totalProcessed += result.Processed
				totalFailed += result.Failed
				lastStateRoot = result.StateRoot
				lastHead = result.CurrentHead
				if len(result.Errors) > 0 {
					for _, e := range result.Errors {
						log.Printf("  Error: %s", e)
					}
				}
			}
			batch = batch[:0]

			// Progress report every 10 seconds
			if time.Since(lastReport) > 10*time.Second {
				elapsed := time.Since(startTime)
				rate := float64(totalProcessed) / elapsed.Seconds()
				fmt.Printf("Progress: %d processed, %d failed, %.1f blocks/sec, head=%d, stateRoot=%s\n",
					totalProcessed, totalFailed, rate, lastHead, lastStateRoot)

				// Optionally verify balance
				if *verifyAddress != "" {
					balance, err := getBalance(*rpcURL, *verifyAddress)
					if err != nil {
						fmt.Printf("  Balance check: error - %v\n", err)
					} else {
						fmt.Printf("  Balance check: %s = %s\n", *verifyAddress, balance)
					}
				}
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
			totalProcessed += result.Processed
			totalFailed += result.Failed
			lastStateRoot = result.StateRoot
			lastHead = result.CurrentHead
		}
	}

	if err := scanner.Err(); err != nil {
		log.Fatalf("Scanner error: %v", err)
	}

	elapsed := time.Since(startTime)
	rate := float64(totalProcessed) / elapsed.Seconds()
	fmt.Printf("\n=== Processing Complete ===\n")
	fmt.Printf("Total processed: %d\n", totalProcessed)
	fmt.Printf("Total failed: %d\n", totalFailed)
	fmt.Printf("Current head: %d\n", lastHead)
	fmt.Printf("State root: %s\n", lastStateRoot)
	fmt.Printf("Time elapsed: %v\n", elapsed.Round(time.Second))
	fmt.Printf("Average rate: %.1f blocks/sec\n", rate)

	// Final balance check
	if *verifyAddress != "" {
		balance, err := getBalance(*rpcURL, *verifyAddress)
		if err != nil {
			fmt.Printf("Final balance check: error - %v\n", err)
		} else {
			fmt.Printf("Final balance: %s = %s\n", *verifyAddress, balance)
		}
	}
}
