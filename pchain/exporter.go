// Copyright (C) 2019-2025, Lux Industries, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

// Package pchain provides P-Chain-specific export/import functionality
// for migrating validator sets, subnets, and blockchain data.
package pchain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"time"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/ids"
	"github.com/luxfi/migrate"
)

// PChainData contains P-Chain specific data for export
type PChainData struct {
	// Validators on the primary network
	Validators []ValidatorInfo `json:"validators"`
	// Pending validators
	PendingValidators []ValidatorInfo `json:"pendingValidators"`
	// Subnets
	Subnets []SubnetInfo `json:"subnets"`
	// Blockchains
	Blockchains []BlockchainInfo `json:"blockchains"`
	// Chain height
	Height uint64 `json:"height"`
	// Current timestamp
	Timestamp time.Time `json:"timestamp"`
	// Current supply
	CurrentSupply uint64 `json:"currentSupply"`
	// Fee configuration
	FeeConfig *FeeConfig `json:"feeConfig,omitempty"`
}

// ValidatorInfo represents a validator's complete information
type ValidatorInfo struct {
	TxID            ids.ID     `json:"txID"`
	NodeID          ids.NodeID `json:"nodeID"`
	StartTime       uint64     `json:"startTime"`
	EndTime         uint64     `json:"endTime"`
	Weight          uint64     `json:"weight"`
	StakeAmount     uint64     `json:"stakeAmount"`
	PotentialReward uint64     `json:"potentialReward,omitempty"`
	DelegationFee   float32    `json:"delegationFee"`
	Uptime          float32    `json:"uptime,omitempty"`
	Connected       bool       `json:"connected"`
	SubnetID        ids.ID     `json:"subnetID"`
	// Delegators staking to this validator
	Delegators []DelegatorInfo `json:"delegators,omitempty"`
	// Reward owner information
	RewardOwner *OwnerInfo `json:"rewardOwner,omitempty"`
	// BLS public key (for permissionless validators)
	PublicKey []byte `json:"publicKey,omitempty"`
}

// DelegatorInfo represents a delegator's information
type DelegatorInfo struct {
	TxID            ids.ID     `json:"txID"`
	NodeID          ids.NodeID `json:"nodeID"`
	StartTime       uint64     `json:"startTime"`
	EndTime         uint64     `json:"endTime"`
	Weight          uint64     `json:"weight"`
	PotentialReward uint64     `json:"potentialReward,omitempty"`
	RewardOwner     *OwnerInfo `json:"rewardOwner,omitempty"`
}

// OwnerInfo represents a reward owner
type OwnerInfo struct {
	Locktime  uint64          `json:"locktime"`
	Threshold uint32          `json:"threshold"`
	Addresses []ids.ShortID   `json:"addresses"`
}

// SubnetInfo represents a subnet's information
type SubnetInfo struct {
	ID           ids.ID        `json:"id"`
	ControlKeys  []ids.ShortID `json:"controlKeys"`
	Threshold    uint32        `json:"threshold"`
	Locktime     uint64        `json:"locktime"`
	IsPermissioned bool        `json:"isPermissioned"`
	// For L1 subnets
	ConversionID   ids.ID `json:"conversionID,omitempty"`
	ManagerChainID ids.ID `json:"managerChainID,omitempty"`
	ManagerAddress []byte `json:"managerAddress,omitempty"`
}

// BlockchainInfo represents a blockchain's information
type BlockchainInfo struct {
	ID       ids.ID `json:"id"`
	Name     string `json:"name"`
	SubnetID ids.ID `json:"subnetID"`
	VMID     ids.ID `json:"vmID"`
	FxIDs    []ids.ID `json:"fxIDs,omitempty"`
	Status   string `json:"status"`
}

// FeeConfig represents the P-Chain fee configuration
type FeeConfig struct {
	Capacity                 uint64 `json:"capacity"`
	MaxPerSecond             uint64 `json:"maxPerSecond"`
	TargetPerSecond          uint64 `json:"targetPerSecond"`
	MinPrice                 uint64 `json:"minPrice"`
	ExcessConversionConstant uint64 `json:"excessConversionConstant"`
}

// Exporter exports data from P-Chain via RPC
type Exporter struct {
	config      migrate.ExporterConfig
	client      *http.Client
	initialized bool
}

