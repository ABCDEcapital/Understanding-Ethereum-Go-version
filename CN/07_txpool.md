# Transaction Pool (交易池)

## 概述 

交易池是以太坊节点用于存储和管理待处理交易的内存数据结构。交易可以分为两种类型：

1. Local Transaction (本地交易)
   - 通过节点的 RPC 接口提交的交易
   - 享有特权地位，不会被轻易驱逐
   - 会被持久化保存到本地磁盘

2. Remote Transaction (远程交易)
   - 通过 P2P 网络接收的交易
   - 在资源受限时可能被驱逐
   - 不会被持久化

注：本文主要讨论 legacypool，适用于 Legacy、AccessList 和 Dynamic 类型的交易。

## 交易池的基本结构

Transaction Pool 主要由两个核心组件构成：

1. Pending Pool
   - 存储当前可执行的交易
   - 交易的 nonce 值连续且正确
   - 账户余额足够支付交易费用
   
2. Queue Pool
   - 存储暂时无法执行的交易
   - 可能是因为 nonce 过高
   - 或账户余额不足等原因

### 核心数据结构

```go
type LegacyPool struct {
	// 基础配置
	config      Config                    // 交易池配置参数(如容量限制、价格限制等)
	chainconfig *params.ChainConfig       // 区块链配置参数(如网络ID、分叉规则等)
	chain       BlockChain               // 区块链接口,用于访问链状态
	gasTip      atomic.Pointer[uint256.Int] // 最低gas小费要求,原子操作保证线程安全
	txFeed      event.Feed               // 交易事件发送器,用于通知新交易
	signer      types.Signer             // 交易签名器,用于验证交易签名
	mu          sync.RWMutex             // 读写锁,保护并发访问

	// 当前状态
	currentHead   atomic.Pointer[types.Header] // 当前区块头,原子操作保证线程安全
	currentState  *state.StateDB              // 当前状态数据库
	pendingNonces *noncer                     // 待处理的nonce追踪器,用于nonce计数

	// 本地交易管理
	locals  *accountSet  // 本地账户集合,这些账户的交易免于驱逐规则
	journal *journal     // 本地交易日志,用于持久化存储本地交易

	// 交易存储和管理
	reserve txpool.AddressReserver        // 地址预留器,确保跨子池的地址互斥性
	pending map[common.Address]*list      // 当前可执行的交易映射(按地址索引)
	queue   map[common.Address]*list      // 未来待执行的交易映射(按地址索引)
	beats   map[common.Address]time.Time  // 每个账户的最后活动时间
	all     *lookup                       // 所有交易的查找表,支持快速查找
	priced  *pricedList                   // 按价格排序的交易列表

	// 通道和事件处理
	reqResetCh      chan *txpoolResetRequest  // 请求重置交易池的通道
	reqPromoteCh    chan *accountSet          // 请求提升交易的通道
	queueTxEventCh  chan *types.Transaction   // 交易队列事件通道
	reorgDoneCh     chan chan struct{}        // 重组完成通知通道
	reorgShutdownCh chan struct{}             // 请求关闭重组循环的通道
	wg              sync.WaitGroup            // 等待组,用于追踪goroutine
	initDoneCh      chan struct{}             // 初始化完成通道(用于测试)

	// 状态追踪
	changesSinceReorg int  // 在重组之间执行的删除操作计数器
}

```

## 交易验证与排序
验证交易:
- nonce检查
- gas价格检查 
- 余额检查
- gas限制检查

```go
func validateTx(tx *types.Transaction, local bool) error {
    // 1. 大小验证
    if tx.Size() > txMaxSize { // txMaxSize = 128KB
        return ErrOversizedData
    }
    
    // 2. 签名验证
    from, err := types.Sender(pool.signer, tx)
    if err != nil {
        return ErrInvalidSender
    }

    // 3. Nonce检查
    nonce := pool.currentState.GetNonce(from)
    if tx.Nonce() < nonce {
        // nonce太低,交易已过期
        return ErrNonceTooLow
    }

    // 4. Gas价格验证
    if !local { // 本地交易豁免
        gasTipCap, _ := tx.EffectiveGasTip(baseFee)
        if gasTipCap.Cmp(pool.gasTip.Load()) < 0 {
            return ErrUnderpriced
        }
    }

    // 5. 余额验证
    balance := pool.currentState.GetBalance(from)
    if balance.Cmp(tx.Cost()) < 0 {
        return ErrInsufficientFunds
    }

    // 6. Gas限制验证
    if tx.Gas() > pool.currentHead.Load().GasLimit {
        return ErrGasLimit
    }

    return nil
}
```

