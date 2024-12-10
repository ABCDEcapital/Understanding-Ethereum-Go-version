# 状态管理一: StateDB

## 概述

在本章中，我们来简析一下 go-ethereum 状态管理模块 StateDB。
core/state

## 理解 StateDB 的结构

我们知道以太坊是是基于以账户为核心的状态机 (State Machine)的模型。在账户的值发生变化的时候，我们说该账户从一个状态转换到了另一个状态。我们知道，在实际中，每个地址都对应了一个账户。随着以太坊用户和合约数量的增加，如何管理这些账户是客户端开发人员需要解决的首要问题。在 go-ethereum 中，StateDB 模块就是为管理账户状态设计的。它是直接提供了与 `StateObject` (账户和合约的抽象) 相关的 CURD 的接口给其他的模块，比如：

- Block 同步模块，执行新 Block 中的交易时调用 `StateDB` 来更新对应账户的值，并且形成新 world state，同时用这个计算出来的 world state与 Block Header 中提供的 state root 进行比较，来验证区块的合法性。
- 在 EVM 模块中，调用与合约存储有关的相关的两个 opcode, `sStore` 和 `sSload` 时会调用 `StateDB`中的函数来查询和更新 Contract 中的持久化存储.
- ...

在实际中，所有的账户数据(包括当前和历史的状态数据)最终还是持久化在硬盘中。目前所有的状态数据都是通过KV的形式被持久化到了基于 LSM-Tree 的存储引擎中(例如 go-leveldb)。显然，直接从这种KV存储引擎中读取和更新状态数据是不友好的。而 `StateDB` 就是为了操作这些数据而诞生的抽象层。`StateDB` 本质上是一个用于管理所有账户状态的位于内存中的抽象组件。从某种意义上说，我们可以把它理解成一个中间层的内存数据库。


`StateDB` 的定义位于 `core/state/statedb.go` 文件中，如下所示。

```go
type StateDB struct {
    db         Database
    
    // trie预取器，用于优化数据加载性能
    prefetcher *triePrefetcher
    
    // 状态树的根节点，用于存储所有账户状态
    trie       Trie
    
    // 状态读取器接口，提供对状态的只读访问
    reader     Reader

    // 原始状态根哈希，记录状态变更前的根哈希
    // 当调用Commit时会更新此值
    originalRoot common.Hash

    // 活跃对象映射，存储当前正在处理的状态对象
    // key是账户地址，value是账户状态对象
    // 这些对象在状态转换过程中会被修改
    stateObjects map[common.Address]*stateObject

    // 已删除对象映射，存储被标记为删除的状态对象
    // 同一个地址可能同时存在于stateObjects中（账户重生的情况）
    // 这里存储的是转换前的原始值
    // 此映射在交易边界被填充
    stateObjectsDestruct map[common.Address]*stateObject

    // 账户变更追踪映射，记录状态转换过程中的账户变更
    // 同一账户的未提交变更可以合并为一个等效的数据库操作
    // 此映射在交易边界被填充
    mutations map[common.Address]*mutation

    // 数据库错误
    // 状态对象被共识核心和VM使用，它们无法处理数据库级别的错误
    // 任何数据库读取错误都会被记录在这里，最终由StateDB.Commit返回
    // 这个错误也会被所有缓存的状态对象共享
    dbErr error

    // 退款计数器，用于状态转换过程中的gas退款
    refund uint64

    // 交易上下文和交易范围内产生的所有日志
    thash   common.Hash    // 交易哈希
    txIndex int           // 交易索引
    logs    map[common.Hash][]*types.Log  // 日志映射
    logSize uint          // 日志大小

    // 区块范围内VM看到的原像映射
    preimages map[common.Hash][]byte

    // 每个交易的访问列表，记录被访问的账户和存储槽
    accessList   *accessList
    accessEvents *AccessEvents

    // 临时存储，用于存储交易执行期间的临时数据
    transientStorage transientStorage

    // 状态修改日志，用于实现快照和回滚功能
    journal *journal

    // 状态见证，用于交叉验证
    witness *stateless.Witness

    // 执行过程中收集的性能指标，用于调试目的
    AccountReads    time.Duration    // 账户读取耗时
    AccountHashes   time.Duration    // 账户哈希计算耗时
    AccountUpdates  time.Duration    // 账户更新耗时
    AccountCommits  time.Duration    // 账户提交耗时
    StorageReads    time.Duration    // 存储读取耗时
    StorageUpdates  time.Duration    // 存储更新耗时
    StorageCommits  time.Duration    // 存储提交耗时
    SnapshotCommits time.Duration    // 快照提交耗时
    TrieDBCommits   time.Duration    // Trie数据库提交耗时

    // 状态转换过程中的统计数据
    AccountLoaded  int           // 从数据库加载的账户数量
    AccountUpdated int           // 更新的账户数量
    AccountDeleted int           // 删除的账户数量
    StorageLoaded  int           // 从数据库加载的存储槽数量
    StorageUpdated atomic.Int64  // 更新的存储槽数量（原子操作）
    StorageDeleted atomic.Int64  // 删除的存储槽数量（原子操作）
}
```


