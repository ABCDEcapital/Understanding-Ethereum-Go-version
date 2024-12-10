# miner/worker.go
### 1. 关键结构
```go
// environment 是当前区块生成环境的状态
type environment struct {
    signer   types.Signer          // 交易签名器
    state    *state.StateDB        // 状态数据库
    tcount   int                   // 当前周期的交易计数
    gasPool  *core.GasPool         // 可用 gas 池
    coinbase common.Address        // 矿工地址
    header   *types.Header         // 区块头
    txs      []*types.Transaction  // 交易列表
    receipts []*types.Receipt      // 收据列表
    sidecars []*types.BlobTxSidecar // blob 交易的额外数据
}

// generateParams 封装了区块生成的参数
type generateParams struct {
    timestamp   uint64            // 时间戳
    forceTime   bool             // 是否强制使用给定时间戳
    parentHash  common.Hash       // 父区块哈希
    coinbase    common.Address    // 矿工地址
    random      common.Hash       // 随机数（来自信标链）
    withdrawals types.Withdrawals // 提款列表
    beaconRoot  *common.Hash      // 信标根
}
```
### 2. 主要流程
#### 1.区块生成:
```go
func (miner *Miner) generateWork(params *generateParams, witness bool) *newPayloadResult {
    // 1. 准备区块环境
    work, err := miner.prepareWork(params, witness)
    if err != nil {
        return &newPayloadResult{err: err}
    }

    // 2. 填充交易（如果不是空区块）
    if !params.noTxs {
        interrupt := new(atomic.Int32)
        timer := time.AfterFunc(miner.config.Recommit, func() {
            interrupt.Store(commitInterruptTimeout)
        })
        defer timer.Stop()

        err := miner.fillTransactions(interrupt, work)
        if errors.Is(err, errBlockInterruptedByTimeout) {
            log.Warn("Block building is interrupted", "allowance", common.PrettyDuration(miner.config.Recommit))
        }
    }

    // 3. 完成区块构建
    block, err := miner.engine.FinalizeAndAssemble(...)
    return &newPayloadResult{...}
}
```
#### 2.准备工作环境:
```go
func (miner *Miner) prepareWork(genParams *generateParams, witness bool) (*environment, error) {
    // 1. 获取父区块
    parent := miner.chain.CurrentBlock()
    
    // 2. 构建区块头
    header := &types.Header{
        ParentHash: parent.Hash(),
        Number:     new(big.Int).Add(parent.Number, common.Big1),
        GasLimit:   core.CalcGasLimit(parent.GasLimit, miner.config.GasCeil),
        Time:       timestamp,
        Coinbase:   genParams.coinbase,
    }

    // 3. 设置共识相关字段
    if err := miner.engine.Prepare(miner.chain, header); err != nil {
        return nil, err
    }

    // 4. 创建执行环境
    env, err := miner.makeEnv(parent, header, genParams.coinbase, witness)
    return env, nil
}
```
#### 4.交易处理:
```go
func (miner *Miner) fillTransactions(interrupt *atomic.Int32, env *environment) error {
    // 1. 获取最低 gas 价格配置
    miner.confMu.RLock()
    tip := miner.config.GasPrice
    miner.confMu.RUnlock()

    // 2. 设置交易过滤条件
    filter := txpool.PendingFilter{
        MinTip: uint256.MustFromBig(tip),  // 设置最低小费要求
    }
    // 如果是 EIP-1559 交易，设置基础费用
    if env.header.BaseFee != nil {
        filter.BaseFee = uint256.MustFromBig(env.header.BaseFee)
    }
    // 如果是 EIP-4844 blob 交易，设置 blob 费用
    if env.header.ExcessBlobGas != nil {
        filter.BlobFee = uint256.MustFromBig(eip4844.CalcBlobFee(*env.header.ExcessBlobGas))
    }

    // 3. 分别获取普通交易和 blob 交易
    // 首先获取普通交易
    filter.OnlyPlainTxs, filter.OnlyBlobTxs = true, false
    pendingPlainTxs := miner.txpool.Pending(filter)

    // 然后获取 blob 交易
    filter.OnlyPlainTxs, filter.OnlyBlobTxs = false, true
    pendingBlobTxs := miner.txpool.Pending(filter)

    // 4. 将交易分为本地交易和远程交易
    localPlainTxs, remotePlainTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingPlainTxs
    localBlobTxs, remoteBlobTxs := make(map[common.Address][]*txpool.LazyTransaction), pendingBlobTxs

    // 遍历本地账户，将其交易从远程列表移到本地列表
    for _, account := range miner.txpool.Locals() {
        // 处理普通交易
        if txs := remotePlainTxs[account]; len(txs) > 0 {
            delete(remotePlainTxs, account)
            localPlainTxs[account] = txs
        }
        // 处理 blob 交易
        if txs := remoteBlobTxs[account]; len(txs) > 0 {
            delete(remoteBlobTxs, account)
            localBlobTxs[account] = txs
        }
    }

    // 5. 优先处理本地交易
    if len(localPlainTxs) > 0 || len(localBlobTxs) > 0 {
        // 按价格和 nonce 对交易排序
        plainTxs := newTransactionsByPriceAndNonce(env.signer, localPlainTxs, env.header.BaseFee)
        blobTxs := newTransactionsByPriceAndNonce(env.signer, localBlobTxs, env.header.BaseFee)

        // 提交本地交易
        if err := miner.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
            return err
        }
    }

    // 6. 处理远程交易
    if len(remotePlainTxs) > 0 || len(remoteBlobTxs) > 0 {
        // 按价格和 nonce 对交易排序
        plainTxs := newTransactionsByPriceAndNonce(env.signer, remotePlainTxs, env.header.BaseFee)
        blobTxs := newTransactionsByPriceAndNonce(env.signer, remoteBlobTxs, env.header.BaseFee)

        // 提交远程交易
        if err := miner.commitTransactions(env, plainTxs, blobTxs, interrupt); err != nil {
            return err
        }
    }
    return nil
}
```
- 交易分类：
  - 区分普通交易和 blob 交易
  - 区分本地交易和远程交易
  - 本地交易优先处理
- 排序机制：
  - 按照价格和 nonce 排序
  - 确保交易按正确顺序执行
#### 4.交易处理:
```go
func (miner *Miner) commitTransaction(env *environment, tx *types.Transaction) error {
    // 1. 处理 blob 交易
    if tx.Type() == types.BlobTxType {
        return miner.commitBlobTransaction(env, tx)
    }

    // 2. 应用交易
    receipt, err := miner.applyTransaction(env, tx)
    if err != nil {
        return err
    }

    // 3. 更新环境
    env.txs = append(env.txs, tx)
    env.receipts = append(env.receipts, receipt)
    env.tcount++
    return nil
}
```