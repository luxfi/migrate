package qchain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luxfi/migrate"
)

// mockRPCServer creates a mock JSON-RPC server for testing
func mockRPCServer(t *testing.T, responses map[string]interface{}) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string        `json:"jsonrpc"`
			ID      int           `json:"id"`
			Method  string        `json:"method"`
			Params  []interface{} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("Failed to decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		result, ok := responses[req.Method]
		if !ok {
			t.Logf("No mock response for method: %s", req.Method)
			resp := map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"error": map[string]interface{}{
					"code":    -32601,
					"message": "Method not found",
				},
			}
			json.NewEncoder(w).Encode(resp)
			return
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result":  result,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
}

func TestNewExporter(t *testing.T) {
	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: "http://localhost:8545",
	}

	exporter, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}

	if exporter == nil {
		t.Fatal("Expected non-nil exporter")
	}
}

func TestExporterInit(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId":     "0x1",
		"eth_blockNumber": "0x100",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0x0100000000000000000000000000000000000000000000000000000000000000",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactionsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0x0200000000000000000000000000000000000000000000000000000000000000",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"transactions":     []interface{}{},
			"miner":            "0x0000000000000000000000000000000000000000",
		},
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	exporter, err := NewExporter(config)
	if err != nil {
		t.Fatalf("NewExporter failed: %v", err)
	}

	err = exporter.Init(config)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
}

func TestExporterInitDoubleInit(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId":     "0x1",
		"eth_blockNumber": "0x100",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0x0100000000000000000000000000000000000000000000000000000000000000",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactionsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0x0200000000000000000000000000000000000000000000000000000000000000",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"transactions":     []interface{}{},
			"miner":            "0x0000000000000000000000000000000000000000",
		},
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	exporter, _ := NewExporter(config)
	_ = exporter.Init(config)

	// Second init should fail
	err := exporter.Init(config)
	if err == nil {
		t.Error("Expected error on double init")
	}
}

func TestExporterGetInfo(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId":     "0x539", // 1337
		"eth_blockNumber": "0x64",  // 100
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0xabcd000000000000000000000000000000000000000000000000000000000000",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactionsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0xdead000000000000000000000000000000000000000000000000000000000000",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"transactions":     []interface{}{},
			"miner":            "0x0000000000000000000000000000000000000000",
		},
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	exporter, _ := NewExporter(config)
	_ = exporter.Init(config)

	info, err := exporter.GetInfo()
	if err != nil {
		t.Fatalf("GetInfo failed: %v", err)
	}

	if info.VMType != migrate.VMTypeQChain {
		t.Errorf("Expected VMType q-chain, got %s", info.VMType)
	}

	if info.CurrentHeight != 100 {
		t.Errorf("Expected CurrentHeight 100, got %d", info.CurrentHeight)
	}
}

func TestExporterGetInfoNotInitialized(t *testing.T) {
	exporter := &Exporter{}

	_, err := exporter.GetInfo()
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestExporterExportBlocksNotInitialized(t *testing.T) {
	exporter := &Exporter{}

	blocks, errs := exporter.ExportBlocks(context.Background(), 0, 10)

	// Drain channels
	for range blocks {
	}
	err := <-errs

	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestExporterExportConfig(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId":     "0x539",
		"eth_blockNumber": "0x64",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0xabcd000000000000000000000000000000000000000000000000000000000000",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactionsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0xdead000000000000000000000000000000000000000000000000000000000000",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"transactions":     []interface{}{},
			"miner":            "0x0000000000000000000000000000000000000000",
		},
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	exporter, _ := NewExporter(config)
	_ = exporter.Init(config)

	exportedConfig, err := exporter.ExportConfig()
	if err != nil {
		t.Fatalf("ExportConfig failed: %v", err)
	}

	if exportedConfig.NetworkID != 1337 {
		t.Errorf("Expected NetworkID 1337, got %d", exportedConfig.NetworkID)
	}
}

func TestExporterClose(t *testing.T) {
	exporter := &Exporter{
		inited: true,
	}

	err := exporter.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if exporter.inited {
		t.Error("Expected inited to be false after Close")
	}
}

func TestExporterVerifyExport(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId":     "0x1",
		"eth_blockNumber": "0x64",
		"eth_getBlockByNumber": map[string]interface{}{
			"number":           "0x0",
			"hash":             "0xabcd000000000000000000000000000000000000000000000000000000000000",
			"parentHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
			"transactionsRoot": "0x0000000000000000000000000000000000000000000000000000000000000000",
			"stateRoot":        "0xdead000000000000000000000000000000000000000000000000000000000000",
			"gasLimit":         "0x1000000",
			"gasUsed":          "0x0",
			"timestamp":        "0x0",
			"transactions":     []interface{}{},
			"miner":            "0x0000000000000000000000000000000000000000",
		},
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ExporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	exporter, _ := NewExporter(config)
	_ = exporter.Init(config)

	err := exporter.VerifyExport(0)
	if err != nil {
		t.Logf("VerifyExport returned: %v", err)
	}
}

func TestExporterExportAccountNotInitialized(t *testing.T) {
	exporter := &Exporter{}

	_, err := exporter.ExportAccount(context.Background(), [20]byte{}, 0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestExporterExportStateNotInitialized(t *testing.T) {
	exporter := &Exporter{}

	accounts, errs := exporter.ExportState(context.Background(), 0)

	// Drain channels
	for range accounts {
	}
	err := <-errs

	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

// TestInterfaceCompliance verifies Exporter implements migrate.Exporter
func TestInterfaceCompliance(t *testing.T) {
	var _ migrate.Exporter = (*Exporter)(nil)
}
