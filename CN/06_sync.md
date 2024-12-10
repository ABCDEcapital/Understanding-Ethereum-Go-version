# 交易和区块的同步

在本章中，我们会像哲学家一样思考：数据结构/实例/对象/变量是从哪里来，又要到哪里去呢？

## 概述

 在前面的章节中，我们已经讨论了在以太坊中 Transactions 是从 Transaction Pool 中，被 Validator/Miner 们验证打包，最终被保存在区块链中。那么，接下来的问题是，Transaction 是怎么被进入到 Transaction Pool 中的呢？基于同样的思考方式，那么一个刚刚在某个节点被打包好的 Block，它又将怎么传输到区块链网络中的其他节点那里，并最终实现 Blockchain 长度是一致的呢？在本章中，我们就来探索一下，节点是如何发送和接收 Transaction 和 Block 的。

## How Geth syncs Transactions：同步交易状态

在前面的章节中，我们曾经提到，Geth 节点中最顶级的对象是 Node 类型，负责节点最高级别生命周期相关的操作，例如节点的启动以及关闭，节点数据库的打开和关闭，启动RPC监听。而更具体的管理业务生命周期(lifecycle)的函数，都是由后端 Service 实例 `Ethereum` 和 `LesEthereum` 来实现的。

定义在`eth/backend.go` 中的 `Ethereum` 提供了一个全节点的所有的服务包括：TxPool 交易池， Miner 模块，共识模块，API 服务，以及解析从 P2P 网络中获取的数据。`LesEthereum` 提供了轻节点对应的服务。由于轻节点所支持的功能相对较少，在这里我们不过多描述。`Ethereum` 结构体的定义如下所示。

```go
type Ethereum struct {
    // 1. 核心协议对象
    config     *ethconfig.Config     // 以太坊配置参数
    txPool     *txpool.TxPool        // 交易池，存储待处理的交易
    blockchain *core.BlockChain      // 区块链核心组件，管理区块和状态

    // 2. 网络处理
    handler *handler                 // 处理以太坊协议的网络消息
    discmix *enode.FairMix          // 节点发现混合器，用于P2P网络

    // 3. 数据库接口
    chainDb ethdb.Database          // 区块链数据库，存储所有区块数据

    // 4. 事件和共识
    eventMux       *event.TypeMux    // 事件多路复用器，处理各种事件
    engine         consensus.Engine   // 共识引擎（PoW/PoS）
    accountManager *accounts.Manager  // 账户管理器

    // 5. Bloom过滤器相关
    bloomRequests     chan chan *bloombits.Retrieval // 布隆过滤器数据检索请求通道
    bloomIndexer      *core.ChainIndexer             // 区块导入时的布隆索引器
    closeBloomHandler chan struct{}                  // 关闭布隆处理器的信号通道

    // 6. API和挖矿
    APIBackend *EthAPIBackend        // 后端API接口
    miner      *miner.Miner          // 挖矿器
    gasPrice   *big.Int              // 燃料价格

    // 7. 网络相关
    networkID     uint64             // 网络ID
    netRPCService *ethapi.NetAPI     // RPC服务
    p2pServer     *p2p.Server        // P2P服务器

    // 8. 同步和保护
    lock sync.RWMutex                // 读写锁，保护可变字段
    shutdownTracker *shutdowncheck.ShutdownTracker // 跟踪节点是否非正常关闭
}
```

这里值得提醒一下，在 Geth 代码中，不少地方都使用 `backend` 这个变量名，来指代 `Ethereum`。但是，也存在一些代码中使用 `backend` 来指代 `ethapi.Backend` 接口。在这里，我们可以做一下区分，`Ethereum` 负责维护节点后端的生命周期的函数，例如 Miner 的开启与关闭。而`ethapi.Backend` 接口主要是提供对外的业务接口，例如查询区块和交易的状态。读者可以根据上下文来判断 `backend` 具体指代的对象。我们在 geth 是如何启动的一章中提到，`Ethereum` 是在 Geth 启动的实例化的。在实例化 `Ethereum` 的过程中，就会创建一个 `APIBackend *EthAPIBackend` 的成员变量，它就是`ethapi.Backend` 接口类型的。


