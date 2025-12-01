// Copyright (C) 2019-2025, Lux Industries, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package pchain

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luxfi/ids"
	"github.com/luxfi/migrate"
)

func TestNewImporter(t *testing.T) {
	tests := []struct {
		name    string
		config  migrate.ImporterConfig
		wantErr bool
	}{
		{
			name: "valid config with RPC URL",
			config: migrate.ImporterConfig{
				RPCURL: "http://localhost:9650",
			},
			wantErr: false,
		},
		{
			name:    "missing RPC URL",
			config:  migrate.ImporterConfig{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imp, err := NewImporter(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewImporter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && imp == nil {
				t.Error("NewImporter() returned nil importer")
			}
		})
	}
}

func TestImporter_Init(t *testing.T) {
	imp := &Importer{}

	config := migrate.ImporterConfig{
		RPCURL: "http://localhost:9650",
	}

	if err := imp.Init(config); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if !imp.initialized {
		t.Error("Init() did not set initialized to true")
	}

	if imp.importedValidators == nil {
		t.Error("Init() did not initialize importedValidators map")
	}

	if imp.importedTxs == nil {
		t.Error("Init() did not initialize importedTxs map")
	}

	// Double init should fail
	if err := imp.Init(config); err != migrate.ErrAlreadyInitialized {
		t.Errorf("Init() second call error = %v, want %v", err, migrate.ErrAlreadyInitialized)
	}
}

func TestImporter_ImportConfig(t *testing.T) {
	imp := &Importer{initialized: true}

	config := &migrate.Config{
		NetworkID: 1,
	}

	if err := imp.ImportConfig(config); err != nil {
		t.Errorf("ImportConfig() error = %v", err)
	}
}

func TestImporter_ImportConfig_NotInitialized(t *testing.T) {
	imp := &Importer{initialized: false}

	if err := imp.ImportConfig(&migrate.Config{}); err != migrate.ErrNotInitialized {
		t.Errorf("ImportConfig() error = %v, want %v", err, migrate.ErrNotInitialized)
	}
}

func TestImporter_IssueTx(t *testing.T) {
	// Use a proper cb58 encoded ID
	expectedID := ids.GenerateTestID()
	expectedTxID := expectedID.String()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		if req.Method != "platform.issueTx" {
			t.Errorf("unexpected method: %s", req.Method)
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"txID": expectedTxID,
			},
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	imp := &Importer{
		initialized: true,
		config:      migrate.ImporterConfig{RPCURL: server.URL},
		client:      &http.Client{},
	}

	txBytes := []byte{0x01, 0x02, 0x03}
	txID, err := imp.issueTx(txBytes)
	if err != nil {
		t.Fatalf("issueTx() error = %v", err)
	}

	if txID != expectedID {
		t.Errorf("issueTx() txID = %v, want %v", txID, expectedID)
	}
}

func TestImporter_GetTxStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"status": "Committed",
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	imp := &Importer{
		initialized: true,
		config:      migrate.ImporterConfig{RPCURL: server.URL},
		client:      &http.Client{},
	}

	txID, _ := ids.FromString("2Z36RnQuk1hvsnFeGWzfZUfXNr7w8SjoCBWPDGGPmgW3dXfFbT")
	status, err := imp.getTxStatus(txID)
	if err != nil {
		t.Fatalf("getTxStatus() error = %v", err)
	}

	if status != "Committed" {
		t.Errorf("getTxStatus() status = %v, want Committed", status)
	}
}

func TestImporter_GetHeight(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"height": float64(12345),
			},
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	imp := &Importer{
		initialized: true,
		config:      migrate.ImporterConfig{RPCURL: server.URL},
		client:      &http.Client{},
	}

	height, err := imp.getHeight()
	if err != nil {
		t.Fatalf("getHeight() error = %v", err)
	}

	if height != 12345 {
		t.Errorf("getHeight() = %v, want 12345", height)
	}
}

func TestImporter_ValidatorExists(t *testing.T) {
	tests := []struct {
		name           string
		validators     []interface{}
		expectedExists bool
	}{
		{
			name:           "validator exists",
			validators:     []interface{}{map[string]interface{}{"nodeID": "NodeID-test"}},
			expectedExists: true,
		},
		{
			name:           "validator does not exist",
			validators:     []interface{}{},
			expectedExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				resp := map[string]interface{}{
					"jsonrpc": "2.0",
					"id":      1,
					"result": map[string]interface{}{
						"validators": tt.validators,
					},
				}
				json.NewEncoder(w).Encode(resp)
			}))
			defer server.Close()

			imp := &Importer{
				initialized: true,
				config:      migrate.ImporterConfig{RPCURL: server.URL},
				client:      &http.Client{},
			}

			nodeID, _ := ids.NodeIDFromString("NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg")
			exists, err := imp.validatorExists(nodeID)
			if err != nil {
				t.Fatalf("validatorExists() error = %v", err)
			}

			if exists != tt.expectedExists {
				t.Errorf("validatorExists() = %v, want %v", exists, tt.expectedExists)
			}
		})
	}
}