### 交易排序机制

交易池使用两级排序机制：

1. 账户内排序
   - 使用 `TransactionsByNonce` 按 nonce 值排序
   - 确保交易按序执行
   
2. 账户间排序
   - 使用 `pricedList` 按 gas 价格排序
   - 高价格交易优先处理

```go
type pricedList struct {
    all    *lookup                  // 指向所有交易的指针
    items  *prque.Prque[*types.Transaction] // 按价格排序的优先队列
    stales int64                    // 作废交易的数量
}

// Put 添加一个交易到价格排序列表
func (l *pricedList) Put(tx *types.Transaction) {
    // 计算交易的有效gas价格
    gasFeeCap := tx.GasFeeCap()
    l.items.Push(tx, -gasFeeCap.Int64()) // 负值使得高价格排在前面
}

// list 实现按nonce排序的交易列表
type list struct {
    txs    *types.TransactionsByNonce // 按nonce排序的交易
    nonces map[uint64]*types.Transaction // nonce到交易的映射
}

// Add 添加一个交易到nonce排序列表
func (l *list) Add(tx *types.Transaction) {
    nonce := tx.Nonce()
    if l.nonces[nonce] == nil {
        l.txs.Insert(tx)
        l.nonces[nonce] = tx
    }
}

```

### 交易选择
从pending池中选择交易打包时:
 1. 首先按账户分组
 2. 每个账户内部按nonce排序
 3. 不同账户间按gas价格排序

### 交易替换

```go
// 当收到相同nonce的新交易时:
if newTx.GasPrice().Cmp(oldTx.GasPrice()) > pool.config.PriceBump {
    // 如果新交易的gas价格比旧交易高出足够多(默认10%)
    // 则替换旧交易
}
```

### 交易池清理
当池满时移除交易:
 1. 优先移除gas价格低的交易
 2. 保护本地交易不被移除
 3. 确保每个账户的连续性(nonce不能断开)

## 交易池的限制

交易池设置了一些的参数来限制单个交易的 Size ，以及整个 Transaction Pool 中所保存的交易的总数量。当交易池的中维护的交易超过某个阈值的时候，交易池会丢弃/驱逐(Discard/Evict)一部分的交易。这里注意，被清除的交易通常都是 Remote Transaction，而 Local Transaction 通常都会被保留下来。

负责判断哪些交易会被丢弃的函数是 `txPricedList.Discard()`。

Transaction Pool 引入了一个名为 `txSlotSize` 的 Metrics 作为计量一个交易在交易池中所占的空间。目前，`txSlotSize` 的值是 `32 * 1024`。每个交易至少占据一个 `txSlot`，最大能占用四个 `txSlotSize`，`txMaxSize = 4 * txSlotSize = 128 KB`。换句话说，如果一个交易的物理数据大小不足 32KB，那么它会占据一个 `txSlot`。同时，一个合法的交易最大是 128KB 的大小

按照默认的设置，交易池的最多保存 `4096+1024` 个交易，其中 Pending 区保存 4096 个 `txSlot` 规模的交易，Queue 区最多保存 1024 个 `txSlot` 规模的交易。


## 交易池的更新