1. 交易广播机制 (handler.go)
```go
func (h *handler) BroadcastTransactions(txs types.Transactions) {
    var (
        txset = make(map[*ethPeer][]common.Hash)  // 直接传输的交易
        annos = make(map[*ethPeer][]common.Hash)  // 仅通知的交易
    )
    
    // 计算广播目标节点数量(使用平方根来优化网络负载)
    direct := big.NewInt(int64(math.Sqrt(float64(h.peers.len()))))
    
    for _, tx := range txs {
        // 根据交易类型决定传播策略
        switch {
        case tx.Type() == types.BlobTxType:
            // Blob交易只发送通知
            blobTxs++
        case tx.Size() > txMaxBroadcastSize:
            // 大交易只发送通知
            largeTxs++
        default:
            // 普通交易可能直接广播
            maybeDirect = true
        }
        
        // 选择未收到该交易的节点进行传播
        for _, peer := range h.peers.peersWithoutTransaction(tx.Hash()) {
            if broadcast {
                txset[peer] = append(txset[peer], tx.Hash())
            } else {
                annos[peer] = append(annos[peer], tx.Hash())
            }
        }
    }
}
```
2. 交易同步机制 (sync.go)
```go
func (h *handler) syncTransactions(p *eth.Peer) {
    // 获取所有待处理的交易
    var hashes []common.Hash
    for _, batch := range h.txpool.Pending(txpool.PendingFilter{OnlyPlainTxs: true}) {
        for _, tx := range batch {
            hashes = append(hashes, tx.Hash)
        }
    }
    
    // 发送交易哈希给新peer
    if len(hashes) > 0 {
        p.AsyncSendPooledTransactionHashes(hashes)
    }
}
```
3. 交易处理流程 (handler_eth.go)
```go
func (h *ethHandler) Handle(peer *eth.Peer, packet eth.Packet) error {
    switch packet := packet.(type) {
    case *eth.NewPooledTransactionHashesPacket:
        // 处理新交易哈希通知
        return h.txFetcher.Notify(peer.ID(), packet.Types, packet.Sizes, packet.Hashes)
        
    case *eth.TransactionsPacket:
        // 处理接收到的交易
        return h.txFetcher.Enqueue(peer.ID(), *packet, false)
        
    case *eth.PooledTransactionsResponse:
        // 处理交易池响应
        return h.txFetcher.Enqueue(peer.ID(), *packet, true)
    }
}
```
1. 交易广播机制 (handler.go)
```go
```

## How Geth syncs Blocks：同步区块状态
### 1. 同步模式选择 (backend.go)
```go
func (s *Ethereum) SyncMode() ethconfig.SyncMode {
    // 1. 检查是否在快照同步模式
    if s.handler.snapSync.Load() {
        return ethconfig.SnapSync
    }
    
    // 2. 检查是否需要重新启用快照同步
    head := s.blockchain.CurrentBlock()
    if pivot := rawdb.ReadLastPivotNumber(s.chainDb); pivot != nil {
        if head.Number.Uint64() < *pivot {
            return ethconfig.SnapSync
        }
    }
    
    // 3. 检查状态完整性
    if !s.blockchain.HasState(head.Root) {
        log.Info("Reenabled snap sync as chain is stateless")
        return ethconfig.SnapSync
    }
    
    return ethconfig.FullSync
}
```
### 2. 节点同步处理 (handler.go)
```go
func (h *handler) runEthPeer(peer *eth.Peer, handler eth.Handler) error {
    // 1. 执行以太坊握手
    var (
        genesis = h.chain.Genesis()
        head    = h.chain.CurrentHeader()
        hash    = head.Hash()
        number  = head.Number.Uint64()
        td      = h.chain.GetTd(hash, number)
    )
    
    // 2. 验证分叉ID
    forkID := forkid.NewID(h.chain.Config(), genesis, number, head.Time)
    if err := peer.Handshake(h.networkID, td, hash, genesis.Hash(), forkID, h.forkFilter); err != nil {
        return err
    }
    
    // 3. 注册到下载器
    if err := h.downloader.RegisterPeer(peer.ID(), peer.Version(), peer); err != nil {
        return err
    }
    
    // 4. 如果支持快照同步，注册快照同步
    if snap != nil {
        if err := h.downloader.SnapSyncer.Register(snap); err != nil {
            return err
        }
    }
}
```
### 3. 区块同步流程
3.1. 初始化同步:
```go
func newHandler(config *handlerConfig) (*handler, error) {
    h := &handler{
        // ...
        downloader: downloader.New(config.Database, h.eventMux, h.chain, h.removePeer)
    }
    
    // 根据配置决定同步模式
    if config.Sync == ethconfig.FullSync {
        // 检查是否需要切换到快照同步
        if fullBlock.Number.Uint64() == 0 && snapBlock.Number.Uint64() > 0 {
            h.snapSync.Store(true)
        }
    }
}
```
3.2. 同步过程管理:
```go
func (h *handler) Start(maxPeers int) {
    // 启动同步处理器
    h.txFetcher.Start()
    
    // 启动协议处理器追踪
    go h.protoTracker()
}
```
3.3. 状态验证:
```go
func (h *handler) runEthPeer(peer *eth.Peer, handler eth.Handler) error {
    // 验证必需区块
    for number, hash := range h.requiredBlocks {
        // 请求区块头
        req, err := peer.RequestHeadersByNumber(number, 1, 0, false, resCh)
        
        // 验证响应
        if headers[0].Number.Uint64() != number || headers[0].Hash() != hash {
            return errors.New("required block mismatch")
        }
    }
}
```
### 4. 同步完成处理
```go
func (h *handler) enableSyncedFeatures() {
    // 标记节点已同步
    h.synced.Store(true)

    // 如果是快照同步完成，禁用后续快照同步
    if h.snapSync.Load() {
        log.Info("Snap sync complete, auto disabling")
        h.snapSync.Store(false)
    }
}
```
主要同步流程：
1. 同步模式确定
- 根据节点状态选择同步模式
- 支持快照同步和全同步
- 可动态切换同步模式
2. 节点连接处理
- 执行以太坊握手
- 验证分叉ID
- 注册peer到下载器
3. 区块下载和验证
- 请求区块头和区块体
- 验证区块有效性
- 处理分叉情况
4. 状态同步
- 同步状态树
- 处理快照数据
- 验证状态完整性
5. 同步完成处理
- 更新节点状态
- 启用同步后功能
- 处理模式切换