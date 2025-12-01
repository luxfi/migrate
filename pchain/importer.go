// Copyright (C) 2019-2025, Lux Industries, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package pchain

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/luxfi/ids"
	"github.com/luxfi/migrate"
)

// TxType represents P-Chain transaction types
type TxType string

const (
	TxTypeAddValidator              TxType = "addValidator"
	TxTypeAddDelegator              TxType = "addDelegator"
	TxTypeAddPermissionlessValidator TxType = "addPermissionlessValidator"
	TxTypeAddPermissionlessDelegator TxType = "addPermissionlessDelegator"
	TxTypeCreateSubnet              TxType = "createSubnet"
	TxTypeCreateChain               TxType = "createChain"
	TxTypeImport                    TxType = "import"
	TxTypeExport                    TxType = "export"
)

// ImportRequest represents a request to import P-Chain data
type ImportRequest struct {
	// Type of import operation
	Type TxType `json:"type"`
	// Raw transaction bytes (hex encoded)
	TxBytes string `json:"txBytes,omitempty"`
	// Validator information for validator/delegator imports
	Validator *ValidatorImport `json:"validator,omitempty"`
	// Subnet information for subnet imports
	Subnet *SubnetImport `json:"subnet,omitempty"`
	// Chain information for chain imports
	Chain *ChainImport `json:"chain,omitempty"`
}

// ValidatorImport represents validator import data
type ValidatorImport struct {
	NodeID        ids.NodeID    `json:"nodeID"`
	StartTime     uint64        `json:"startTime"`
	EndTime       uint64        `json:"endTime"`
	Weight        uint64        `json:"weight"`
	SubnetID      ids.ID        `json:"subnetID"`
	DelegationFee uint32        `json:"delegationFee,omitempty"` // For validators only
	RewardOwner   *OwnerImport  `json:"rewardOwner,omitempty"`
	Staked        []UTXOImport  `json:"staked,omitempty"`
	PublicKey     []byte        `json:"publicKey,omitempty"` // BLS key
}

// OwnerImport represents owner data for import
type OwnerImport struct {
	Locktime  uint64        `json:"locktime"`
	Threshold uint32        `json:"threshold"`
	Addresses []ids.ShortID `json:"addresses"`
}

// UTXOImport represents a UTXO for staking
type UTXOImport struct {
	TxID        ids.ID `json:"txID"`
	OutputIndex uint32 `json:"outputIndex"`
	Amount      uint64 `json:"amount"`
}

// SubnetImport represents subnet import data
type SubnetImport struct {
	ControlKeys []ids.ShortID `json:"controlKeys"`
	Threshold   uint32        `json:"threshold"`
}

// ChainImport represents chain import data
type ChainImport struct {
	SubnetID    ids.ID   `json:"subnetID"`
	ChainName   string   `json:"chainName"`
	VMID        ids.ID   `json:"vmID"`
	FxIDs       []ids.ID `json:"fxIDs,omitempty"`
	GenesisData []byte   `json:"genesisData"`
}

// Importer imports data to P-Chain via RPC
type Importer struct {
	config      migrate.ImporterConfig
	client      *http.Client
	initialized bool
	// Track imported validators for verification
	importedValidators map[ids.NodeID]bool
	// Track imported transactions
	importedTxs map[ids.ID]bool
}

// NewImporter creates a new P-Chain importer
func NewImporter(config migrate.ImporterConfig) (*Importer, error) {
	if config.RPCURL == "" {
		return nil, fmt.Errorf("RPC URL required for P-Chain import")
	}
	return &Importer{
		config:             config,
		client:             &http.Client{Timeout: 60 * time.Second},
		importedValidators: make(map[ids.NodeID]bool),
		importedTxs:        make(map[ids.ID]bool),
	}, nil
}