// NewExporter creates a new P-Chain exporter
func NewExporter(config migrate.ExporterConfig) (*Exporter, error) {
	if config.RPCURL == "" {
		return nil, fmt.Errorf("RPC URL required for P-Chain export")
	}
	return &Exporter{
		config: config,
		client: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func (e *Exporter) Init(config migrate.ExporterConfig) error {
	if e.initialized {
		return migrate.ErrAlreadyInitialized
	}
	e.config = config
	if config.RPCURL == "" {
		return fmt.Errorf("RPC URL required for P-Chain export")
	}
	e.client = &http.Client{Timeout: 30 * time.Second}
	e.initialized = true
	return nil
}

func (e *Exporter) GetInfo() (*migrate.Info, error) {
	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	height, err := e.getHeight()
	if err != nil {
		return nil, fmt.Errorf("failed to get height: %w", err)
	}

	timestamp, err := e.getTimestamp()
	if err != nil {
		return nil, fmt.Errorf("failed to get timestamp: %w", err)
	}

	return &migrate.Info{
		VMType:        migrate.VMTypePChain,
		CurrentHeight: height,
		VMVersion:     "platformvm",
		DatabaseType:  "rpc",
		Extensions: map[string]interface{}{
			"timestamp": timestamp,
		},
	}, nil
}

// ExportBlocks exports P-Chain blocks in a range
func (e *Exporter) ExportBlocks(ctx context.Context, start, end uint64) (<-chan *migrate.BlockData, <-chan error) {
	blocks := make(chan *migrate.BlockData, 100)
	errs := make(chan error, 1)

	if start > end {
		errs <- migrate.ErrInvalidBlockRange
		close(blocks)
		close(errs)
		return blocks, errs
	}

	go func() {
		defer close(blocks)
		defer close(errs)

		for height := start; height <= end; height++ {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
				block, err := e.getBlockByHeight(height)
				if err != nil {
					errs <- fmt.Errorf("failed to get block %d: %w", height, err)
					return
				}
				blocks <- block
			}
		}
	}()

	return blocks, errs
}

// ExportState exports P-Chain state (validators, subnets, etc.) at a specific height
func (e *Exporter) ExportState(ctx context.Context, blockNumber uint64) (<-chan *migrate.Account, <-chan error) {
	accounts := make(chan *migrate.Account, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(accounts)
		defer close(errs)

		// P-Chain doesn't have traditional accounts like EVM chains.
		// State is represented as validators, subnets, and UTXOs.
		// We export this data as account extensions.

		pchainData, err := e.ExportPChainData(ctx)
		if err != nil {
			errs <- fmt.Errorf("failed to export P-Chain data: %w", err)
			return
		}

		// Encode P-Chain data as a special "state account"
		dataBytes, err := json.Marshal(pchainData)
		if err != nil {
			errs <- fmt.Errorf("failed to marshal P-Chain data: %w", err)
			return
		}

		// Create a virtual account representing P-Chain state
		accounts <- &migrate.Account{
			Address: common.HexToAddress("0x0000000000000000000000000000000000000001"), // P-Chain marker
			Balance: big.NewInt(int64(pchainData.CurrentSupply)),
			Code:    dataBytes,
			Storage: make(map[common.Hash]common.Hash),
		}
	}()

	return accounts, errs
}

// ExportAccount is not applicable for P-Chain (no EVM accounts)
func (e *Exporter) ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*migrate.Account, error) {
	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}
	// P-Chain doesn't have EVM-style accounts
	return nil, fmt.Errorf("P-Chain does not support EVM accounts")
}

// ExportConfig exports P-Chain configuration
func (e *Exporter) ExportConfig() (*migrate.Config, error) {
	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	feeConfig, err := e.getFeeConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to get fee config: %w", err)
	}

	return &migrate.Config{
		NetworkID: e.config.NetworkID,
		Extensions: map[string]interface{}{
			"feeConfig": feeConfig,
		},
	}, nil
}

// VerifyExport verifies export integrity at a block height
func (e *Exporter) VerifyExport(blockNumber uint64) error {
	_, err := e.getBlockByHeight(blockNumber)
	return err
}

// Close closes the exporter
func (e *Exporter) Close() error {
	e.initialized = false
	return nil
}

