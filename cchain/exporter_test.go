package cchain

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/migrate"
)

// mockRPCHandler creates a mock RPC server for testing
func mockRPCHandler(responses map[string]interface{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		result, ok := responses[req.Method]
		if !ok {
			json.NewEncoder(w).Encode(rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32601, Message: "Method not found"},
			})
			return
		}

		resultJSON, _ := json.Marshal(result)
		json.NewEncoder(w).Encode(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  resultJSON,
		})
	}
}

func TestNewExporter(t *testing.T) {
	// Test without RPC URL
	_, err := NewExporter(migrate.ExporterConfig{})
	if err == nil {
		t.Error("expected error for empty RPC URL")
	}

	// Test with RPC URL
	exp, err := NewExporter(migrate.ExporterConfig{
		RPCURL: "http://localhost:9650/ext/bc/C/rpc",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if exp == nil {
		t.Error("expected non-nil exporter")
	}
}

func TestExporterInit(t *testing.T) {
	responses := map[string]interface{}{
		"eth_chainId":       "0x1",
		"net_version":       "1",
		"eth_blockNumber":   "0x100",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"receiptsRoot":     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"miner":            "0x0000000000000000000000000000000000000000",
			"difficulty":       "0x1",
			"totalDifficulty":  "0x1",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"extraData":        "0x",
			"nonce":            "0x0000000000000000",
			"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactions":     []interface{}{},
		},
	}

	server := httptest.NewServer(mockRPCHandler(responses))
	defer server.Close()

	exp, err := NewExporter(migrate.ExporterConfig{
		RPCURL: server.URL,
	})
	if err != nil {
		t.Fatalf("failed to create exporter: %v", err)
	}

	err = exp.Init(migrate.ExporterConfig{
		RPCURL: server.URL,
	})
	if err != nil {
		t.Fatalf("failed to initialize exporter: %v", err)
	}

	// Test double init
	err = exp.Init(migrate.ExporterConfig{
		RPCURL: server.URL,
	})
	if err != migrate.ErrAlreadyInitialized {
		t.Errorf("expected ErrAlreadyInitialized, got: %v", err)
	}
}

func TestExporterGetInfo(t *testing.T) {
	responses := map[string]interface{}{
		"eth_chainId":       "0xa86a", // 43114 - C-Chain mainnet
		"net_version":       "43114",
		"eth_blockNumber":   "0x100",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x100",
			"hash":             "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000001",
			"stateRoot":        "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"receiptsRoot":     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"miner":            "0x0000000000000000000000000000000000000000",
			"difficulty":       "0x1",
			"totalDifficulty":  "0x100",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x64000000",
			"extraData":        "0x",
			"nonce":            "0x0000000000000000",
			"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"baseFeePerGas":    "0x5d21dba00",
			"transactions":     []interface{}{},
		},
	}

	server := httptest.NewServer(mockRPCHandler(responses))
	defer server.Close()

	exp, _ := NewExporter(migrate.ExporterConfig{RPCURL: server.URL})
	_ = exp.Init(migrate.ExporterConfig{RPCURL: server.URL})

	info, err := exp.GetInfo()
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}

	if info.VMType != migrate.VMTypeCChain {
		t.Errorf("expected VMTypeCChain, got %s", info.VMType)
	}

	expectedChainID := big.NewInt(43114)
	if info.ChainID.Cmp(expectedChainID) != 0 {
		t.Errorf("expected chain ID %s, got %s", expectedChainID, info.ChainID)
	}
}

func TestExportBlock(t *testing.T) {
	responses := map[string]interface{}{
		"eth_chainId":     "0x1",
		"net_version":     "1",
		"eth_blockNumber": "0x100",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x10",
			"hash":             "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000001",
			"stateRoot":        "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"receiptsRoot":     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"miner":            "0x0000000000000000000000000000000000000000",
			"difficulty":       "0x1",
			"totalDifficulty":  "0x10",
			"gasLimit":         "0x8000000",
			"gasUsed":          "0x5208",
			"timestamp":        "0x64000000",
			"extraData":        "0xd883010a0083676574",
			"nonce":            "0x0000000000000000",
			"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"baseFeePerGas":    "0x5d21dba00",
			"transactions": []interface{}{
				map[string]interface{}{
					"hash":             "0xabc123def456abc123def456abc123def456abc123def456abc123def456abc1",
					"nonce":            "0x1",
					"from":             "0x1234567890123456789012345678901234567890",
					"to":               "0xabcdefabcdefabcdefabcdefabcdefabcdefabcd",
					"value":            "0xde0b6b3a7640000",
					"gas":              "0x5208",
					"gasPrice":         "0x77359400",
					"input":            "0x",
					"v":                "0x1c",
					"r":                "0x1234",
					"s":                "0x5678",
					"blockHash":        "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
					"blockNumber":      "0x10",
					"transactionIndex": "0x0",
					"type":             "0x0",
				},
			},
		},
	}

	server := httptest.NewServer(mockRPCHandler(responses))
	defer server.Close()

	exp, _ := NewExporter(migrate.ExporterConfig{RPCURL: server.URL})
	_ = exp.Init(migrate.ExporterConfig{RPCURL: server.URL})

	ctx := context.Background()
	blocksCh, errsCh := exp.ExportBlocks(ctx, 16, 16)

	var block *migrate.BlockData
	select {
	case b := <-blocksCh:
		block = b
	case err := <-errsCh:
		t.Fatalf("ExportBlocks error: %v", err)
	}

	if block == nil {
		t.Fatal("expected block, got nil")
	}

	if block.Number != 16 {
		t.Errorf("expected block number 16, got %d", block.Number)
	}

	if len(block.Transactions) != 1 {
		t.Errorf("expected 1 transaction, got %d", len(block.Transactions))
	}

	if block.GasUsed != 0x5208 {
		t.Errorf("expected gasUsed 0x5208, got 0x%x", block.GasUsed)
	}
}