### 1.交易添加流程
```go
// add 验证并添加交易到池中
func (pool *LegacyPool) add(tx *types.Transaction, local bool) (replaced bool, err error) {
    // 获取交易发送者
    from, err := types.Sender(pool.signer, tx)
    if err != nil {
        return false, err
    }
    
    // 加锁保护并发访问
    pool.mu.Lock()
    defer pool.mu.Unlock()

    // 1. 基础验证
    if err := pool.validateTx(tx, local); err != nil {
        return false, err
    }

    // 2. 检查是否替换现有交易
    if old := pool.all.Get(tx.Hash()); old != nil {
        // 如果新交易gas价格不够高，拒绝替换
        if !pool.shouldReplace(old, tx) {
            return false, ErrReplaceUnderpriced
        }
        // 标记为替换操作
        replaced = true
    }

    // 3. 将交易添加到合适的队列
    if pool.pending[from] == nil {
        pool.pending[from] = newTxList(true)
    }
    if pool.queue[from] == nil {
        pool.queue[from] = newTxList(false)
    }

    // 4. 根据nonce决定放入pending还是queue
    if tx.Nonce() > pool.currentState.GetNonce(from) {
        pool.queue[from].Add(tx)
    } else {
        pool.pending[from].Add(tx)
    }

    // 5. 更新价格排序
    pool.priced.Put(tx)
    
    return replaced, nil
}
```
### 2. 状态更新流程
```go
// reset 处理链状态更新
func (pool *LegacyPool) reset(oldHead, newHead *types.Header) {
    // 1. 初始化新状态
    var reinject types.Transactions
    if oldHead != nil && oldHead.Hash() != newHead.ParentHash {
        // 发生链重组，需要重新注入交易
        oldNum := oldHead.Number.Uint64()
        newNum := newHead.Number.Uint64()
        
        // 收集需要重新注入的交易
        for i := oldNum + 1; i <= newNum; i++ {
            block := pool.chain.GetBlock(newHead.Hash(), i)
            for _, tx := range block.Transactions() {
                reinject = append(reinject, tx)
            }
        }
    }

    // 2. 更新当前状态
    pool.currentState, _ = pool.chain.StateAt(newHead.Root)
    pool.pendingNonces = newNoncer(pool.currentState)

    // 3. 重新验证所有pending交易
    pool.demoteUnexecutables()

    // 4. 重新注入交易
    if len(reinject) > 0 {
        pool.addTxsLocked(reinject, false)
    }
}
```
### 3. 交易提升流程
```go
// promoteTx 尝试将交易从queue提升到pending
func (pool *LegacyPool) promoteTx(addr common.Address, hash common.Hash, tx *types.Transaction) bool {
    // 1. 检查nonce
    if pool.currentState.GetNonce(addr) != tx.Nonce() {
        return false
    }

    // 2. 检查余额
    if pool.currentState.GetBalance(addr).Cmp(tx.Cost()) < 0 {
        return false
    }

    // 3. 从queue移除
    pool.queue[addr].Remove(hash)
    
    // 4. 添加到pending
    pool.pending[addr].Add(tx)
    
    // 5. 更新状态
    pool.beats[addr] = time.Now()
    
    return true
}
```

## 容量限制与清理机制

### 容量限制

1. 单个交易限制
   - 最小占用: 1个 txSlot (32KB)
   - 最大占用: 4个 txSlot (128KB)

2. 交易池容量
   - Pending池: 4096个 txSlot
   - Queue池: 1024个 txSlot
   
### 清理策略

当池满时，按以下策略清理交易：

1. 优先清理远程交易，保护本地交易
2. 按gas价格排序，优先清理低价交易
3. 保持每个账户的nonce连续性
4. 清理长期未被打包的过期交易

## 状态更新与维护

### 定期维护任务

交易池运行以下定期维护任务：
```go
func loop() {
    // 1. 状态报告 (每分钟)
    case <-report.C:
        logPoolStats()
    
    // 2. 交易清理 (每小时)
    case <-evict.C:
        removeExpiredTxs()
    
    // 3. 本地交易持久化 (每小时)
    case <-journal.C:
        persistLocalTxs()
}
```

### 链状态同步

当区块链状态发生变化时：

1. 检测链重组，重新注入相关交易
2. 更新状态数据库引用
3. 重新验证所有pending交易
4. 尝试将queue中的交易提升到pending

这些机制共同确保了交易池的正确性和效率。