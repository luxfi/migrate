// rpcserver provides a read-only Ethereum JSON-RPC server
// that reads from a PebbleDB database for Blockscout indexing
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"strconv"
	"strings"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/geth/core/rawdb"
	"github.com/luxfi/geth/core/types"
	"github.com/luxfi/geth/ethdb"
	"github.com/luxfi/geth/ethdb/pebble"
)

var (
	dbPath  = flag.String("db", "", "Path to PebbleDB database")
	addr    = flag.String("addr", ":9630", "HTTP server address")
	chainID = flag.Int64("chain-id", 96369, "Chain ID")
)

type Server struct {
	db      ethdb.Database
	chainID *big.Int
}

type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      interface{}     `json:"id"`
}

type JSONRPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
	ID      interface{} `json:"id"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func main() {
	flag.Parse()

	if *dbPath == "" {
		log.Fatal("Usage: rpcserver -db <path> [-addr :9630] [-chain-id 96369]")
	}

	// Open PebbleDB
	kvstore, err := pebble.New(*dbPath, 512, 256, "rpcserver", true) // readonly=true
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer kvstore.Close()

	db := rawdb.NewDatabase(kvstore)

	server := &Server{
		db:      db,
		chainID: big.NewInt(*chainID),
	}

	// Get current head
	headHash := rawdb.ReadHeadBlockHash(db)
	if headHash != (common.Hash{}) {
		num, _ := rawdb.ReadHeaderNumber(db, headHash)
		log.Printf("Database head: block %d (%s)", num, headHash.Hex())
	}

	// Set up HTTP server
	http.HandleFunc("/", server.handleRPC)
	http.HandleFunc("/ext/bc/C/rpc", server.handleRPC) // LUX-style path

	log.Printf("Starting RPC server on %s (chain ID: %d)", *addr, *chainID)
	if err := http.ListenAndServe(*addr, nil); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req JSONRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.writeError(w, nil, -32700, "Parse error")
		return
	}

	result, err := s.dispatch(r.Context(), req.Method, req.Params)
	if err != nil {
		s.writeError(w, req.ID, -32603, err.Error())
		return
	}

	s.writeResult(w, req.ID, result)
}

func (s *Server) dispatch(ctx context.Context, method string, params json.RawMessage) (interface{}, error) {
	switch method {
	case "eth_chainId":
		return fmt.Sprintf("0x%x", s.chainID), nil

	case "eth_blockNumber":
		return s.ethBlockNumber()

	case "eth_getBlockByNumber":
		return s.ethGetBlockByNumber(params)

	case "eth_getBlockByHash":
		return s.ethGetBlockByHash(params)

	case "eth_getTransactionByHash":
		return s.ethGetTransactionByHash(params)

	case "eth_getTransactionReceipt":
		return s.ethGetTransactionReceipt(params)

	case "eth_getBlockTransactionCountByNumber":
		return s.ethGetBlockTransactionCountByNumber(params)

	case "eth_getBlockTransactionCountByHash":
		return s.ethGetBlockTransactionCountByHash(params)

	case "eth_getTransactionByBlockNumberAndIndex":
		return s.ethGetTransactionByBlockNumberAndIndex(params)

	case "eth_getTransactionByBlockHashAndIndex":
		return s.ethGetTransactionByBlockHashAndIndex(params)

	case "eth_getLogs":
		return s.ethGetLogs(params)

	case "net_version":
		return fmt.Sprintf("%d", s.chainID), nil

	case "web3_clientVersion":
		return "LUX-Migrate-RPC/1.0.0", nil

	case "eth_syncing":
		return false, nil

	case "eth_gasPrice":
		return "0x5d21dba00", nil // 25 gwei

	case "eth_getBalance":
		return "0x0", nil // Balance queries not supported from block data alone

	case "eth_getCode":
		return "0x", nil // Code queries not supported from block data alone

	case "eth_getStorageAt":
		return "0x0", nil

	case "eth_getTransactionCount":
		return "0x0", nil

	default:
		return nil, fmt.Errorf("method %s not supported", method)
	}
}

func (s *Server) ethBlockNumber() (string, error) {
	headHash := rawdb.ReadHeadBlockHash(s.db)
	if headHash == (common.Hash{}) {
		return "0x0", nil
	}
	num, ok := rawdb.ReadHeaderNumber(s.db, headHash)
	if !ok {
		return "0x0", nil
	}
	return fmt.Sprintf("0x%x", num), nil
}

func (s *Server) ethGetBlockByNumber(params json.RawMessage) (interface{}, error) {
	var args []interface{}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("missing parameters")
	}

	blockNum, err := s.parseBlockNumber(args[0])
	if err != nil {
		return nil, err
	}

	fullTx, _ := args[1].(bool)

	hash := rawdb.ReadCanonicalHash(s.db, blockNum)
	if hash == (common.Hash{}) {
		return nil, nil
	}

	return s.getBlock(hash, blockNum, fullTx)
}

func (s *Server) ethGetBlockByHash(params json.RawMessage) (interface{}, error) {
	var args []interface{}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("missing parameters")
	}

	hashStr, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("invalid hash")
	}
	hash := common.HexToHash(hashStr)

	fullTx, _ := args[1].(bool)

	num, ok := rawdb.ReadHeaderNumber(s.db, hash)
	if !ok {
		return nil, nil
	}

	return s.getBlock(hash, num, fullTx)
}

func (s *Server) getBlock(hash common.Hash, number uint64, fullTx bool) (map[string]interface{}, error) {
	header := rawdb.ReadHeader(s.db, hash, number)
	if header == nil {
		return nil, nil
	}

	body := rawdb.ReadBody(s.db, hash, number)

	result := map[string]interface{}{
		"number":           fmt.Sprintf("0x%x", number),
		"hash":             hash.Hex(),
		"parentHash":       header.ParentHash.Hex(),
		"nonce":            fmt.Sprintf("0x%016x", header.Nonce),
		"sha3Uncles":       header.UncleHash.Hex(),
		"logsBloom":        "0x" + hex.EncodeToString(header.Bloom[:]),
		"transactionsRoot": header.TxHash.Hex(),
		"stateRoot":        header.Root.Hex(),
		"receiptsRoot":     header.ReceiptHash.Hex(),
		"miner":            header.Coinbase.Hex(),
		"difficulty":       fmt.Sprintf("0x%x", header.Difficulty),
		"totalDifficulty":  "0x0",
		"extraData":        "0x" + hex.EncodeToString(header.Extra),
		"size":             "0x0",
		"gasLimit":         fmt.Sprintf("0x%x", header.GasLimit),
		"gasUsed":          fmt.Sprintf("0x%x", header.GasUsed),
		"timestamp":        fmt.Sprintf("0x%x", header.Time),
		"uncles":           []string{},
		"mixHash":          header.MixDigest.Hex(),
	}

	if header.BaseFee != nil {
		result["baseFeePerGas"] = fmt.Sprintf("0x%x", header.BaseFee)
	}

	var txs []interface{}
	if body != nil {
		for i, tx := range body.Transactions {
			if fullTx {
				txs = append(txs, s.formatTransaction(tx, hash, number, uint64(i)))
			} else {
				txs = append(txs, tx.Hash().Hex())
			}
		}
	}
	result["transactions"] = txs

	return result, nil
}

func (s *Server) ethGetTransactionByHash(params json.RawMessage) (interface{}, error) {
	var args []string
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if len(args) < 1 {
		return nil, fmt.Errorf("missing hash")
	}

	txHash := common.HexToHash(args[0])

	// Read tx lookup - returns only block number
	blockNumPtr := rawdb.ReadTxLookupEntry(s.db, txHash)
	if blockNumPtr == nil {
		return nil, nil
	}
	blockNum := *blockNumPtr

	// Get canonical hash for this block number
	blockHash := rawdb.ReadCanonicalHash(s.db, blockNum)
	if blockHash == (common.Hash{}) {
		return nil, nil
	}

	// Read block body to find the transaction
	body := rawdb.ReadBody(s.db, blockHash, blockNum)
	if body == nil {
		return nil, nil
	}

	// Find the transaction index
	for i, tx := range body.Transactions {
		if tx.Hash() == txHash {
			return s.formatTransaction(tx, blockHash, blockNum, uint64(i)), nil
		}
	}

	return nil, nil
}

func (s *Server) ethGetTransactionReceipt(params json.RawMessage) (interface{}, error) {
	var args []string
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if len(args) < 1 {
		return nil, fmt.Errorf("missing hash")
	}

	txHash := common.HexToHash(args[0])

	// Read tx lookup - returns only block number
	blockNumPtr := rawdb.ReadTxLookupEntry(s.db, txHash)
	if blockNumPtr == nil {
		return nil, nil
	}
	blockNum := *blockNumPtr

	// Get canonical hash for this block number
	blockHash := rawdb.ReadCanonicalHash(s.db, blockNum)
	if blockHash == (common.Hash{}) {
		return nil, nil
	}

	// Read receipts
	receipts := rawdb.ReadRawReceipts(s.db, blockHash, blockNum)
	if receipts == nil {
		return nil, nil
	}

	// Read block body to find the transaction index
	body := rawdb.ReadBody(s.db, blockHash, blockNum)
	if body == nil {
		return nil, nil
	}

	// Find the transaction index
	for i, tx := range body.Transactions {
		if tx.Hash() == txHash {
			if i < len(receipts) {
				return s.formatReceipt(receipts[i], txHash, blockHash, blockNum, uint64(i)), nil
			}
			break
		}
	}

	return nil, nil
}

func (s *Server) formatTransaction(tx *types.Transaction, blockHash common.Hash, blockNum, txIndex uint64) map[string]interface{} {
	signer := types.LatestSignerForChainID(s.chainID)
	from, _ := types.Sender(signer, tx)

	result := map[string]interface{}{
		"hash":             tx.Hash().Hex(),
		"nonce":            fmt.Sprintf("0x%x", tx.Nonce()),
		"blockHash":        blockHash.Hex(),
		"blockNumber":      fmt.Sprintf("0x%x", blockNum),
		"transactionIndex": fmt.Sprintf("0x%x", txIndex),
		"from":             from.Hex(),
		"value":            fmt.Sprintf("0x%x", tx.Value()),
		"gas":              fmt.Sprintf("0x%x", tx.Gas()),
		"input":            "0x" + hex.EncodeToString(tx.Data()),
		"type":             fmt.Sprintf("0x%x", tx.Type()),
	}

	if tx.To() != nil {
		result["to"] = tx.To().Hex()
	} else {
		result["to"] = nil
	}

	switch tx.Type() {
	case types.LegacyTxType:
		result["gasPrice"] = fmt.Sprintf("0x%x", tx.GasPrice())
	case types.AccessListTxType:
		result["gasPrice"] = fmt.Sprintf("0x%x", tx.GasPrice())
		result["accessList"] = tx.AccessList()
	case types.DynamicFeeTxType:
		result["maxFeePerGas"] = fmt.Sprintf("0x%x", tx.GasFeeCap())
		result["maxPriorityFeePerGas"] = fmt.Sprintf("0x%x", tx.GasTipCap())
		result["accessList"] = tx.AccessList()
	}

	v, r, ss := tx.RawSignatureValues()
	result["v"] = fmt.Sprintf("0x%x", v)
	result["r"] = fmt.Sprintf("0x%x", r)
	result["s"] = fmt.Sprintf("0x%x", ss)

	return result
}

func (s *Server) formatReceipt(receipt *types.Receipt, txHash, blockHash common.Hash, blockNum, txIndex uint64) map[string]interface{} {
	result := map[string]interface{}{
		"transactionHash":   txHash.Hex(),
		"transactionIndex":  fmt.Sprintf("0x%x", txIndex),
		"blockHash":         blockHash.Hex(),
		"blockNumber":       fmt.Sprintf("0x%x", blockNum),
		"cumulativeGasUsed": fmt.Sprintf("0x%x", receipt.CumulativeGasUsed),
		"gasUsed":           fmt.Sprintf("0x%x", receipt.GasUsed),
		"logsBloom":         "0x" + hex.EncodeToString(receipt.Bloom[:]),
		"status":            fmt.Sprintf("0x%x", receipt.Status),
		"type":              fmt.Sprintf("0x%x", receipt.Type),
	}

	if receipt.ContractAddress != (common.Address{}) {
		result["contractAddress"] = receipt.ContractAddress.Hex()
	} else {
		result["contractAddress"] = nil
	}

	var logs []map[string]interface{}
	for i, log := range receipt.Logs {
		logEntry := map[string]interface{}{
			"address":          log.Address.Hex(),
			"blockHash":        blockHash.Hex(),
			"blockNumber":      fmt.Sprintf("0x%x", blockNum),
			"data":             "0x" + hex.EncodeToString(log.Data),
			"logIndex":         fmt.Sprintf("0x%x", i),
			"removed":          false,
			"transactionHash":  txHash.Hex(),
			"transactionIndex": fmt.Sprintf("0x%x", txIndex),
		}
		var topics []string
		for _, topic := range log.Topics {
			topics = append(topics, topic.Hex())
		}
		logEntry["topics"] = topics
		logs = append(logs, logEntry)
	}
	result["logs"] = logs

	return result
}

func (s *Server) ethGetBlockTransactionCountByNumber(params json.RawMessage) (interface{}, error) {
	var args []interface{}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if len(args) < 1 {
		return nil, fmt.Errorf("missing block number")
	}

	blockNum, err := s.parseBlockNumber(args[0])
	if err != nil {
		return nil, err
	}

	hash := rawdb.ReadCanonicalHash(s.db, blockNum)
	if hash == (common.Hash{}) {
		return nil, nil
	}

	body := rawdb.ReadBody(s.db, hash, blockNum)
	if body == nil {
		return "0x0", nil
	}

	return fmt.Sprintf("0x%x", len(body.Transactions)), nil
}

func (s *Server) ethGetBlockTransactionCountByHash(params json.RawMessage) (interface{}, error) {
	var args []string
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if len(args) < 1 {
		return nil, fmt.Errorf("missing hash")
	}

	hash := common.HexToHash(args[0])
	num, ok := rawdb.ReadHeaderNumber(s.db, hash)
	if !ok {
		return nil, nil
	}

	body := rawdb.ReadBody(s.db, hash, num)
	if body == nil {
		return "0x0", nil
	}

	return fmt.Sprintf("0x%x", len(body.Transactions)), nil
}

func (s *Server) ethGetTransactionByBlockNumberAndIndex(params json.RawMessage) (interface{}, error) {
	var args []interface{}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("missing parameters")
	}

	blockNum, err := s.parseBlockNumber(args[0])
	if err != nil {
		return nil, err
	}

	indexStr, ok := args[1].(string)
	if !ok {
		return nil, fmt.Errorf("invalid index")
	}
	index, err := strconv.ParseUint(strings.TrimPrefix(indexStr, "0x"), 16, 64)
	if err != nil {
		return nil, err
	}

	hash := rawdb.ReadCanonicalHash(s.db, blockNum)
	if hash == (common.Hash{}) {
		return nil, nil
	}

	body := rawdb.ReadBody(s.db, hash, blockNum)
	if body == nil || int(index) >= len(body.Transactions) {
		return nil, nil
	}

	return s.formatTransaction(body.Transactions[index], hash, blockNum, index), nil
}

func (s *Server) ethGetTransactionByBlockHashAndIndex(params json.RawMessage) (interface{}, error) {
	var args []interface{}
	if err := json.Unmarshal(params, &args); err != nil {
		return nil, err
	}
	if len(args) < 2 {
		return nil, fmt.Errorf("missing parameters")
	}

	hashStr, ok := args[0].(string)
	if !ok {
		return nil, fmt.Errorf("invalid hash")
	}
	hash := common.HexToHash(hashStr)

	indexStr, ok := args[1].(string)
	if !ok {
		return nil, fmt.Errorf("invalid index")
	}
	index, err := strconv.ParseUint(strings.TrimPrefix(indexStr, "0x"), 16, 64)
	if err != nil {
		return nil, err
	}

	num, ok := rawdb.ReadHeaderNumber(s.db, hash)
	if !ok {
		return nil, nil
	}

	body := rawdb.ReadBody(s.db, hash, num)
	if body == nil || int(index) >= len(body.Transactions) {
		return nil, nil
	}

	return s.formatTransaction(body.Transactions[index], hash, num, index), nil
}

func (s *Server) ethGetLogs(params json.RawMessage) (interface{}, error) {
	// Basic log filtering - limited without bloom filter index
	return []interface{}{}, nil
}

func (s *Server) parseBlockNumber(arg interface{}) (uint64, error) {
	switch v := arg.(type) {
	case string:
		if v == "latest" || v == "pending" {
			headHash := rawdb.ReadHeadBlockHash(s.db)
			if headHash == (common.Hash{}) {
				return 0, nil
			}
			num, _ := rawdb.ReadHeaderNumber(s.db, headHash)
			return num, nil
		}
		if v == "earliest" {
			return 0, nil
		}
		return strconv.ParseUint(strings.TrimPrefix(v, "0x"), 16, 64)
	case float64:
		return uint64(v), nil
	default:
		return 0, fmt.Errorf("invalid block number: %v", arg)
	}
}

func (s *Server) writeResult(w http.ResponseWriter, id interface{}, result interface{}) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		Result:  result,
		ID:      id,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) writeError(w http.ResponseWriter, id interface{}, code int, message string) {
	resp := JSONRPCResponse{
		JSONRPC: "2.0",
		Error:   &RPCError{Code: code, Message: message},
		ID:      id,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