func TestExportAccount(t *testing.T) {
	responses := map[string]interface{}{
		"eth_chainId":             "0x1",
		"net_version":             "1",
		"eth_blockNumber":         "0x100",
		"eth_getBalance":          "0xde0b6b3a7640000", // 1 ETH
		"eth_getTransactionCount": "0x5",
		"eth_getCode":             "0x608060405234801561001057600080fd5b50",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"receiptsRoot":     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"miner":            "0x0000000000000000000000000000000000000000",
			"difficulty":       "0x1",
			"totalDifficulty":  "0x1",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"extraData":        "0x",
			"nonce":            "0x0000000000000000",
			"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactions":     []interface{}{},
		},
	}

	server := httptest.NewServer(mockRPCHandler(responses))
	defer server.Close()

	exp, _ := NewExporter(migrate.ExporterConfig{RPCURL: server.URL})
	_ = exp.Init(migrate.ExporterConfig{RPCURL: server.URL})

	addr := common.HexToAddress("0x1234567890123456789012345678901234567890")
	ctx := context.Background()

	account, err := exp.ExportAccount(ctx, addr, 100)
	if err != nil {
		t.Fatalf("ExportAccount failed: %v", err)
	}

	expectedBalance := big.NewInt(1000000000000000000) // 1 ETH
	if account.Balance.Cmp(expectedBalance) != 0 {
		t.Errorf("expected balance %s, got %s", expectedBalance, account.Balance)
	}

	if account.Nonce != 5 {
		t.Errorf("expected nonce 5, got %d", account.Nonce)
	}

	if len(account.Code) == 0 {
		t.Error("expected non-empty code")
	}
}

func TestExportConfig(t *testing.T) {
	responses := map[string]interface{}{
		"eth_chainId":     "0xa86a",
		"net_version":     "43114",
		"eth_blockNumber": "0x100",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"receiptsRoot":     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"miner":            "0x0000000000000000000000000000000000000000",
			"difficulty":       "0x1",
			"totalDifficulty":  "0x1",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"extraData":        "0x",
			"nonce":            "0x0000000000000000",
			"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactions":     []interface{}{},
		},
	}

	server := httptest.NewServer(mockRPCHandler(responses))
	defer server.Close()

	exp, _ := NewExporter(migrate.ExporterConfig{RPCURL: server.URL})
	_ = exp.Init(migrate.ExporterConfig{RPCURL: server.URL})

	config, err := exp.ExportConfig()
	if err != nil {
		t.Fatalf("ExportConfig failed: %v", err)
	}

	expectedChainID := big.NewInt(43114)
	if config.ChainID.Cmp(expectedChainID) != 0 {
		t.Errorf("expected chain ID %s, got %s", expectedChainID, config.ChainID)
	}

	if config.NetworkID != 43114 {
		t.Errorf("expected network ID 43114, got %d", config.NetworkID)
	}

	if config.GenesisBlock == nil {
		t.Error("expected genesis block")
	}
}

func TestVerifyExport(t *testing.T) {
	responses := map[string]interface{}{
		"eth_chainId":     "0x1",
		"net_version":     "1",
		"eth_blockNumber": "0x100",
		"eth_getBalance":  "0x0",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x50",
			"hash":             "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000001",
			"stateRoot":        "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"receiptsRoot":     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"miner":            "0x0000000000000000000000000000000000000000",
			"difficulty":       "0x1",
			"totalDifficulty":  "0x50",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x64000000",
			"extraData":        "0x",
			"nonce":            "0x0000000000000000",
			"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactions":     []interface{}{},
		},
	}

	server := httptest.NewServer(mockRPCHandler(responses))
	defer server.Close()

	exp, _ := NewExporter(migrate.ExporterConfig{RPCURL: server.URL})
	_ = exp.Init(migrate.ExporterConfig{RPCURL: server.URL})

	err := exp.VerifyExport(80)
	if err != nil {
		t.Errorf("VerifyExport failed: %v", err)
	}
}

