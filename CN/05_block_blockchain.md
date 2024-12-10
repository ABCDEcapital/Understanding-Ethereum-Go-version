# 区块和区块链 (Block & Blockchain)

## Block 

### 基础数据结构

```go
type Block struct {
    // 核心数据
    header       *Header          // 区块头
    uncles       []*Header        // 叔块列表
    transactions Transactions     // 交易列表
    withdrawals  Withdrawals      // 提款操作列表

    // 状态证明
    witness *ExecutionWitness     // 执行见证数据（非区块体的一部分）

    // 缓存字段
    hash atomic.Pointer[common.Hash]  // 区块哈希缓存
    size atomic.Uint64               // 区块大小缓存

    // 网络相关字段
    ReceivedAt   time.Time           // 接收时间
    ReceivedFrom interface{}         // 接收来源
}
```

```go
// Header 表示以太坊区块链中的区块头
type Header struct {
    // ParentHash 是父区块的哈希值
    // 用于维护区块链的链式结构
    ParentHash  common.Hash    `json:"parentHash"       gencodec:"required"`

    // UncleHash 是叔块列表的哈希值
    // 在 PoW 中用于奖励接近但未被选中的区块
    UncleHash   common.Hash    `json:"sha3Uncles"       gencodec:"required"`

    // Coinbase 是接收挖矿奖励的地址
    // 在 PoS 中是接收交易费用的地址
    Coinbase    common.Address `json:"miner"`

    // Root 是状态树的根哈希
    // 代表该区块后整个以太坊状态的快照
    Root        common.Hash    `json:"stateRoot"        gencodec:"required"`

    // TxHash 是交易树的根哈希
    // 包含该区块中所有交易的哈希
    TxHash      common.Hash    `json:"transactionsRoot" gencodec:"required"`

    // ReceiptHash 是收据树的根哈希
    // 包含该区块中所有交易收据的哈希
    ReceiptHash common.Hash    `json:"receiptsRoot"     gencodec:"required"`

    // Bloom 是布隆过滤器
    // 用于快速查询日志事件
    Bloom       Bloom          `json:"logsBloom"        gencodec:"required"`

    // Difficulty 是区块的难度值
    // 在 PoW 中用于调整挖矿难度
    Difficulty  *big.Int       `json:"difficulty"       gencodec:"required"`

    // Number 是区块号
    // 表示该区块在区块链中的高度
    Number      *big.Int       `json:"number"           gencodec:"required"`

    // GasLimit 是区块的燃料上限
    // 限制区块中所有交易可以使用的最大燃料量
    GasLimit    uint64         `json:"gasLimit"         gencodec:"required"`

    // GasUsed 是区块中实际使用的燃料总量
    GasUsed     uint64         `json:"gasUsed"          gencodec:"required"`

    // Time 是区块的时间戳
    // 表示区块创建的近似时间
    Time        uint64         `json:"timestamp"        gencodec:"required"`

    // Extra 是额外数据字段
    // 可以包含任意数据，通常用于特殊用途
    Extra       []byte         `json:"extraData"        gencodec:"required"`

    // MixDigest 是混合哈希
    // 在 PoW 中用于验证工作量证明
    MixDigest   common.Hash    `json:"mixHash"`

    // Nonce 是用于 PoW 的随机数
    // 矿工通过改变这个值来寻找有效的区块哈希
    Nonce       BlockNonce     `json:"nonce"`

    // EIP-1559 引入的字段
    // BaseFee 是基础费用，用于动态调整交易费用
    BaseFee *big.Int `json:"baseFeePerGas" rlp:"optional"`

    // EIP-4895 引入的字段
    // WithdrawalsHash 是提款操作列表的哈希
    WithdrawalsHash *common.Hash `json:"withdrawalsRoot" rlp:"optional"`

    // EIP-4844 引入的字段
    // BlobGasUsed 是 blob 交易使用的燃料量
    BlobGasUsed *uint64 `json:"blobGasUsed" rlp:"optional"`

    // EIP-4844 引入的字段
    // ExcessBlobGas 是超出目标的 blob 燃料量
    ExcessBlobGas *uint64 `json:"excessBlobGas" rlp:"optional"`

    // EIP-4788 引入的字段
    // ParentBeaconRoot 是父区块对应的信标链区块根
    ParentBeaconRoot *common.Hash `json:"parentBeaconBlockRoot" rlp:"optional"`

    // EIP-7685 引入的字段
    // RequestsHash 是请求列表的哈希
    RequestsHash *common.Hash `json:"requestsRoot" rlp:"optional"`
}
```

## Blockchain：区块链

### 基础数据结构