func TestImporter_ImportRawTx(t *testing.T) {
	// Use a proper cb58 encoded ID
	expectedID := ids.GenerateTestID()
	expectedTxID := expectedID.String()

	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		var req struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		var resp interface{}
		switch req.Method {
		case "platform.issueTx":
			resp = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]interface{}{
					"txID": expectedTxID,
				},
			}
		case "platform.getTxStatus":
			resp = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]interface{}{
					"status": "Committed",
				},
			}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	imp := &Importer{
		initialized:        true,
		config:             migrate.ImporterConfig{RPCURL: server.URL},
		client:             &http.Client{},
		importedValidators: make(map[ids.NodeID]bool),
		importedTxs:        make(map[ids.ID]bool),
	}

	ctx := context.Background()
	txBytes := []byte{0x01, 0x02, 0x03, 0x04}
	txID, err := imp.ImportRawTx(ctx, txBytes)
	if err != nil {
		t.Fatalf("ImportRawTx() error = %v", err)
	}

	if txID != expectedID {
		t.Errorf("ImportRawTx() txID = %v, want %v", txID, expectedID)
	}

	// Verify tx was tracked
	if !imp.importedTxs[txID] {
		t.Error("ImportRawTx() did not track imported tx")
	}
}

func TestImporter_Close(t *testing.T) {
	imp := &Importer{
		initialized:        true,
		importedValidators: make(map[ids.NodeID]bool),
		importedTxs:        make(map[ids.ID]bool),
	}

	// Add some data
	nodeID, _ := ids.NodeIDFromString("NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg")
	imp.importedValidators[nodeID] = true

	if err := imp.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if imp.initialized {
		t.Error("Close() did not set initialized to false")
	}

	if imp.importedValidators != nil {
		t.Error("Close() did not clear importedValidators")
	}

	if imp.importedTxs != nil {
		t.Error("Close() did not clear importedTxs")
	}
}

func TestImportRequest_JSON(t *testing.T) {
	nodeID, _ := ids.NodeIDFromString("NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg")

	request := &ImportRequest{
		Type: TxTypeAddValidator,
		Validator: &ValidatorImport{
			NodeID:        nodeID,
			StartTime:     1609459200,
			EndTime:       1640995200,
			Weight:        2000000000000,
			DelegationFee: 20000,
		},
	}

	data, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded ImportRequest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.Type != TxTypeAddValidator {
		t.Errorf("Type mismatch: got %v, want %v", decoded.Type, TxTypeAddValidator)
	}

	if decoded.Validator.Weight != 2000000000000 {
		t.Errorf("Weight mismatch: got %v, want %v", decoded.Validator.Weight, 2000000000000)
	}
}

func TestTxType_Values(t *testing.T) {
	tests := []struct {
		txType   TxType
		expected string
	}{
		{TxTypeAddValidator, "addValidator"},
		{TxTypeAddDelegator, "addDelegator"},
		{TxTypeAddPermissionlessValidator, "addPermissionlessValidator"},
		{TxTypeAddPermissionlessDelegator, "addPermissionlessDelegator"},
		{TxTypeCreateSubnet, "createSubnet"},
		{TxTypeCreateChain, "createChain"},
		{TxTypeImport, "import"},
		{TxTypeExport, "export"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if string(tt.txType) != tt.expected {
				t.Errorf("TxType = %v, want %v", tt.txType, tt.expected)
			}
		})
	}
}

func TestImporter_ImportRawBlock(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		if req.Method != "lux.importBlock" {
			t.Errorf("unexpected method: %s, want lux.importBlock", req.Method)
		}

		// Verify hex encoding
		if params, ok := req.Params["block"].(string); ok {
			if _, err := hex.DecodeString(params); err != nil {
				t.Errorf("block param is not valid hex: %v", err)
			}
		}

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]interface{}{},
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	imp := &Importer{
		initialized: true,
		config:      migrate.ImporterConfig{RPCURL: server.URL},
		client:      &http.Client{},
	}

	blockBytes := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
	if err := imp.importRawBlock(blockBytes); err != nil {
		t.Fatalf("importRawBlock() error = %v", err)
	}
}
