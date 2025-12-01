package qchain

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/migrate"
)

func TestNewImporter(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: "http://localhost:8545",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	if importer == nil {
		t.Fatal("Expected non-nil importer")
	}
}

func TestImporterInit(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId": "0x1",
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	err = importer.Init(config)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if !importer.initialized {
		t.Error("Expected importer to be initialized")
	}
}

func TestImporterInitNoRPCURL(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
	}

	importer, _ := NewImporter(config)
	err := importer.Init(config)

	if err == nil {
		t.Error("Expected error for missing RPC URL")
	}
}

func TestImporterInitDoubleInit(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId": "0x1",
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	err := importer.Init(config)
	if err != migrate.ErrAlreadyInitialized {
		t.Errorf("Expected ErrAlreadyInitialized, got %v", err)
	}
}

func TestImporterImportConfig(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId": "0x539", // 1337
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	// Matching chain ID should succeed
	migrateConfig := &migrate.Config{
		ChainID: big.NewInt(1337),
	}

	err := importer.ImportConfig(migrateConfig)
	if err != nil {
		t.Fatalf("ImportConfig failed: %v", err)
	}
}

func TestImporterImportConfigMismatch(t *testing.T) {
	mockResponses := map[string]interface{}{
		"eth_chainId": "0x539", // 1337
	}

	server := mockRPCServer(t, mockResponses)
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	// Mismatching chain ID should fail
	migrateConfig := &migrate.Config{
		ChainID: big.NewInt(999),
	}

	err := importer.ImportConfig(migrateConfig)
	if err == nil {
		t.Error("Expected error for chain ID mismatch")
	}
}

func TestImporterImportConfigNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.ImportConfig(&migrate.Config{})
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterImportBlock(t *testing.T) {
	importCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "eth_chainId":
			result, _ := json.Marshal("0x1")
			resp.Result = result
		case "lux_importBlock":
			importCalled = true
			result, _ := json.Marshal(map[string]interface{}{
				"success": true,
			})
			resp.Result = result
		default:
			resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	block := &migrate.BlockData{
		Number:     1,
		Hash:       common.HexToHash("0x1234"),
		ParentHash: common.HexToHash("0x0000"),
		StateRoot:  common.HexToHash("0xabcd"),
		Header:     []byte{0x01, 0x02, 0x03},
		Body:       []byte{0x04, 0x05, 0x06},
	}

	err := importer.ImportBlock(block)
	if err != nil {
		t.Fatalf("ImportBlock failed: %v", err)
	}

	if !importCalled {
		t.Error("Expected lux_importBlock to be called")
	}

	if importer.lastImported != 1 {
		t.Errorf("Expected lastImported 1, got %d", importer.lastImported)
	}

	if importer.importedCount != 1 {
		t.Errorf("Expected importedCount 1, got %d", importer.importedCount)
	}
}

func TestImporterImportBlockNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.ImportBlock(&migrate.BlockData{})
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterImportBlocks(t *testing.T) {
	importCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "eth_chainId":
			result, _ := json.Marshal("0x1")
			resp.Result = result
		case "lux_importBlocks":
			// Batch import
			result, _ := json.Marshal(map[string]interface{}{
				"imported": 3,
				"failed":   0,
				"success":  true,
			})
			resp.Result = result
		case "lux_importBlock":
			importCount++
			result, _ := json.Marshal(map[string]interface{}{
				"success": true,
			})
			resp.Result = result
		default:
			resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	blocks := []*migrate.BlockData{
		{Number: 1, Hash: common.HexToHash("0x1"), Header: []byte{1}, Body: []byte{1}},
		{Number: 2, Hash: common.HexToHash("0x2"), Header: []byte{2}, Body: []byte{2}},
		{Number: 3, Hash: common.HexToHash("0x3"), Header: []byte{3}, Body: []byte{3}},
	}

	err := importer.ImportBlocks(blocks)
	if err != nil {
		t.Fatalf("ImportBlocks failed: %v", err)
	}
}

func TestImporterImportBlocksEmpty(t *testing.T) {
	importer := &Importer{initialized: true}

	err := importer.ImportBlocks(nil)
	if err != nil {
		t.Errorf("Expected nil error for empty blocks, got %v", err)
	}

	err = importer.ImportBlocks([]*migrate.BlockData{})
	if err != nil {
		t.Errorf("Expected nil error for empty blocks, got %v", err)
	}
}

