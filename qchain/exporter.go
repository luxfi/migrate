// Package qchain provides Q-Chain (Quantum Chain) export/import functionality
package qchain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sync"

	"github.com/luxfi/geth/common"
	"github.com/luxfi/migrate"
)

// Exporter exports Q-Chain data via RPC
type Exporter struct {
	config  migrate.ExporterConfig
	client  *http.Client
	chainID *big.Int
	mu      sync.Mutex
	inited  bool
}

// NewExporter creates a new Q-Chain exporter
func NewExporter(config migrate.ExporterConfig) (*Exporter, error) {
	return &Exporter{
		config: config,
		client: &http.Client{},
	}, nil
}

func (e *Exporter) Init(config migrate.ExporterConfig) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.inited {
		return fmt.Errorf("exporter already initialized")
	}

	e.config = config
	if e.config.RPCURL == "" {
		return fmt.Errorf("RPC URL required for Q-Chain export")
	}

	// Get chain ID
	result, err := e.callRPC("eth_chainId", []interface{}{})
	if err != nil {
		return fmt.Errorf("failed to get chain ID: %w", err)
	}

	chainIDHex, ok := result.(string)
	if !ok {
		return fmt.Errorf("invalid chain ID response")
	}

	e.chainID = new(big.Int)
	e.chainID.SetString(chainIDHex[2:], 16)
	e.inited = true
	return nil
}

func (e *Exporter) GetInfo() (*migrate.Info, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.inited {
		return nil, migrate.ErrNotInitialized
	}

	// Get block number
	result, err := e.callRPC("eth_blockNumber", []interface{}{})
	if err != nil {
		return nil, err
	}

	blockNumHex := result.(string)
	headBlock := new(big.Int)
	headBlock.SetString(blockNumHex[2:], 16)

	// Get genesis block
	genesisResult, err := e.callRPC("eth_getBlockByNumber", []interface{}{"0x0", false})
	if err != nil {
		return nil, err
	}

	genesisBlock := genesisResult.(map[string]interface{})
	genesisHash := common.HexToHash(genesisBlock["hash"].(string))

	return &migrate.Info{
		VMType:      migrate.VMTypeQChain,
		ChainID:     e.chainID,
		NetworkID:   e.chainID.Uint64(),
		GenesisHash: genesisHash,
		CurrentHeight: headBlock.Uint64(),
	}, nil
}

func (e *Exporter) ExportBlocks(ctx context.Context, start, end uint64) (<-chan *migrate.BlockData, <-chan error) {
	blocks := make(chan *migrate.BlockData, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(blocks)
		defer close(errs)

		e.mu.Lock()
		inited := e.inited
		e.mu.Unlock()

		if !inited {
			errs <- migrate.ErrNotInitialized
			return
		}

		if start > end {
			errs <- fmt.Errorf("invalid block range: start %d > end %d", start, end)
			return
		}

		for num := start; num <= end; num++ {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
			}

			block, err := e.exportBlock(num)
			if err != nil {
				errs <- fmt.Errorf("failed to export block %d: %w", num, err)
				return
			}

			select {
			case blocks <- block:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}
	}()

	return blocks, errs
}

func (e *Exporter) exportBlock(number uint64) (*migrate.BlockData, error) {
	// Get block with transactions
	result, err := e.callRPC("eth_getBlockByNumber", []interface{}{
		fmt.Sprintf("0x%x", number),
		true,
	})
	if err != nil {
		return nil, err
	}

	if result == nil {
		return nil, migrate.ErrBlockNotFound
	}

	blockData := result.(map[string]interface{})

	block := &migrate.BlockData{
		Number:     number,
		Hash:       common.HexToHash(blockData["hash"].(string)),
		ParentHash: common.HexToHash(blockData["parentHash"].(string)),
		StateRoot:  common.HexToHash(blockData["stateRoot"].(string)),
		TransactionsRoot: common.HexToHash(blockData["transactionsRoot"].(string)),
		Extensions: make(map[string]interface{}),
	}

	// Parse timestamp
	if ts, ok := blockData["timestamp"].(string); ok {
		timestamp := new(big.Int)
		timestamp.SetString(ts[2:], 16)
		block.Timestamp = timestamp.Uint64()
	}

	// Parse gas used/limit
	if gasUsed, ok := blockData["gasUsed"].(string); ok {
		gu := new(big.Int)
		gu.SetString(gasUsed[2:], 16)
		block.GasUsed = gu.Uint64()
	}

	if gasLimit, ok := blockData["gasLimit"].(string); ok {
		gl := new(big.Int)
		gl.SetString(gasLimit[2:], 16)
		block.GasLimit = gl.Uint64()
	}

	// Parse miner
	if miner, ok := blockData["miner"].(string); ok {
		block.Coinbase = common.HexToAddress(miner)
	}

	// Parse transactions
	if txs, ok := blockData["transactions"].([]interface{}); ok {
		for _, tx := range txs {
			txData := tx.(map[string]interface{})
			block.Transactions = append(block.Transactions, e.convertTransaction(txData))
		}
	}

	return block, nil
}

