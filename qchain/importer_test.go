package qchain

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: "http://localhost:8545",
	}

	importer, err := NewImporter(config)
	if err != nil {
		t.Fatalf("NewImporter failed: %v", err)
	}

	err = importer.Init(config)
	if err != nil {
		t.Fatalf("Init failed: %v", err)
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
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: "http://localhost:8545",
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	err := importer.Init(config)
	if err == nil {
		t.Error("Expected error on double init")
	}
}

func TestImporterImportConfig(t *testing.T) {
	config := migrate.ImporterConfig{
		VMType: migrate.VMTypeQChain,
		RPCURL: "http://localhost:8545",
	}

	importer, _ := NewImporter(config)
	_ = importer.Init(config)

	// ImportConfig is a no-op for Q-Chain
	err := importer.ImportConfig(&migrate.Config{})
	if err != nil {
		t.Fatalf("ImportConfig failed: %v", err)
	}
}

func TestImporterImportBlockNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.ImportBlock(&migrate.BlockData{})
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterImportBlocksNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.ImportBlocks([]*migrate.BlockData{{Number: 1}})
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterImportBlocksEmpty(t *testing.T) {
	importer := &Importer{inited: true}

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
	importer := &Importer{inited: true}

	err := importer.ImportState([]*migrate.Account{}, 0)
	if err != migrate.ErrStateImportNotSupported {
		t.Errorf("Expected ErrStateImportNotSupported, got %v", err)
	}
}

func TestImporterVerifyImport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string        `json:"jsonrpc"`
			ID      int           `json:"id"`
			Method  string        `json:"method"`
			Params  []interface{} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"number":     "0x64",
				"hash":       "0x1234",
				"stateRoot":  "0xabcd",
				"parentHash": "0x0000",
			},
		}

		w.Header().Set("Content-Type", "application/json")
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

func TestImporterVerifyImportNotInitialized(t *testing.T) {
	importer := &Importer{}

	err := importer.VerifyImport(0)
	if err != migrate.ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}
}

func TestImporterFinalizeImport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			JSONRPC string        `json:"jsonrpc"`
			ID      int           `json:"id"`
			Method  string        `json:"method"`
			Params  []interface{} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      req.ID,
			"result": map[string]interface{}{
				"number": "0x64",
				"hash":   "0x1234",
			},
		}

		w.Header().Set("Content-Type", "application/json")
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
		t.Logf("FinalizeImport returned: %v", err)
	}
}

func TestImporterExecuteBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  true,
		}

		w.Header().Set("Content-Type", "application/json")
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
	}
	err := importer.ExecuteBlock(block)
	if err != nil {
		t.Logf("ExecuteBlock returned: %v", err)
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
	importer := &Importer{inited: true}

	err := importer.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	if importer.inited {
		t.Error("Expected inited to be false after Close")
	}
}

// TestImporterInterfaceCompliance verifies Importer implements migrate.Importer
func TestImporterInterfaceCompliance(t *testing.T) {
	var _ migrate.Importer = (*Importer)(nil)
}