func (i *Importer) Init(config migrate.ImporterConfig) error {
	if i.initialized {
		return migrate.ErrAlreadyInitialized
	}
	i.config = config
	if config.RPCURL == "" {
		return fmt.Errorf("RPC URL required for P-Chain import")
	}
	i.client = &http.Client{Timeout: 60 * time.Second}
	i.importedValidators = make(map[ids.NodeID]bool)
	i.importedTxs = make(map[ids.ID]bool)
	i.initialized = true
	return nil
}

func (i *Importer) ImportConfig(config *migrate.Config) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}
	// P-Chain configuration is set at network genesis
	// Additional runtime config changes not typically supported via RPC
	return nil
}

// ImportBlock imports a P-Chain block
func (i *Importer) ImportBlock(block *migrate.BlockData) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// For P-Chain, we typically import transactions rather than blocks
	// If block body contains raw block data, attempt to import it
	if len(block.Body) > 0 {
		return i.importRawBlock(block.Body)
	}

	// If transactions are available, import them individually
	for _, tx := range block.Transactions {
		if err := i.importTransaction(tx); err != nil {
			return fmt.Errorf("failed to import transaction %s: %w", tx.Hash.Hex(), err)
		}
	}

	return nil
}

// ImportBlocks imports multiple P-Chain blocks
func (i *Importer) ImportBlocks(blocks []*migrate.BlockData) error {
	for _, block := range blocks {
		if err := i.ImportBlock(block); err != nil {
			return err
		}
	}
	return nil
}

// ImportState imports P-Chain state (validators, subnets, etc.)
func (i *Importer) ImportState(accounts []*migrate.Account, blockNumber uint64) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// P-Chain state is encoded in accounts' Code field
	for _, account := range accounts {
		// Check for P-Chain marker address
		if account.Address.Hex() == "0x0000000000000000000000000000000000000001" {
			if len(account.Code) > 0 {
				var pchainData PChainData
				if err := json.Unmarshal(account.Code, &pchainData); err != nil {
					return fmt.Errorf("failed to unmarshal P-Chain data: %w", err)
				}
				return i.importPChainData(&pchainData)
			}
		}
	}

	return nil
}

// FinalizeImport finalizes the P-Chain import
func (i *Importer) FinalizeImport(blockNumber uint64) error {
	// Verify all imported validators are active
	return i.VerifyImport(blockNumber)
}

// VerifyImport verifies the P-Chain import
func (i *Importer) VerifyImport(blockNumber uint64) error {
	if !i.initialized {
		return migrate.ErrNotInitialized
	}

	// Get current height
	height, err := i.getHeight()
	if err != nil {
		return fmt.Errorf("failed to get height: %w", err)
	}

	if height < blockNumber {
		return fmt.Errorf("chain height %d is less than expected %d", height, blockNumber)
	}

	// Verify imported validators exist
	for nodeID := range i.importedValidators {
		exists, err := i.validatorExists(nodeID)
		if err != nil {
			return fmt.Errorf("failed to check validator %s: %w", nodeID, err)
		}
		if !exists {
			return fmt.Errorf("validator %s not found after import", nodeID)
		}
	}

	// Verify imported transactions
	for txID := range i.importedTxs {
		status, err := i.getTxStatus(txID)
		if err != nil {
			return fmt.Errorf("failed to get tx status for %s: %w", txID, err)
		}
		if status != "Committed" && status != "Accepted" {
			return fmt.Errorf("transaction %s has unexpected status: %s", txID, status)
		}
	}

	return nil
}

// ExecuteBlock executes/replays a P-Chain block
func (i *Importer) ExecuteBlock(block *migrate.BlockData) error {
	// For P-Chain, execution is handled by the node when transactions are submitted
	return i.ImportBlock(block)
}

// Close closes the importer
func (i *Importer) Close() error {
	i.initialized = false
	i.importedValidators = nil
	i.importedTxs = nil
	return nil
}