func TestImporterImportState(t *testing.T) {
	stateCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "eth_chainId":
			result, _ := json.Marshal("0x1")
			resp.Result = result
		case "lux_importState":
			stateCalled = true
			result, _ := json.Marshal(map[string]interface{}{
				"success":  true,
				"imported": 2,
			})
			resp.Result = result
		default:
			resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	accounts := []*migrate.Account{
		{
			Address: common.HexToAddress("0x1234567890123456789012345678901234567890"),
			Nonce:   1,
			Balance: big.NewInt(1000),
		},
		{
			Address: common.HexToAddress("0x0987654321098765432109876543210987654321"),
			Nonce:   5,
			Balance: big.NewInt(5000),
			Code:    []byte{0x60, 0x00},
		},
	}

	err := importer.ImportState(accounts, 100)
	if err != nil {
		t.Fatalf("ImportState failed: %v", err)
	}

	if !stateCalled {
		t.Error("Expected lux_importState to be called")
	}
}

func TestImporterImportStateNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.ImportState([]*migrate.Account{}, 0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterFinalizeImport(t *testing.T) {
	finalizeCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "eth_chainId":
			result, _ := json.Marshal("0x1")
			resp.Result = result
		case "lux_finalizeImport":
			finalizeCalled = true
			result, _ := json.Marshal(map[string]interface{}{
				"success":   true,
				"stateRoot": "0xabcd",
			})
			resp.Result = result
		default:
			resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	err := importer.FinalizeImport(100)
	if err != nil {
		t.Fatalf("FinalizeImport failed: %v", err)
	}

	if !finalizeCalled {
		t.Error("Expected lux_finalizeImport to be called")
	}
}

func TestImporterFinalizeImportNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.FinalizeImport(0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterVerifyImport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "eth_chainId":
			result, _ := json.Marshal("0x1")
			resp.Result = result
		case "eth_getBlockByNumber":
			result, _ := json.Marshal(map[string]interface{}{
				"number":     "0x64", // 100
				"hash":       "0x1234",
				"stateRoot":  "0xabcd",
				"parentHash": "0x0000",
			})
			resp.Result = result
		default:
			resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	err := importer.VerifyImport(100)
	if err != nil {
		t.Fatalf("VerifyImport failed: %v", err)
	}
}

func TestImporterVerifyImportBlockNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "eth_chainId":
			result, _ := json.Marshal("0x1")
			resp.Result = result
		case "eth_getBlockByNumber":
			result, _ := json.Marshal(nil)
			resp.Result = result
		default:
			resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	err := importer.VerifyImport(100)
	if err == nil {
		t.Error("Expected error for block not found")
	}
}

func TestImporterVerifyImportNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.VerifyImport(0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterExecuteBlock(t *testing.T) {
	executeCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		var resp rpcResponse
		resp.JSONRPC = "2.0"
		resp.ID = req.ID

		switch req.Method {
		case "eth_chainId":
			result, _ := json.Marshal("0x1")
			resp.Result = result
		case "lux_executeBlock":
			executeCalled = true
			result, _ := json.Marshal(map[string]interface{}{
				"success":   true,
				"stateRoot": "0xabcd000000000000000000000000000000000000000000000000000000000000",
				"gasUsed":   21000,
			})
			resp.Result = result
		default:
			resp.Error = &rpcError{Code: -32601, Message: "Method not found"}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	block := &migrate.BlockData{
		Number:    1,
		Header:    []byte{0x01, 0x02},
		Body:      []byte{0x03, 0x04},
		StateRoot: common.HexToHash("0xabcd000000000000000000000000000000000000000000000000000000000000"),
	}

	err := importer.ExecuteBlock(block)
	if err != nil {
		t.Fatalf("ExecuteBlock failed: %v", err)
	}

	if !executeCalled {
		t.Error("Expected lux_executeBlock to be called")
	}
}

func TestImporterExecuteBlockNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.ExecuteBlock(&migrate.BlockData{})
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterClose(t *testing.T) {
	importer := &Importer{
		initialized: true,
		rpc:         NewRPCClient("http://localhost:8545"),
	}

	err := importer.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if importer.initialized {
		t.Error("Expected initialized to be false after Close")
	}

	if importer.rpc != nil {
		t.Error("Expected rpc to be nil after Close")
	}
}

func TestImporterGetImportStats(t *testing.T) {
	importer := &Importer{
		lastImported:  100,
		importedCount: 50,
	}

	last, total := importer.GetImportStats()
	if last != 100 {
		t.Errorf("Expected lastImported 100, got %d", last)
	}
	if total != 50 {
		t.Errorf("Expected importedCount 50, got %d", total)
	}
}

func TestProgressTracker(t *testing.T) {
	tracker := NewProgressTracker(0, 100)

	if tracker.Progress() != 0 {
		t.Errorf("Expected initial progress 0, got %f", tracker.Progress())
	}

	tracker.Update(50)
	if tracker.Progress() != 50.0 {
		t.Errorf("Expected progress 50, got %f", tracker.Progress())
	}

	tracker.Update(100)
	if tracker.Progress() != 100.0 {
		t.Errorf("Expected progress 100, got %f", tracker.Progress())
	}
}

func TestProgressTrackerZeroRange(t *testing.T) {
	tracker := NewProgressTracker(50, 50)
	if tracker.Progress() != 100.0 {
		t.Errorf("Expected progress 100 for zero range, got %f", tracker.Progress())
	}
}

func TestProgressTrackerAddError(t *testing.T) {
	tracker := NewProgressTracker(0, 100)

	if len(tracker.Errors) != 0 {
		t.Error("Expected no initial errors")
	}

	tracker.AddError(migrate.ErrBlockNotFound)
	if len(tracker.Errors) != 1 {
		t.Errorf("Expected 1 error, got %d", len(tracker.Errors))
	}
}

func TestProgressTrackerETA(t *testing.T) {
	tracker := NewProgressTracker(0, 100)
	tracker.StartTime = time.Now().Add(-10 * time.Second) // Started 10 seconds ago
	tracker.Update(50)

	eta := tracker.ETA()
	// Should be approximately 10 seconds remaining
	if eta < 5*time.Second || eta > 15*time.Second {
		t.Errorf("ETA %v out of expected range", eta)
	}
}

func TestProgressTrackerBlocksPerSecond(t *testing.T) {
	tracker := NewProgressTracker(0, 100)
	tracker.StartTime = time.Now().Add(-10 * time.Second) // Started 10 seconds ago
	tracker.Update(50)

	bps := tracker.BlocksPerSecond()
	// Should be approximately 5 blocks per second
	if bps < 4 || bps > 6 {
		t.Errorf("Blocks per second %f out of expected range", bps)
	}
}

func TestBatchRPCClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqs []rpcRequest
		json.NewDecoder(r.Body).Decode(&reqs)

		resps := make([]rpcResponse, len(reqs))
		for i, req := range reqs {
			result, _ := json.Marshal("0x1")
			resps[i] = rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  result,
			}
		}

		json.NewEncoder(w).Encode(resps)
	}))
	defer server.Close()

	client := NewBatchRPCClient(server.URL, 10)

	calls := []struct {
		Method string
		Params []interface{}
	}{
		{Method: "eth_chainId", Params: nil},
		{Method: "eth_blockNumber", Params: nil},
		{Method: "net_version", Params: nil},
	}

	results, err := client.BatchCall(calls)
	if err != nil {
		t.Fatalf("BatchCall failed: %v", err)
	}

	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}
}

func TestEncodeFullBlock(t *testing.T) {
	block := &migrate.BlockData{
		Header: []byte{0x01, 0x02, 0x03},
		Body:   []byte{0x04, 0x05, 0x06},
	}

	encoded, err := encodeFullBlock(block)
	if err != nil {
		t.Fatalf("encodeFullBlock failed: %v", err)
	}

	expected := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06}
	if string(encoded) != string(expected) {
		t.Errorf("Encoded block mismatch: got %v, expected %v", encoded, expected)
	}
}

// TestImporterInterfaceCompliance verifies Importer implements migrate.Importer
func TestImporterInterfaceCompliance(t *testing.T) {
	var _ migrate.Importer = (*Importer)(nil)
}

// BenchmarkImportBlock benchmarks block import performance
func BenchmarkImportBlock(b *testing.B) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		json.NewDecoder(r.Body).Decode(&req)

		resp := rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
		}

		switch req.Method {
		case "eth_chainId":
			result, _ := json.Marshal("0x1")
			resp.Result = result
		case "lux_importBlock":
			result, _ := json.Marshal(map[string]interface{}{"success": true})
			resp.Result = result
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: server.URL,
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	block := &migrate.BlockData{
		Number: 1,
		Hash:   common.HexToHash("0x1234"),
		Header: []byte{0x01, 0x02, 0x03},
		Body:   []byte{0x04, 0x05, 0x06},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		block.Number = uint64(i)
		err := importer.ImportBlock(block)
		if err != nil {
			b.Fatalf("ImportBlock failed: %v", err)
		}
	}
}