// ExportPChainData exports complete P-Chain data
func (e *Exporter) ExportPChainData(ctx context.Context) (*PChainData, error) {
	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}

	height, err := e.getHeight()
	if err != nil {
		return nil, fmt.Errorf("failed to get height: %w", err)
	}

	timestamp, err := e.getTimestamp()
	if err != nil {
		return nil, fmt.Errorf("failed to get timestamp: %w", err)
	}

	// Get primary network validators
	validators, err := e.getCurrentValidators(ids.Empty) // Empty ID = primary network
	if err != nil {
		return nil, fmt.Errorf("failed to get validators: %w", err)
	}

	// Get pending validators
	pendingValidators, err := e.getPendingValidators(ids.Empty)
	if err != nil {
		return nil, fmt.Errorf("failed to get pending validators: %w", err)
	}

	// Get subnets
	subnets, err := e.getSubnets()
	if err != nil {
		return nil, fmt.Errorf("failed to get subnets: %w", err)
	}

	// Get blockchains
	blockchains, err := e.getBlockchains()
	if err != nil {
		return nil, fmt.Errorf("failed to get blockchains: %w", err)
	}

	// Get current supply
	supply, _, err := e.getCurrentSupply(ids.Empty)
	if err != nil {
		return nil, fmt.Errorf("failed to get current supply: %w", err)
	}

	// Get fee config
	feeConfig, err := e.getFeeConfig()
	if err != nil {
		// Fee config might not be available on all networks
		feeConfig = nil
	}

	return &PChainData{
		Validators:        validators,
		PendingValidators: pendingValidators,
		Subnets:           subnets,
		Blockchains:       blockchains,
		Height:            height,
		Timestamp:         timestamp,
		CurrentSupply:     supply,
		FeeConfig:         feeConfig,
	}, nil
}

// ExportValidators exports all validators for a subnet (primary network if subnetID is empty)
func (e *Exporter) ExportValidators(ctx context.Context, subnetID ids.ID) ([]ValidatorInfo, error) {
	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}
	return e.getCurrentValidators(subnetID)
}

// ExportSubnets exports all subnet information
func (e *Exporter) ExportSubnets(ctx context.Context) ([]SubnetInfo, error) {
	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}
	return e.getSubnets()
}

// ExportBlockchains exports all blockchain information
func (e *Exporter) ExportBlockchains(ctx context.Context) ([]BlockchainInfo, error) {
	if !e.initialized {
		return nil, migrate.ErrNotInitialized
	}
	return e.getBlockchains()
}

// RPC helper methods

func (e *Exporter) getHeight() (uint64, error) {
	result, err := e.callRPC("platform.getHeight", struct{}{})
	if err != nil {
		return 0, err
	}
	heightMap, ok := result.(map[string]interface{})
	if !ok {
		return 0, fmt.Errorf("unexpected response type")
	}
	height, ok := heightMap["height"].(float64)
	if !ok {
		return 0, fmt.Errorf("height not found in response")
	}
	return uint64(height), nil
}

func (e *Exporter) getTimestamp() (time.Time, error) {
	result, err := e.callRPC("platform.getTimestamp", struct{}{})
	if err != nil {
		return time.Time{}, err
	}
	timestampMap, ok := result.(map[string]interface{})
	if !ok {
		return time.Time{}, fmt.Errorf("unexpected response type")
	}
	timestampStr, ok := timestampMap["timestamp"].(string)
	if !ok {
		return time.Time{}, fmt.Errorf("timestamp not found in response")
	}
	return time.Parse(time.RFC3339, timestampStr)
}

func (e *Exporter) getBlockByHeight(height uint64) (*migrate.BlockData, error) {
	result, err := e.callRPC("platform.getBlockByHeight", map[string]interface{}{
		"height":   height,
		"encoding": "hex",
	})
	if err != nil {
		return nil, err
	}

	blockMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	blockHex, ok := blockMap["block"].(string)
	if !ok {
		return nil, fmt.Errorf("block not found in response")
	}

	// Decode hex block data
	blockBytes := common.FromHex(blockHex)

	return &migrate.BlockData{
		Number:     height,
		Body:       blockBytes,
		Extensions: map[string]interface{}{
			"encoding": blockMap["encoding"],
		},
	}, nil
}