### db

`StateDB` 结构中的第一个变量 `db` 是一个由 `Database` 类型定义的。这里的 `Database` 是一个抽象层的接口类型，它的定义如下所示。我们可以看到在`Database`接口中定义了一些操作更细粒度的数据管理模块的函数。`TrieDB()` 函数会返回一个指向更底层的 Trie Databse 的实例。这两个模块都是非常重要的管理链上数据的模块。

```go
// Database 接口封装了对状态树和合约代码的访问
// 它是以太坊状态数据库的核心抽象接口
type Database interface {
    // Reader 返回与指定状态根关联的状态读取器
    // 参数:
    //   - root: 状态树的根哈希
    // 返回:
    //   - Reader: 状态读取器接口
    // 用途: 提供对特定状态的只读访问能力
    Reader(root common.Hash) (Reader, error)

    // OpenTrie 打开主账户树
    // 参数: root: 账户树的根哈希
    // 返回: Trie: 账户树接口
    // 用途: 访问和操作账户状态树
    OpenTrie(root common.Hash) (Trie, error)

    // OpenStorageTrie 打开账户的存储树
    // 参数:
    //   - stateRoot: 状态树根哈希
    //   - address: 账户地址
    //   - root: 存储树根哈希
    //   - trie: 父树实例
    // 返回:
    //   - Trie: 存储树接口
    //   - error: 可能的错误
    // 用途: 访问和操作智能合约的存储数据
    OpenStorageTrie(stateRoot common.Hash, address common.Address, root common.Hash, trie Trie) (Trie, error)

    // PointCache 返回用于verkle树键计算的点缓存
    // 返回: 点缓存实例
    // 用途: 优化verkle树的键计算性能
    PointCache() *utils.PointCache

    // TrieDB 返回底层的trie数据库
    // 返回: trie数据库实例
    // 用途: 管理trie节点的底层存储
    TrieDB() *triedb.Database

    // Snapshot 返回底层状态快照
    // 返回:状态快照树
    // 用途: 提供状态快照功能，用于优化状态访问
    Snapshot() *snapshot.Tree
}
```
### Trie

这里的 `trie` 变量同样的是由一个 `Trie` 类型的接口定义的。通过这个 `Trie` 类型的接口，上层其他模块就可以通过 `StateDB.tire` 来具体的对 `trie` 的数据进行操作。 

```go
// Trie 接口定义了以太坊的 Merkle Patricia Trie 的操作
type Trie interface {
    // GetKey 返回之前用于存储值的哈希键的原像
    // 注意：这个方法计划在 StateTrie 移除后废弃
    GetKey([]byte) []byte

    // GetAccount 从 trie 中读取账户信息
    // 参数：账户地址
    // 返回：
    //   - 账户状态对象（如果账户不存在则返回 nil）
    //   - 错误信息（如果 trie 损坏或节点丢失）
    GetAccount(address common.Address) (*types.StateAccount, error)

    // GetStorage 从 trie 中获取存储值
    // 参数：
    //   - addr: 合约地址
    //   - key: 存储键
    // 返回：
    //   - 存储值（调用者不得修改返回的字节数组）
    //   - 错误信息（如果节点未找到则返回 MissingNodeError）
    GetStorage(addr common.Address, key []byte) ([]byte, error)

    // UpdateAccount 将账户信息写入 trie
    // 参数：
    //   - address: 账户地址
    //   - account: 账户状态对象
    //   - codeLen: 合约代码长度
    // 返回：可能的错误信息
    UpdateAccount(address common.Address, account *types.StateAccount, codeLen int) error

    // UpdateStorage 在 trie 中更新存储键值对
    // 参数：
    //   - addr: 合约地址
    //   - key: 存储键
    //   - value: 存储值（如果长度为零则删除现有值）
    // 返回：可能的错误信息
    UpdateStorage(addr common.Address, key, value []byte) error

    // DeleteAccount 从 trie 中删除账户
    // 参数：要删除的账户地址
    // 返回：可能的错误信息
    DeleteAccount(address common.Address) error

    // DeleteStorage 从 trie 中删除存储键值对
    // 参数：
    //   - addr: 合约地址
    //   - key: 要删除的存储键
    // 返回：可能的错误信息
    DeleteStorage(addr common.Address, key []byte) error

    // UpdateContractCode 更新合约代码
    // 参数：
    //   - address: 合约地址
    //   - codeHash: 代码哈希
    //   - code: 合约代码
    // 返回：可能的错误信息
    UpdateContractCode(address common.Address, codeHash common.Hash, code []byte) error

    // Hash 返回 trie 的根哈希
    // 注意：不会写入数据库，即使 trie 没有数据库也可以使用
    Hash() common.Hash

    // Commit 提交所有脏节点并用对应的节点哈希替换它们
    // 参数：
    //   - collectLeaf: 是否收集脏叶子节点
    // 返回：
    //   - 新的根哈希
    //   - 收集到的节点集合（如果 trie 是干净的则为 nil）
    // 注意：提交后的 trie 不能再使用，需要创建新的 trie
    Commit(collectLeaf bool) (common.Hash, *trienode.NodeSet)

    // Witness 返回包含所有已访问 trie 节点的集合
    // 返回：访问过的节点集合（如果为空则返回 nil）
    Witness() map[string]struct{}

    // NodeIterator 返回遍历 trie 节点的迭代器
    // 参数：起始键（迭代从该键之后开始）
    // 返回：节点迭代器和可能的错误
    NodeIterator(startKey []byte) (trie.NodeIterator, error)

    // Prove 为指定键构造默克尔证明
    // 参数：
    //   - key: 要证明的键
    //   - proofDb: 用于写入证明的数据库
    // 返回：可能的错误信息
    // 注意：如果键不存在，返回的证明包含最长存在前缀的所有节点
    Prove(key []byte, proofDb ethdb.KeyValueWriter) error

    // IsVerkle 返回该 trie 是否基于 Verkle 树
    IsVerkle() bool
}
```