// ImportValidator imports a validator to the P-Chain
func (i *Importer) ImportValidator(ctx context.Context, validator *ValidatorImport) (ids.ID, error) {
	if !i.initialized {
		return ids.Empty, migrate.ErrNotInitialized
	}

	var txType TxType
	if validator.PublicKey != nil {
		txType = TxTypeAddPermissionlessValidator
	} else {
		txType = TxTypeAddValidator
	}

	request := &ImportRequest{
		Type:      txType,
		Validator: validator,
	}

	txID, err := i.submitImportRequest(request)
	if err != nil {
		return ids.Empty, err
	}

	// Wait for transaction to be accepted
	if err := i.waitForTxAccepted(ctx, txID); err != nil {
		return txID, fmt.Errorf("transaction not accepted: %w", err)
	}

	i.importedValidators[validator.NodeID] = true
	i.importedTxs[txID] = true

	return txID, nil
}

// ImportDelegator imports a delegator to the P-Chain
func (i *Importer) ImportDelegator(ctx context.Context, delegator *ValidatorImport) (ids.ID, error) {
	if !i.initialized {
		return ids.Empty, migrate.ErrNotInitialized
	}

	var txType TxType
	if delegator.PublicKey != nil {
		txType = TxTypeAddPermissionlessDelegator
	} else {
		txType = TxTypeAddDelegator
	}

	request := &ImportRequest{
		Type:      txType,
		Validator: delegator,
	}

	txID, err := i.submitImportRequest(request)
	if err != nil {
		return ids.Empty, err
	}

	if err := i.waitForTxAccepted(ctx, txID); err != nil {
		return txID, fmt.Errorf("transaction not accepted: %w", err)
	}

	i.importedTxs[txID] = true

	return txID, nil
}

// ImportSubnet imports a subnet to the P-Chain
func (i *Importer) ImportSubnet(ctx context.Context, subnet *SubnetImport) (ids.ID, error) {
	if !i.initialized {
		return ids.Empty, migrate.ErrNotInitialized
	}

	request := &ImportRequest{
		Type:   TxTypeCreateSubnet,
		Subnet: subnet,
	}

	txID, err := i.submitImportRequest(request)
	if err != nil {
		return ids.Empty, err
	}

	if err := i.waitForTxAccepted(ctx, txID); err != nil {
		return txID, fmt.Errorf("transaction not accepted: %w", err)
	}

	i.importedTxs[txID] = true

	return txID, nil
}

// ImportChain imports a blockchain to the P-Chain
func (i *Importer) ImportChain(ctx context.Context, chain *ChainImport) (ids.ID, error) {
	if !i.initialized {
		return ids.Empty, migrate.ErrNotInitialized
	}

	request := &ImportRequest{
		Type:  TxTypeCreateChain,
		Chain: chain,
	}

	txID, err := i.submitImportRequest(request)
	if err != nil {
		return ids.Empty, err
	}

	if err := i.waitForTxAccepted(ctx, txID); err != nil {
		return txID, fmt.Errorf("transaction not accepted: %w", err)
	}

	i.importedTxs[txID] = true

	return txID, nil
}

// ImportRawTx imports a raw signed transaction to the P-Chain
func (i *Importer) ImportRawTx(ctx context.Context, txBytes []byte) (ids.ID, error) {
	if !i.initialized {
		return ids.Empty, migrate.ErrNotInitialized
	}

	txID, err := i.issueTx(txBytes)
	if err != nil {
		return ids.Empty, err
	}

	if err := i.waitForTxAccepted(ctx, txID); err != nil {
		return txID, fmt.Errorf("transaction not accepted: %w", err)
	}

	i.importedTxs[txID] = true

	return txID, nil
}