func (e *Exporter) convertTransaction(txData map[string]interface{}) *migrate.Transaction {
	tx := &migrate.Transaction{}

	if hash, ok := txData["hash"].(string); ok {
		tx.Hash = common.HexToHash(hash)
	}
	if from, ok := txData["from"].(string); ok {
		tx.From = common.HexToAddress(from)
	}
	if to, ok := txData["to"].(string); ok && to != "" {
		addr := common.HexToAddress(to)
		tx.To = &addr
	}
	if value, ok := txData["value"].(string); ok {
		tx.Value = new(big.Int)
		tx.Value.SetString(value[2:], 16)
	}
	if gas, ok := txData["gas"].(string); ok {
		g := new(big.Int)
		g.SetString(gas[2:], 16)
		tx.Gas = g.Uint64()
	}
	if gasPrice, ok := txData["gasPrice"].(string); ok {
		tx.GasPrice = new(big.Int)
		tx.GasPrice.SetString(gasPrice[2:], 16)
	}
	if nonce, ok := txData["nonce"].(string); ok {
		n := new(big.Int)
		n.SetString(nonce[2:], 16)
		tx.Nonce = n.Uint64()
	}
	if input, ok := txData["input"].(string); ok {
		tx.Data = common.FromHex(input)
	}

	return tx
}

func (e *Exporter) ExportState(ctx context.Context, blockNumber uint64) (<-chan *migrate.Account, <-chan error) {
	accounts := make(chan *migrate.Account, 100)
	errs := make(chan error, 1)

	go func() {
		defer close(accounts)
		defer close(errs)

		e.mu.Lock()
		inited := e.inited
		e.mu.Unlock()

		if !inited {
			errs <- migrate.ErrNotInitialized
			return
		}

		// Use debug_accountRange to iterate accounts
		var startKey common.Hash
		blockHex := fmt.Sprintf("0x%x", blockNumber)

		for {
			select {
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			default:
			}

			result, err := e.callRPC("debug_accountRange", []interface{}{
				blockHex,
				startKey.Hex(),
				100,
				false,
				false,
				false,
			})
			if err != nil {
				// debug_accountRange may not be available
				errs <- fmt.Errorf("debug_accountRange not available: %w", err)
				return
			}

			resp := result.(map[string]interface{})
			accts := resp["accounts"].(map[string]interface{})

			for addrHex, acctData := range accts {
				acct := acctData.(map[string]interface{})
				addr := common.HexToAddress(addrHex)

				account := &migrate.Account{
					Address: addr,
				}

				if balance, ok := acct["balance"].(string); ok {
					account.Balance = new(big.Int)
					account.Balance.SetString(balance[2:], 16)
				}

				if nonce, ok := acct["nonce"].(float64); ok {
					account.Nonce = uint64(nonce)
				}

				if codeHash, ok := acct["codeHash"].(string); ok {
					account.CodeHash = common.HexToHash(codeHash)
				}

				select {
				case accounts <- account:
				case <-ctx.Done():
					errs <- ctx.Err()
					return
				}
			}

			// Check for next key
			nextKey, hasNext := resp["next"].(string)
			if !hasNext || nextKey == "" {
				break
			}
			startKey = common.HexToHash(nextKey)
		}
	}()

	return accounts, errs
}

func (e *Exporter) ExportAccount(ctx context.Context, address common.Address, blockNumber uint64) (*migrate.Account, error) {
	e.mu.Lock()
	inited := e.inited
	e.mu.Unlock()

	if !inited {
		return nil, migrate.ErrNotInitialized
	}

	blockHex := fmt.Sprintf("0x%x", blockNumber)

	// Get balance
	balanceResult, err := e.callRPC("eth_getBalance", []interface{}{address.Hex(), blockHex})
	if err != nil {
		return nil, err
	}

	balance := new(big.Int)
	if balanceHex, ok := balanceResult.(string); ok {
		balance.SetString(balanceHex[2:], 16)
	}

	// Get nonce
	nonceResult, err := e.callRPC("eth_getTransactionCount", []interface{}{address.Hex(), blockHex})
	if err != nil {
		return nil, err
	}

	nonce := uint64(0)
	if nonceHex, ok := nonceResult.(string); ok {
		n := new(big.Int)
		n.SetString(nonceHex[2:], 16)
		nonce = n.Uint64()
	}

	// Get code
	codeResult, err := e.callRPC("eth_getCode", []interface{}{address.Hex(), blockHex})
	if err != nil {
		return nil, err
	}

	var code []byte
	if codeHex, ok := codeResult.(string); ok {
		code = common.FromHex(codeHex)
	}

	return &migrate.Account{
		Address:  address,
		Balance:  balance,
		Nonce:    nonce,
		Code:     code,
		CodeHash: common.BytesToHash(code),
	}, nil
}

func (e *Exporter) ExportConfig() (*migrate.Config, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.inited {
		return nil, migrate.ErrNotInitialized
	}

	return &migrate.Config{
		ChainID:   e.chainID,
		NetworkID: e.chainID.Uint64(),
	}, nil
}

func (e *Exporter) VerifyExport(blockNumber uint64) error {
	e.mu.Lock()
	inited := e.inited
	e.mu.Unlock()

	if !inited {
		return migrate.ErrNotInitialized
	}

	result, err := e.callRPC("eth_getBlockByNumber", []interface{}{
		fmt.Sprintf("0x%x", blockNumber),
		false,
	})
	if err != nil {
		return err
	}

	if result == nil {
		return migrate.ErrBlockNotFound
	}

	return nil
}

func (e *Exporter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inited = false
	return nil
}

func (e *Exporter) callRPC(method string, params []interface{}) (interface{}, error) {
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

	resp, err := e.client.Post(e.config.RPCURL, "application/json", bytes.NewReader(body))
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

func init() {
	migrate.RegisterExporterFactory(migrate.VMTypeQChain, func(config migrate.ExporterConfig) (migrate.Exporter, error) {
		return NewExporter(config)
	})
}
