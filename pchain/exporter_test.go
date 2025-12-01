// Copyright (C) 2019-2025, Lux Industries, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package pchain

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/luxfi/ids"
	"github.com/luxfi/migrate"
)

func TestNewExporter(t *testing.T) {
	tests := []struct {
		name    string
		config  migrate.ExporterConfig
		wantErr bool
	}{
		{
			name: "valid config with RPC URL",
			config: migrate.ExporterConfig{
				RPCURL: "http://localhost:9650",
			},
			wantErr: false,
		},
		{
			name:    "missing RPC URL",
			config:  migrate.ExporterConfig{},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exp, err := NewExporter(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewExporter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && exp == nil {
				t.Error("NewExporter() returned nil exporter")
			}
		})
	}
}

func TestExporter_Init(t *testing.T) {
	exp := &Exporter{}

	config := migrate.ExporterConfig{
		RPCURL: "http://localhost:9650",
	}

	if err := exp.Init(config); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	if !exp.initialized {
		t.Error("Init() did not set initialized to true")
	}

	// Double init should fail
	if err := exp.Init(config); err != migrate.ErrAlreadyInitialized {
		t.Errorf("Init() second call error = %v, want %v", err, migrate.ErrAlreadyInitialized)
	}
}

func TestExporter_GetInfo(t *testing.T) {
	// Create mock RPC server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		var resp interface{}
		switch req.Method {
		case "platform.getHeight":
			resp = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]interface{}{
					"height": float64(12345),
				},
			}
		case "platform.getTimestamp":
			resp = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]interface{}{
					"timestamp": time.Now().Format(time.RFC3339),
				},
			}
		default:
			resp = map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"error":   "unknown method",
			}
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	exp, err := NewExporter(migrate.ExporterConfig{
		RPCURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewExporter() error = %v", err)
	}

	if err := exp.Init(migrate.ExporterConfig{RPCURL: server.URL}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	info, err := exp.GetInfo()
	if err != nil {
		t.Fatalf("GetInfo() error = %v", err)
	}

	if info.VMType != migrate.VMTypePChain {
		t.Errorf("GetInfo() VMType = %v, want %v", info.VMType, migrate.VMTypePChain)
	}

	if info.CurrentHeight != 12345 {
		t.Errorf("GetInfo() CurrentHeight = %v, want %v", info.CurrentHeight, 12345)
	}
}

func TestExporter_ExportBlocks(t *testing.T) {
	// Create mock RPC server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string                 `json:"method"`
			Params map[string]interface{} `json:"params"`
		}
		json.NewDecoder(r.Body).Decode(&req)

		resp := map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result": map[string]interface{}{
				"block":    "0x1234567890",
				"encoding": "hex",
			},
		}

		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	exp, err := NewExporter(migrate.ExporterConfig{
		RPCURL: server.URL,
	})
	if err != nil {
		t.Fatalf("NewExporter() error = %v", err)
	}

	if err := exp.Init(migrate.ExporterConfig{RPCURL: server.URL}); err != nil {
		t.Fatalf("Init() error = %v", err)
	}

	ctx := context.Background()
	blocks, errs := exp.ExportBlocks(ctx, 0, 2)

	var blockCount int
	for range blocks {
		blockCount++
	}

	select {
	case err := <-errs:
		if err != nil {
			t.Fatalf("ExportBlocks() error = %v", err)
		}
	default:
	}

	if blockCount != 3 {
		t.Errorf("ExportBlocks() got %d blocks, want 3", blockCount)
	}
}

func TestExporter_ExportBlocks_InvalidRange(t *testing.T) {
	exp := &Exporter{
		initialized: true,
		config:      migrate.ExporterConfig{RPCURL: "http://localhost:9650"},
		client:      &http.Client{},
	}

	ctx := context.Background()
	blocks, errs := exp.ExportBlocks(ctx, 10, 5) // start > end

	var blockCount int
	for range blocks {
		blockCount++
	}

	select {
	case err := <-errs:
		if err != migrate.ErrInvalidBlockRange {
			t.Errorf("ExportBlocks() error = %v, want %v", err, migrate.ErrInvalidBlockRange)
		}
	default:
		t.Error("ExportBlocks() expected error for invalid range")
	}

	if blockCount != 0 {
		t.Errorf("ExportBlocks() got %d blocks, want 0", blockCount)
	}
}

func TestExporter_Close(t *testing.T) {
	exp := &Exporter{initialized: true}

	if err := exp.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if exp.initialized {
		t.Error("Close() did not set initialized to false")
	}
}

func TestValidatorInfo_JSON(t *testing.T) {
	nodeID, _ := ids.NodeIDFromString("NodeID-7Xhw2mDxuDS44j42TCB6U5579esbSt3Lg")
	txID, _ := ids.FromString("2Z36RnQuk1hvsnFeGWzfZUfXNr7w8SjoCBWPDGGPmgW3dXfFbT")

	validator := ValidatorInfo{
		TxID:          txID,
		NodeID:        nodeID,
		StartTime:     1609459200,
		EndTime:       1640995200,
		Weight:        2000000000000,
		StakeAmount:   2000000000000,
		DelegationFee: 2.0,
		Connected:     true,
	}

	data, err := json.Marshal(validator)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded ValidatorInfo
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.NodeID != validator.NodeID {
		t.Errorf("NodeID mismatch: got %v, want %v", decoded.NodeID, validator.NodeID)
	}

	if decoded.Weight != validator.Weight {
		t.Errorf("Weight mismatch: got %v, want %v", decoded.Weight, validator.Weight)
	}
}

func TestPChainData_JSON(t *testing.T) {
	data := &PChainData{
		Height:        12345,
		Timestamp:     time.Now(),
		CurrentSupply: 720000000000000000,
		Validators: []ValidatorInfo{
			{
				Weight:      2000000000000,
				StakeAmount: 2000000000000,
			},
		},
		Subnets: []SubnetInfo{
			{
				Threshold: 1,
			},
		},
		Blockchains: []BlockchainInfo{
			{
				Name: "C-Chain",
			},
		},
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	var decoded PChainData
	if err := json.Unmarshal(jsonData, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if decoded.Height != data.Height {
		t.Errorf("Height mismatch: got %v, want %v", decoded.Height, data.Height)
	}

	if decoded.CurrentSupply != data.CurrentSupply {
		t.Errorf("CurrentSupply mismatch: got %v, want %v", decoded.CurrentSupply, data.CurrentSupply)
	}

	if len(decoded.Validators) != 1 {
		t.Errorf("Validators count mismatch: got %v, want 1", len(decoded.Validators))
	}
}