// importPChainData imports complete P-Chain data
func (i *Importer) importPChainData(data *PChainData) error {
	ctx := context.Background()

	// Import subnets first
	for _, subnet := range data.Subnets {
		// Skip primary network
		if subnet.ID == ids.Empty {
			continue
		}

		subnetImport := &SubnetImport{
			ControlKeys: subnet.ControlKeys,
			Threshold:   subnet.Threshold,
		}

		if _, err := i.ImportSubnet(ctx, subnetImport); err != nil {
			return fmt.Errorf("failed to import subnet %s: %w", subnet.ID, err)
		}
	}

	// Import blockchains
	for _, blockchain := range data.Blockchains {
		chainImport := &ChainImport{
			SubnetID:  blockchain.SubnetID,
			ChainName: blockchain.Name,
			VMID:      blockchain.VMID,
			FxIDs:     blockchain.FxIDs,
		}

		if _, err := i.ImportChain(ctx, chainImport); err != nil {
			return fmt.Errorf("failed to import blockchain %s: %w", blockchain.ID, err)
		}
	}

	// Import validators
	for _, validator := range data.Validators {
		validatorImport := &ValidatorImport{
			NodeID:        validator.NodeID,
			StartTime:     validator.StartTime,
			EndTime:       validator.EndTime,
			Weight:        validator.Weight,
			SubnetID:      validator.SubnetID,
			DelegationFee: uint32(validator.DelegationFee * 10000), // Convert to basis points
			PublicKey:     validator.PublicKey,
		}

		if validator.RewardOwner != nil {
			validatorImport.RewardOwner = &OwnerImport{
				Locktime:  validator.RewardOwner.Locktime,
				Threshold: validator.RewardOwner.Threshold,
				Addresses: validator.RewardOwner.Addresses,
			}
		}

		if _, err := i.ImportValidator(ctx, validatorImport); err != nil {
			return fmt.Errorf("failed to import validator %s: %w", validator.NodeID, err)
		}

		// Import delegators for this validator
		for _, delegator := range validator.Delegators {
			delegatorImport := &ValidatorImport{
				NodeID:    delegator.NodeID,
				StartTime: delegator.StartTime,
				EndTime:   delegator.EndTime,
				Weight:    delegator.Weight,
				SubnetID:  validator.SubnetID,
			}

			if delegator.RewardOwner != nil {
				delegatorImport.RewardOwner = &OwnerImport{
					Locktime:  delegator.RewardOwner.Locktime,
					Threshold: delegator.RewardOwner.Threshold,
					Addresses: delegator.RewardOwner.Addresses,
				}
			}

			if _, err := i.ImportDelegator(ctx, delegatorImport); err != nil {
				return fmt.Errorf("failed to import delegator %s: %w", delegator.TxID, err)
			}
		}
	}

	// Import pending validators
	for _, validator := range data.PendingValidators {
		validatorImport := &ValidatorImport{
			NodeID:        validator.NodeID,
			StartTime:     validator.StartTime,
			EndTime:       validator.EndTime,
			Weight:        validator.Weight,
			SubnetID:      validator.SubnetID,
			DelegationFee: uint32(validator.DelegationFee * 10000),
			PublicKey:     validator.PublicKey,
		}

		if validator.RewardOwner != nil {
			validatorImport.RewardOwner = &OwnerImport{
				Locktime:  validator.RewardOwner.Locktime,
				Threshold: validator.RewardOwner.Threshold,
				Addresses: validator.RewardOwner.Addresses,
			}
		}

		if _, err := i.ImportValidator(ctx, validatorImport); err != nil {
			return fmt.Errorf("failed to import pending validator %s: %w", validator.NodeID, err)
		}
	}

	return nil
}

// importRawBlock imports a raw P-Chain block
func (i *Importer) importRawBlock(blockBytes []byte) error {
	// Use lux_importBlock RPC if available
	result, err := i.callRPC("lux.importBlock", map[string]interface{}{
		"block":    hex.EncodeToString(blockBytes),
		"encoding": "hex",
	})
	if err != nil {
		return err
	}

	// Check for errors in result
	if resultMap, ok := result.(map[string]interface{}); ok {
		if errMsg, ok := resultMap["error"]; ok {
			return fmt.Errorf("import failed: %v", errMsg)
		}
	}

	return nil
}