func TestHexConversions(t *testing.T) {
	// Test hexToUint64
	tests := []struct {
		input    string
		expected uint64
	}{
		{"0x0", 0},
		{"0x1", 1},
		{"0x10", 16},
		{"0xff", 255},
		{"0x100", 256},
		{"", 0},
	}

	for _, test := range tests {
		result := hexToUint64(test.input)
		if result != test.expected {
			t.Errorf("hexToUint64(%s) = %d, expected %d", test.input, result, test.expected)
		}
	}

	// Test hexToBigInt
	bigIntTests := []struct {
		input    string
		expected *big.Int
	}{
		{"0x0", big.NewInt(0)},
		{"0x1", big.NewInt(1)},
		{"0xde0b6b3a7640000", big.NewInt(1000000000000000000)},
		{"", nil},
	}

	for _, test := range bigIntTests {
		result := hexToBigInt(test.input)
		if test.expected == nil {
			if result != nil {
				t.Errorf("hexToBigInt(%s) = %v, expected nil", test.input, result)
			}
		} else if result == nil || result.Cmp(test.expected) != 0 {
			t.Errorf("hexToBigInt(%s) = %v, expected %v", test.input, result, test.expected)
		}
	}

	// Test hexToBytes
	bytesTests := []struct {
		input    string
		expected []byte
	}{
		{"0x", nil},
		{"", nil},
		{"0x00", []byte{0}},
		{"0xff", []byte{255}},
		{"0x1234", []byte{0x12, 0x34}},
	}

	for _, test := range bytesTests {
		result := hexToBytes(test.input)
		if test.expected == nil {
			if result != nil {
				t.Errorf("hexToBytes(%s) = %v, expected nil", test.input, result)
			}
		} else if string(result) != string(test.expected) {
			t.Errorf("hexToBytes(%s) = %v, expected %v", test.input, result, test.expected)
		}
	}
}

func TestExporterNotInitialized(t *testing.T) {
	exp := &Exporter{}

	// GetInfo without init
	_, err := exp.GetInfo()
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}

	// ExportBlocks without init
	ctx := context.Background()
	_, errsCh := exp.ExportBlocks(ctx, 0, 10)
	err = <-errsCh
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}

	// ExportState without init
	_, errsCh = exp.ExportState(ctx, 0)
	err = <-errsCh
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}

	// ExportAccount without init
	_, err = exp.ExportAccount(ctx, common.Address{}, 0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}

	// ExportConfig without init
	_, err = exp.ExportConfig()
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}

	// VerifyExport without init
	err = exp.VerifyExport(0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized, got: %v", err)
	}
}

func TestInvalidBlockRange(t *testing.T) {
	responses := map[string]interface{}{
		"eth_chainId":     "0x1",
		"net_version":     "1",
		"eth_blockNumber": "0x100",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"receiptsRoot":     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"miner":            "0x0000000000000000000000000000000000000000",
			"difficulty":       "0x1",
			"totalDifficulty":  "0x1",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"extraData":        "0x",
			"nonce":            "0x0000000000000000",
			"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactions":     []interface{}{},
		},
	}

	server := httptest.NewServer(mockRPCHandler(responses))
	defer server.Close()

	exp, _ := NewExporter(migrate.ExporterConfig{RPCURL: server.URL})
	_ = exp.Init(migrate.ExporterConfig{RPCURL: server.URL})

	ctx := context.Background()
	_, errsCh := exp.ExportBlocks(ctx, 100, 10) // Invalid: start > end
	err := <-errsCh
	if err != migrate.ErrInvalidBlockRange {
		t.Errorf("expected ErrInvalidBlockRange, got: %v", err)
	}
}

func TestExporterClose(t *testing.T) {
	responses := map[string]interface{}{
		"eth_chainId":     "0x1",
		"net_version":     "1",
		"eth_blockNumber": "0x100",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0x1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0xabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
			"transactionsRoot": "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"receiptsRoot":     "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
			"miner":            "0x0000000000000000000000000000000000000000",
			"difficulty":       "0x1",
			"totalDifficulty":  "0x1",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"extraData":        "0x",
			"nonce":            "0x0000000000000000",
			"mixHash":          "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactions":     []interface{}{},
		},
	}

	server := httptest.NewServer(mockRPCHandler(responses))
	defer server.Close()

	exp, _ := NewExporter(migrate.ExporterConfig{RPCURL: server.URL})
	_ = exp.Init(migrate.ExporterConfig{RPCURL: server.URL})

	err := exp.Close()
	if err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// After close, should not be initialized
	_, err = exp.GetInfo()
	if err != migrate.ErrNotInitialized {
		t.Errorf("expected ErrNotInitialized after close, got: %v", err)
	}
}

// Test interface implementation at compile time
var _ migrate.Exporter = (*Exporter)(nil)
