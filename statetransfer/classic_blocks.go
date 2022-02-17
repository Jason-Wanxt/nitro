package statetransfer

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

type TransactionResults struct {
	Transactions []types.ArbitrumLegacyTransactionResult `json:"transactions" gencodec:"required"`
}

func ReadBlockFromClassic(ctx context.Context, rpcClient *rpc.Client, blockNumber *big.Int) (*StoredBlock, error) {
	var raw json.RawMessage
	client := ethclient.NewClient(rpcClient)
	err := rpcClient.CallContext(ctx, &raw, "eth_getBlockByNumber", hexutil.EncodeBig(blockNumber), true)
	if err != nil {
		return nil, err
	}
	var blockHeader types.Header
	var transactionResults TransactionResults // dont calculate txhashes alone
	if err := json.Unmarshal(raw, &blockHeader); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &transactionResults); err != nil {
		return nil, err
	}
	var receipts types.Receipts
	var txs []types.ArbitrumLegacyTransactionResult
	for _, tx := range transactionResults.Transactions {
		reciept, err := client.TransactionReceipt(ctx, tx.Hash)
		if err != nil { // we might just skip that receipt, but let's find one first
			return nil, err
		}
		if reciept.BlockNumber.Cmp(blockNumber) != 0 {
			// duplicate Txhash. Skip.
			continue
		}
		txs = append(txs, tx)
		receipts = append(receipts, reciept)
	}
	return &StoredBlock{
		Header:       blockHeader,
		Transactions: txs,
		Reciepts:     receipts,
	}, nil
}

func scanAndCopyBlocks(reader StoredBlockReader, writer *JsonListWriter) (int64, common.Hash, error) {
	blockNum := int64(0)
	lastHash := common.Hash{}
	for reader.More() {
		block, err := reader.GetNext()
		if err != nil {
			return blockNum, lastHash, err
		}
		if block.Header.Number.Cmp(big.NewInt(blockNum)) != 0 {
			return blockNum, lastHash, fmt.Errorf("unexpected block number in input: %v", block.Header.Number)
		}
		if block.Header.ParentHash != lastHash {
			return blockNum, lastHash, fmt.Errorf("unexpected prev block hash in input: %v", block.Header.ParentHash)
		}
		err = writer.Write(block)
		if err != nil {
			return blockNum, lastHash, err
		}
		lastHash = block.Header.Hash()
		blockNum++
	}
	return blockNum, lastHash, nil
}

func fillBlocks(ctx context.Context, rpcClient *rpc.Client, fromBlock, toBlock uint64, prevHash common.Hash, writer *JsonListWriter) error {
	for blockNum := fromBlock; blockNum <= toBlock; blockNum++ {
		storedBlock, err := ReadBlockFromClassic(ctx, rpcClient, new(big.Int).SetUint64(blockNum))
		if err != nil {
			return err
		}
		if storedBlock.Header.ParentHash != prevHash {
			return fmt.Errorf("unexpected block hash: %v", prevHash)
		}
		err = writer.Write(&storedBlock)
		if err != nil {
			return err
		}
		prevHash = storedBlock.Header.Hash()
	}
	return nil
}