// importTransaction imports a single transaction
func (i *Importer) importTransaction(tx *migrate.Transaction) error {
	if len(tx.Data) == 0 {
		return fmt.Errorf("transaction has no data")
	}

	txID, err := i.issueTx(tx.Data)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	return i.waitForTxAccepted(ctx, txID)
}

// submitImportRequest submits an import request and returns the transaction ID
func (i *Importer) submitImportRequest(request *ImportRequest) (ids.ID, error) {
	// For raw transaction bytes, use issueTx directly
	if request.TxBytes != "" {
		txBytes, err := hex.DecodeString(request.TxBytes)
		if err != nil {
			return ids.Empty, fmt.Errorf("invalid tx bytes: %w", err)
		}
		return i.issueTx(txBytes)
	}

	// For structured import requests, we need to build and sign the transaction
	// This requires access to keys which should be handled by the node or wallet
	return ids.Empty, fmt.Errorf("structured import requires signing - use raw transaction bytes")
}

// issueTx issues a signed transaction to the P-Chain
func (i *Importer) issueTx(txBytes []byte) (ids.ID, error) {
	result, err := i.callRPC("platform.issueTx", map[string]interface{}{
		"tx":       hex.EncodeToString(txBytes),
		"encoding": "hex",
	})
	if err != nil {
		return ids.Empty, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return ids.Empty, fmt.Errorf("unexpected response type")
	}

	txIDStr, ok := resultMap["txID"].(string)
	if !ok {
		return ids.Empty, fmt.Errorf("txID not found in response")
	}

	return ids.FromString(txIDStr)
}

// waitForTxAccepted waits for a transaction to be accepted
func (i *Importer) waitForTxAccepted(ctx context.Context, txID ids.ID) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			status, err := i.getTxStatus(txID)
			if err != nil {
				continue // Retry on transient errors
			}

			switch status {
			case "Committed", "Accepted":
				return nil
			case "Aborted", "Rejected", "Unknown":
				return fmt.Errorf("transaction %s was %s", txID, status)
			}
			// Processing, Dropped - continue waiting
		}
	}
}

// getTxStatus gets the status of a transaction
func (i *Importer) getTxStatus(txID ids.ID) (string, error) {
	result, err := i.callRPC("platform.getTxStatus", map[string]interface{}{
		"txID": txID.String(),
	})
	if err != nil {
		return "", err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("unexpected response type")
	}

	status, ok := resultMap["status"].(string)
	if !ok {
		return "", fmt.Errorf("status not found in response")
	}

	return status, nil
}

// getHeight gets the current P-Chain height
func (i *Importer) getHeight() (uint64, error) {
	result, err := i.callRPC("platform.getHeight", struct{}{})
	if err != nil {
		return 0, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("unexpected response type")
	}

	height, ok := resultMap["height"].(float64)
	if !ok {
		return 0, fmt.Errorf("height not found in response")
	}

	return uint64(height), nil
}

// validatorExists checks if a validator exists in the current set
func (i *Importer) validatorExists(nodeID ids.NodeID) (bool, error) {
	result, err := i.callRPC("platform.getCurrentValidators", map[string]interface{}{
		"nodeIDs": []string{nodeID.String()},
	})
	if err != nil {
		return false, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("unexpected response type")
	}

	validators, ok := resultMap["validators"].([]interface{})
	if !ok {
		return false, nil
	}

	return len(validators) > 0, nil
}

// callRPC makes an RPC call to the P-Chain
func (i *Importer) callRPC(method string, params interface{}) (interface{}, error) {
	request := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}

	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}

	// P-Chain endpoint
	url := i.config.RPCURL + "/ext/P"
	resp, err := i.client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, migrate.ErrRPCConnectionFailed
	}
	defer resp.Body.Close()

	var result struct {
		Result interface{}      `json:"result"`
		Error  *json.RawMessage `json:"error"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	if result.Error != nil {
		return nil, fmt.Errorf("RPC error: %s", string(*result.Error))
	}

	return result.Result, nil
}