func (e *Exporter) getCurrentValidators(subnetID ids.ID) ([]ValidatorInfo, error) {
	params := map[string]interface{}{}
	if subnetID != ids.Empty {
		params["subnetID"] = subnetID.String()
	}

	result, err := e.callRPC("platform.getCurrentValidators", params)
	if err != nil {
		return nil, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	validatorsRaw, ok := resultMap["validators"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("validators not found in response")
	}

	validators := make([]ValidatorInfo, 0, len(validatorsRaw))
	for _, v := range validatorsRaw {
		vMap, ok := v.(map[string]interface{})
		if !ok {
			continue
		}

		validator := ValidatorInfo{
			SubnetID: subnetID,
		}

		if txID, ok := vMap["txID"].(string); ok {
			validator.TxID, _ = ids.FromString(txID)
		}
		if nodeID, ok := vMap["nodeID"].(string); ok {
			validator.NodeID, _ = ids.NodeIDFromString(nodeID)
		}
		if startTime, ok := vMap["startTime"].(float64); ok {
			validator.StartTime = uint64(startTime)
		}
		if endTime, ok := vMap["endTime"].(float64); ok {
			validator.EndTime = uint64(endTime)
		}
		if weight, ok := vMap["weight"].(float64); ok {
			validator.Weight = uint64(weight)
		}
		if stakeAmount, ok := vMap["stakeAmount"].(float64); ok {
			validator.StakeAmount = uint64(stakeAmount)
		}
		if potentialReward, ok := vMap["potentialReward"].(float64); ok {
			validator.PotentialReward = uint64(potentialReward)
		}
		if delegationFee, ok := vMap["delegationFee"].(float64); ok {
			validator.DelegationFee = float32(delegationFee)
		}
		if uptime, ok := vMap["uptime"].(float64); ok {
			validator.Uptime = float32(uptime)
		}
		if connected, ok := vMap["connected"].(bool); ok {
			validator.Connected = connected
		}

		// Parse delegators if present
		if delegatorsRaw, ok := vMap["delegators"].([]interface{}); ok {
			validator.Delegators = make([]DelegatorInfo, 0, len(delegatorsRaw))
			for _, d := range delegatorsRaw {
				dMap, ok := d.(map[string]interface{})
				if !ok {
					continue
				}
				delegator := DelegatorInfo{}
				if txID, ok := dMap["txID"].(string); ok {
					delegator.TxID, _ = ids.FromString(txID)
				}
				if nodeID, ok := dMap["nodeID"].(string); ok {
					delegator.NodeID, _ = ids.NodeIDFromString(nodeID)
				}
				if startTime, ok := dMap["startTime"].(float64); ok {
					delegator.StartTime = uint64(startTime)
				}
				if endTime, ok := dMap["endTime"].(float64); ok {
					delegator.EndTime = uint64(endTime)
				}
				if weight, ok := dMap["weight"].(float64); ok {
					delegator.Weight = uint64(weight)
				}
				if potentialReward, ok := dMap["potentialReward"].(float64); ok {
					delegator.PotentialReward = uint64(potentialReward)
				}
				validator.Delegators = append(validator.Delegators, delegator)
			}
		}

		validators = append(validators, validator)
	}

	return validators, nil
}

func (e *Exporter) getPendingValidators(subnetID ids.ID) ([]ValidatorInfo, error) {
	params := map[string]interface{}{}
	if subnetID != ids.Empty {
		params["subnetID"] = subnetID.String()
	}

	result, err := e.callRPC("platform.getPendingValidators", params)
	if err != nil {
		return nil, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	validatorsRaw, ok := resultMap["validators"].([]interface{})
	if !ok {
		// No pending validators
		return []ValidatorInfo{}, nil
	}

	validators := make([]ValidatorInfo, 0, len(validatorsRaw))
	for _, v := range validatorsRaw {
		vMap, ok := v.(map[string]interface{})
		if !ok {
			continue
		}

		validator := ValidatorInfo{
			SubnetID: subnetID,
		}

		if txID, ok := vMap["txID"].(string); ok {
			validator.TxID, _ = ids.FromString(txID)
		}
		if nodeID, ok := vMap["nodeID"].(string); ok {
			validator.NodeID, _ = ids.NodeIDFromString(nodeID)
		}
		if startTime, ok := vMap["startTime"].(float64); ok {
			validator.StartTime = uint64(startTime)
		}
		if endTime, ok := vMap["endTime"].(float64); ok {
			validator.EndTime = uint64(endTime)
		}
		if weight, ok := vMap["weight"].(float64); ok {
			validator.Weight = uint64(weight)
		}
		if stakeAmount, ok := vMap["stakeAmount"].(float64); ok {
			validator.StakeAmount = uint64(stakeAmount)
		}
		if delegationFee, ok := vMap["delegationFee"].(float64); ok {
			validator.DelegationFee = float32(delegationFee)
		}

		validators = append(validators, validator)
	}

	return validators, nil
}

func (e *Exporter) getSubnets() ([]SubnetInfo, error) {
	result, err := e.callRPC("platform.getSubnets", struct{}{})
	if err != nil {
		return nil, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	subnetsRaw, ok := resultMap["subnets"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("subnets not found in response")
	}

	subnets := make([]SubnetInfo, 0, len(subnetsRaw))
	for _, s := range subnetsRaw {
		sMap, ok := s.(map[string]interface{})
		if !ok {
			continue
		}

		subnet := SubnetInfo{}

		if id, ok := sMap["id"].(string); ok {
			subnet.ID, _ = ids.FromString(id)
		}
		if threshold, ok := sMap["threshold"].(float64); ok {
			subnet.Threshold = uint32(threshold)
		}
		if locktime, ok := sMap["locktime"].(float64); ok {
			subnet.Locktime = uint64(locktime)
		}

		// Parse control keys
		if controlKeysRaw, ok := sMap["controlKeys"].([]interface{}); ok {
			subnet.ControlKeys = make([]ids.ShortID, 0, len(controlKeysRaw))
			for _, k := range controlKeysRaw {
				if keyStr, ok := k.(string); ok {
					if keyID, err := ids.ShortFromString(keyStr); err == nil {
						subnet.ControlKeys = append(subnet.ControlKeys, keyID)
					}
				}
			}
		}

		subnets = append(subnets, subnet)
	}

	return subnets, nil
}

func (e *Exporter) getBlockchains() ([]BlockchainInfo, error) {
	result, err := e.callRPC("platform.getBlockchains", struct{}{})
	if err != nil {
		return nil, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	blockchainsRaw, ok := resultMap["blockchains"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("blockchains not found in response")
	}

	blockchains := make([]BlockchainInfo, 0, len(blockchainsRaw))
	for _, b := range blockchainsRaw {
		bMap, ok := b.(map[string]interface{})
		if !ok {
			continue
		}

		blockchain := BlockchainInfo{}

		if id, ok := bMap["id"].(string); ok {
			blockchain.ID, _ = ids.FromString(id)
		}
		if name, ok := bMap["name"].(string); ok {
			blockchain.Name = name
		}
		if subnetID, ok := bMap["subnetID"].(string); ok {
			blockchain.SubnetID, _ = ids.FromString(subnetID)
		}
		if vmID, ok := bMap["vmID"].(string); ok {
			blockchain.VMID, _ = ids.FromString(vmID)
		}

		blockchains = append(blockchains, blockchain)
	}

	return blockchains, nil
}

func (e *Exporter) getCurrentSupply(subnetID ids.ID) (uint64, uint64, error) {
	params := map[string]interface{}{}
	if subnetID != ids.Empty {
		params["subnetID"] = subnetID.String()
	}

	result, err := e.callRPC("platform.getCurrentSupply", params)
	if err != nil {
		return 0, 0, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return 0, 0, fmt.Errorf("unexpected response type")
	}

	supply, ok := resultMap["supply"].(float64)
	if !ok {
		return 0, 0, fmt.Errorf("supply not found in response")
	}

	height, _ := resultMap["height"].(float64)

	return uint64(supply), uint64(height), nil
}

func (e *Exporter) getFeeConfig() (*FeeConfig, error) {
	result, err := e.callRPC("platform.getFeeConfig", struct{}{})
	if err != nil {
		return nil, err
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response type")
	}

	config := &FeeConfig{}

	if capacity, ok := resultMap["capacity"].(float64); ok {
		config.Capacity = uint64(capacity)
	}
	if maxPerSecond, ok := resultMap["maxPerSecond"].(float64); ok {
		config.MaxPerSecond = uint64(maxPerSecond)
	}
	if targetPerSecond, ok := resultMap["targetPerSecond"].(float64); ok {
		config.TargetPerSecond = uint64(targetPerSecond)
	}
	if minPrice, ok := resultMap["minPrice"].(float64); ok {
		config.MinPrice = uint64(minPrice)
	}
	if excessConversionConstant, ok := resultMap["excessConversionConstant"].(float64); ok {
		config.ExcessConversionConstant = uint64(excessConversionConstant)
	}

	return config, nil
}

func (e *Exporter) callRPC(method string, params interface{}) (interface{}, error) {
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
	url := e.config.RPCURL + "/ext/P"
	resp, err := e.client.Post(url, "application/json", bytes.NewReader(body))
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