```golang
// BlockChain 代表基于创世区块数据库的规范链。
// 它管理链导入、回滚和链重组等操作。
type BlockChain struct {
    // 配置相关
    chainConfig *params.ChainConfig // 链和网络配置参数
    cacheConfig *CacheConfig        // 缓存修剪配置

    // 数据库和状态存储
    db        ethdb.Database        // 用于存储最终内容的底层持久化数据库
    snaps     *snapshot.Tree        // 用于快速访问 trie 叶子节点的快照树
    triegc    *prque.Prque[int64, common.Hash] // 用于垃圾回收的区块号到 trie 的优先队列映射
    gcproc    time.Duration         // trie 转储的规范区块处理累计时间
    lastWrite uint64                // 最后一次状态刷新时的区块号
    flushInterval atomic.Int64      // 状态刷新的时间间隔（处理时间）
    
    // 数据库处理器
    triedb    *triedb.Database     // 用于维护 trie 节点的数据库处理器
    statedb   *state.CachingDB     // 在导入之间重用的状态数据库（包含状态缓存）
    txIndexer *txIndexer           // 交易索引器，如果未启用则为 nil

    // 链管理
    hc        *HeaderChain         // 区块头链管理器

    // 事件系统
    rmLogsFeed    event.Feed       // 移除日志的事件通道
    chainFeed     event.Feed       // 链更新事件通道
    chainHeadFeed event.Feed       // 链头更新事件通道
    logsFeed      event.Feed       // 日志事件通道
    blockProcFeed event.Feed       // 区块处理事件通道
    scope         event.SubscriptionScope // 事件订阅范围
    
    // 创世区块
    genesisBlock  *types.Block     // 创世区块

    // 同步锁
    // 此互斥锁同步链写入操作
    // 读取操作不需要获取锁，可以直接读取数据库
    chainmu *syncx.ClosableMutex

    // 当前状态指针
    currentBlock      atomic.Pointer[types.Header] // 当前链头
    currentSnapBlock  atomic.Pointer[types.Header] // 当前快照同步的链头
    currentFinalBlock atomic.Pointer[types.Header] // 最新的（共识）最终确定区块
    currentSafeBlock  atomic.Pointer[types.Header] // 最新的（共识）安全区块

    // 缓存系统
    bodyCache     *lru.Cache[common.Hash, *types.Body]      // 区块体缓存
    bodyRLPCache  *lru.Cache[common.Hash, rlp.RawValue]     // RLP 编码的区块体缓存
    receiptsCache *lru.Cache[common.Hash, []*types.Receipt] // 收据缓存
    blockCache    *lru.Cache[common.Hash, *types.Block]     // 区块缓存

    // 交易查找缓存
    txLookupLock  sync.RWMutex                             // 交易查找锁
    txLookupCache *lru.Cache[common.Hash, txLookup]        // 交易查找缓存

    // 同步和控制
    wg            sync.WaitGroup   // 等待组，用于优雅关闭
    quit          chan struct{}    // 关闭信号，在 Stop 时关闭
    stopping      atomic.Bool      // 链运行状态标志：false 表示运行中，true 表示已停止
    procInterrupt atomic.Bool      // 区块处理中断信号器

    // 处理组件
    engine     consensus.Engine    // 共识引擎
    validator  Validator          // 区块和状态验证器接口
    prefetcher Prefetcher        // 预取器
    processor  Processor         // 区块交易处理器接口
    vmConfig   vm.Config        // 虚拟机配置
    logger     *tracing.Hooks   // 跟踪钩子
}
```
### 主要功能
```golang
// 1. 区块插入
func (bc *BlockChain) InsertChain(chain types.Blocks) (int, error) {
    // 验证区块
    // 执行状态转换
    // 更新数据库
}

// 2. 状态管理
func (bc *BlockChain) State() (*state.StateDB, error) {
    // 返回当前状态
}

// 3. 区块检索
func (bc *BlockChain) GetBlockByNumber(number uint64) *types.Block
func (bc *BlockChain) GetBlockByHash(hash common.Hash) *types.Block

// 4. 链重组
func (bc *BlockChain) reorg(oldBlock, newBlock *types.Header) error {
    // 处理分叉
    // 更新规范链
}
```
### 3. 关键流程
1. 区块处理流程:

```golang
func (bc *BlockChain) insertChain(chain types.Blocks, verifySeals bool) (int, error) {
    // 1. 基本验证
    for i := 1; i < len(chain); i++ {
        if chain[i].NumberU64() != chain[i-1].NumberU64()+1 ||
           chain[i].ParentHash() != chain[i-1].Hash() {
            return i, fmt.Errorf("非连续区块")
        }
    }

    // 2. 状态处理
    for i, block := range chain {
        // 执行交易
        // 更新状态
        // 写入数据库
    }

    // 3. 更新链头
    bc.writeHeadBlock(chain[len(chain)-1])
}
```
2. 状态管理:
```golang
func (bc *BlockChain) writeBlockWithState(block *types.Block, receipts []*types.Receipt, state *state.StateDB) error {
    // 1. 写入区块数据
    batch := bc.db.NewBatch()
    rawdb.WriteBlock(batch, block)
    rawdb.WriteReceipts(batch, block.Hash(), receipts)

    // 2. 提交状态
    root, err := state.Commit()
    if err != nil {
        return err
    }

    // 3. 垃圾回收
    bc.triegc.Push(root, -int64(block.NumberU64()))
}
```
### 事件系统
```golang
// 事件通知
type ChainEvent struct {
    Block *types.Block
    Hash  common.Hash
    Logs  []*types.Log
}

// 订阅机制
func (bc *BlockChain) SubscribeChainEvent(ch chan<- ChainEvent) event.Subscription
```
### blockchain 是如何将 block 串联起来的
1. 区块间的链接机制
```golang
// Header 结构中的关键链接字段
type Header struct {
    ParentHash common.Hash    // 指向父区块的哈希
    Number     *big.Int       // 区块高度
    // ... 其他字段
}
```
2. BlockChain 结构的管理
```golang
type BlockChain struct {
    // 当前链状态
    currentBlock      atomic.Pointer[types.Header] // 当前链头
    currentSnapBlock  atomic.Pointer[types.Header] // 快照同步的链头
    currentFinalBlock atomic.Pointer[types.Header] // 最终确认的区块
    currentSafeBlock  atomic.Pointer[types.Header] // 安全区块

    // 数据存储
    db     ethdb.Database    // 底层数据库
    triedb *triedb.Database // trie 数据库
    
    // 缓存系统
    bodyCache    *lru.Cache[common.Hash, *types.Body]
    bodyRLPCache *lru.Cache[common.Hash, rlp.RawValue]
    blockCache   *lru.Cache[common.Hash, *types.Block]
}
```

3. 区块插入流程
```golang
func (bc *BlockChain) insertChain(chain types.Blocks) (int, error) {
    // 1. 验证区块连续性
    for i := 1; i < len(chain); i++ {
        if chain[i].NumberU64() != chain[i-1].NumberU64()+1 ||
           chain[i].ParentHash() != chain[i-1].Hash() {
            return 0, fmt.Errorf("非连续区块")
        }
    }

    // 2. 处理每个区块
    for i, block := range chain {
        // 验证区块
        if err := bc.engine.VerifyHeader(bc, block.Header()); err != nil {
            return i, err
        }

        // 执行状态转换
        state, err := bc.StateAt(block.ParentHash())
        if err != nil {
            return i, err
        }
        
        // 更新状态
        receipts, err := bc.processor.Process(block, state)
        if err != nil {
            return i, err
        }

        // 写入数据库
        bc.writeBlockWithState(block, receipts, state)
    }
}
```
4. 分叉处理
```golang
func (bc *BlockChain) reorg(oldBlock, newBlock *types.Block) error {
    // 1. 找到共同祖先
    var (
        newChain    types.Blocks
        oldChain    types.Blocks
        commonBlock *types.Block
    )

    // 2. 回滚旧链
    for oldBlock.NumberU64() > commonBlock.NumberU64() {
        oldChain = append(oldChain, oldBlock)
        oldBlock = bc.GetBlock(oldBlock.ParentHash(), oldBlock.NumberU64()-1)
    }

    // 3. 应用新链
    for newBlock.NumberU64() > commonBlock.NumberU64() {
        newChain = append(newChain, newBlock)
        newBlock = bc.GetBlock(newBlock.ParentHash(), newBlock.NumberU64()-1)
    }

    // 4. 写入新链
    for _, block := range newChain {
        // 应用区块
    }
}
```
5. 区块检索机制
```golang
func (bc *BlockChain) GetBlockByHash(hash common.Hash) *types.Block {
    // 1. 检查缓存
    if block := bc.blockCache.Get(hash); block != nil {
        return block
    }

    // 2. 从数据库读取
    block := rawdb.ReadBlock(bc.db, hash)
    if block != nil {
        bc.blockCache.Add(hash, block)
    }
    return block
}

func (bc *BlockChain) GetBlockByNumber(number uint64) *types.Block {
    hash := rawdb.ReadCanonicalHash(bc.db, number)
    if hash == (common.Hash{}) {
        return nil
    }
    return bc.GetBlockByHash(hash)
}
```
6. 状态维护
```golang
// 更新链头
func (bc *BlockChain) writeHeadBlock(block *types.Block) {
    // 1. 更新当前区块
    bc.currentBlock.Store(block.Header())

    // 2. 写入规范链哈希
    rawdb.WriteCanonicalHash(bc.db, block.Hash(), block.NumberU64())

    // 3. 更新链头
    rawdb.WriteHeadBlockHash(bc.db, block.Hash())
}
```
- 区块间的 ParentHash 链接
- 规范链的维护
- 分叉处理机制
- 高效的缓存系统
- 可靠的数据存储