## StateDB 的持久化

```
StateDB --> CachingDB --> TrieDB --> LevelDB
```

### 1. StateDB 层面的持久化
StateDB 的持久化主要通过 Commit 操作完成，这个过程会处理所有状态对象（stateObject）的变更。主要涉及：
```go
// stateObject 表示一个正在被修改的以太坊账户
type stateObject struct {
    db       *StateDB
    address  common.Address
    data     types.StateAccount  // 当前区块范围内的所有变更
    
    // 存储相关的缓存
    originStorage      Storage   // 在当前区块内被访问的存储条目
    dirtyStorage      Storage   // 在当前交易中被修改的存储条目
    pendingStorage    Storage   // 在当前区块中被修改的存储条目
    uncommittedStorage Storage  // 尚未提交的存储变更
}
```
### 2. 存储变更的处理流程
存储变更经过多个阶段的处理：
```go
// 1. 交易执行时的存储更新
func (s *stateObject) SetState(key, value common.Hash) common.Hash {
    prev, origin := s.getState(key)
    if prev == value {
        return prev
    }
    s.db.journal.storageChange(s.address, key, prev, origin)
    s.setState(key, value, origin)
    return prev
}

// 2. 交易结束时的存储整理
func (s *stateObject) finalise() {
    for key, value := range s.dirtyStorage {
        s.pendingStorage[key] = value
    }
    if len(s.dirtyStorage) > 0 {
        s.dirtyStorage = make(Storage)
    }
}

// 3. 区块提交时的存储更新
func (s *stateObject) updateTrie() (Trie, error) {
    for key, value := range s.uncommittedStorage {
        if err := tr.UpdateStorage(s.address, key[:], value[:]); err != nil {
            return nil, err
        }
    }
}
```
### 4. 数据库层面的持久化
最终的持久化由 CachingDB 完成：
```go
   func (s *stateObject) commit() (*accountUpdate, *trienode.NodeSet, error) {
       // 1. 提交账户元数据变更
       op := &accountUpdate{
           address: s.address,
           data:    types.SlimAccountRLP(s.data),
       }
       
       // 2. 提交合约代码（如果有修改）
       if s.dirtyCode {
           op.code = &contractCode{...}
       }
       
       // 3. 提交存储变更
       s.commitStorage(op)
       
       // 4. 提交 trie 变更
       root, nodes := s.trie.Commit(false)
       return op, nodes, nil
   }
```

## 持久化流程
1. 交易执行过程
- 状态变更记录在 stateObject 的 dirtyStorage
- 通过 journal 记录回滚信息
2. 交易结束时：
- 调用 ```finalise() ```将 ```dirtyStorage ```中的变更移至 ```pendingStorage```
- 清空 ```dirtyStorage``` 为下一个交易做准备
3. 区块提交时：
```go
func (s *stateObject) commit() (*accountUpdate, *trienode.NodeSet, error) {
		// 1. 提交账户元数据变更
		op := &accountUpdate{
				address: s.address,
				data:    types.SlimAccountRLP(s.data),
		}
		
		// 2. 提交合约代码（如果有修改）
		if s.dirtyCode {
				op.code = &contractCode{...}
		}
		
		// 3. 提交存储变更
		s.commitStorage(op)
		
		// 4. 提交 trie 变更
		root, nodes := s.trie.Commit(false)
		return op, nodes, nil
}
```
4. 最终持久化：
- Trie 节点被序列化并存储到底层数据库
- 合约代码被存储到专门的代码存储区
- 新的状态根被计算并返回

## 关键机制
1. 多级缓存：
- originStorage: 原始状态缓存
- dirtyStorage: 交易内变更缓存
- pendingStorage: 区块内变更缓存
2. 预取机制：
```go
func (s *stateObject) getPrefetchedTrie() Trie {
	if s.db.prefetcher != nil {
		return s.db.prefetcher.trie(s.addrHash, s.data.Root)
	}
	return nil
}
```
3. 原子性保证
- 使用 journal 记录所有变更
- 支持回滚到任意快照点

4. 数据一致性：
- 通过多级存储确保状态一致性
- 使用 Merkle Patricia Trie 验证数据完整性