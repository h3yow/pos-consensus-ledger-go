# pos-consensus-ledger-go

一套可在**本地单机、多节点**运行的权益证明（PoS）链核心。网络与存储均为
**进程内模拟**：不绑定端口、不睡眠、不依赖真实时序，所有消息都通过显式
"投递"（`DeliverOne / DeliverRound / DeliverAll`）驱动，因此结果完全确定、
可重复，适合验证共识规则本身。

## 能力一览

- 验证者集合 + 按权益权重的**轮转出块**调度。
- 确定性**规范链选择（fork choice）**：区块延迟、短暂分叉、重复广播都不会
  让不同节点得出不同结论。
- 权益加权**投票与终局性**：超过阈值的区块终局后不可回滚。
- **双签/双提案罚没**：同一验证者在同一 `(height, round)` 槽位签两个不同
  对象即被举证、罚没；同一证据全局只生效一次；余额总量守恒。
- **交易池与状态承诺**：按费用/地址/序号确定性打包；无效、冲突、乱序交易
  给出可区分的拒绝原因；每个区块产出跨节点一致的 `stateRoot`。

## 目录结构

| 包 | 职责 |
| --- | --- |
| `internal/types` | 交易、区块、投票、证据的确定性编码、哈希、ed25519 签名 |
| `internal/state` | 账户/余额/序号状态机、转账、罚没、Merkle 状态根 |
| `internal/mempool` | 交易池准入与确定性打包排序 |
| `internal/store` | 区块 DAG（分叉共存）、区块结果状态、投票索引 |
| `internal/network` | 进程内消息交换机：FIFO 队列、分区/恢复、重复投递 |
| `internal/consensus` | 纯规则引擎：校验、调度、分叉选择、计票、终局判定 |
| `internal/node` | 把上述组件串成一个验证者节点（出块/投票/ gossip） |
| `internal/testkit` | 测试/演示用的确定性集群搭建工具 |
| `cmd/simnode` | 本地多节点演示：终局收敛 + 双签罚没 |
| `test` | 覆盖分叉、双签、终局、并发、状态根的测试 |

## 共识规则

### 1. 验证者与出块调度

- 创世在 `Genesis.Validators` 中固定验证者公钥与**权重（stake weight）**，
  验证者按地址升序排列，权重在运行期不变（罚没只转移余额，不改变共识权重，
  从而保证调度与阈值始终确定）。
- 高度 `h`、轮次 `r` 的出块者：

  ```
  index = (h - 1 + r) mod N        （h >= 1，N 为验证者数）
  ```

  正常情况 `r = 0`；当轮值出块者缺席（例如被分区隔离）时，其余连通节点用
  `r = 1, 2, ...` 依次接力，直到出现本分区内的合法出块者。
- 每个区块必须由该槽位调度者签名，签名不匹配或调度不符一律拒绝。

### 2. 规范链选择（Fork Choice）

节点保存完整区块 DAG，同一高度可同时存在竞争区块。在所有"链尖（tips）"中：

1. **终局锚点约束**：候选链必须在最后终局高度上包含终局区块；不满足的候选
   （例如终局前的旧分叉块）被直接排除。
2. 从两条链**第一个出现分歧的高度**开始逐槽比较：
   - 先比轮次：`round` 更小的区块胜出；
   - 轮次相同再比区块哈希：字典序更小者胜出。
3. 若一条链是另一条链的严格前缀，则更长的链胜出。

该规则只依赖区块内容，与消息到达顺序无关，因此全网节点对同一区块集合必然
选出同一条规范链。

### 3. 投票与终局性

- 每个验证者在槽位 `(height, round)` 至多有**一张有效票**，对应该槽位看到
  的规范区块；投票需签名并指定区块哈希。
- 终局阈值在创世中配置：

  ```
  required = floor(totalWeight * thresholdNumerator / thresholdDenominator) + 1
  ```

  默认 `2/3 + 1`（严格大于三分之二权益）。
- 某槽位同一区块的累计投票权重 `>= required` 时，该区块（及其祖先）进入
  **终局**：
  - 之后收到的任何高度 `<= 终局高度` 且哈希不同的区块，一律丢弃，不触发
    回滚，也不会阻断后续合法区块的处理；
  - 规范链永远在终局链之上延伸。

### 4. 作恶举证与罚没

可举证的两类冲突（`internal/types/evidence.go` 中的 `Evidence`）：

- `EquivocationBlock`：同一出块者在同一 `(height, round)` 签了两个哈希不同
  的区块（双提案）。
- `EquivocationVote`：同一验证者在同一 `(height, round)` 对两个不同区块哈希
  投票（双签）。

检测到的证据会在节点间 gossip，被后续区块的出块者打包进块。区块执行时：

1. 校验证据内两个签名均有效、作恶者一致、槽位一致且对象确实冲突；
2. 按创世罚没率 `SlashRateNumerator / SlashRateDenominator`（默认 10%）扣除
   作恶者**余额**，等额转入**国库账户**——只在账户间转移，不销毁，因此
   **总余额/总权益守恒**；
3. 证据以其哈希在状态中去重：同一证据无论出现在多少区块或分支上，**最多
   罚没一次**。

> 注意：进入新一轮 `round` 的投票是合法的视图切换，不属于作恶；作恶严格
> 限定在同一槽位内。

### 5. 交易池、打包顺序与状态根

- 交易为带 ed25519 签名的转账：`nonce / amount / fee`。
- 准入与执行按固定顺序给出**可区分的错误**：
  - `state.ErrBadSignature` 签名错误；
  - `state.ErrUnknownSender` 发送方账户不存在；
  - `state.ErrBadNonce` 序号不连续（乱序被拒绝）；
  - `state.ErrInsufficient` 余额不足（amount+fee）；
  - `state.ErrOverflow` 溢出；
  - `mempool.ErrAlreadyPending` 重复交易；
  - `mempool.ErrConflictingNonce` 同发送方同序号但内容不同（冲突）。
- 打包顺序完全确定：**费用高者优先；费用相同按发送方地址升序；再按 nonce
  升序；最后按交易哈希升序**。
- 区块头包含两个承诺：
  - `TxRoot`：区块内交易哈希列表的 Merkle 根；
  - `StateRoot`：执行全部交易与证据后，按地址升序排列的账户叶子
    `hash(addr || nonce || balance)` 的 Merkle 根。

  执行是纯函数：相同父状态 + 相同区块内容 ⇒ 所有节点算出**完全相同**的
  `StateRoot`；区块头声明的状态根与重算结果不一致的区块会被拒绝。

## 本地多节点验证

无需任何外部依赖，标准 Go 工具链即可。

```bash
# 1) 运行端到端演示（4 个验证者：出块→投票→终局→双签→罚没）
go run ./cmd/simnode

# 2) 运行全部测试
go test ./...

# 3) 打开竞态检测运行（并发消息/并发出块场景）
go test -race ./...

# 4) 只看某类场景的详细判定日志
go test ./test/ -run 'TestForkConvergence|TestCompetingBranchesDeterministic' -v
go test ./test/ -run 'TestDoubleVoteSlashing|TestFinalityProtectsFromRollback' -v
go test ./test/ -run 'TestConcurrent' -race -v
```

`cmd/simnode` 会打印每个节点的输入与判定依据（接受/拒绝、分叉切换、计票
权重、终局锚定、证据检测与罚没数额），例如：

```
[n3] block ACCEPT h=4 r=0 hash=2e9f2e21... parent=748535e5... stateRoot=20887356...
[n3] fork-choice head SWITCH 748535e5... -> 2e9f2e21... (h=4 r=0)
[n2] FINALITY h=6 r=0 block=a0b33b9f... weight=100/67 (ancestors through genesis anchored)
post-slash balances -> offender=993 treasury=110 total=5000 (conserved=true)
```

## 测试覆盖

| 测试 | 验证内容 |
| --- | --- |
| `TestStateTransferAndRoot` | 转账执行、序号推进、两份独立状态的状态根一致、总量守恒 |
| `TestSlashConservation` | 罚没金额转移到国库且总量守恒 |
| `TestTxRejectionReasons` | 坏签名/乱序/余额不足/未知账户/重复/同序号冲突/打包排序 |
| `TestForkConvergence` | 分区产生分叉、延迟与重复消息、恢复后规范链统一、状态根一致 |
| `TestCompetingBranchesDeterministic` | 同高度多轮竞争区块，按"低 round 再低哈希"全网唯一选择 |
| `TestDuplicateBroadcastIdempotent` | 重复广播幂等，不重复存储/计票 |
| `TestFinalityProtectsFromRollback` | 终局后注入竞争块被丢弃，终局块不可回滚 |
| `TestDoubleVoteSlashing` | 投票双签证据检测/传播/上链/罚没，全网状态根一致且守恒 |
| `TestSameEvidenceSlashOnce` | 同一证据不会被重复罚没 |
| `TestDoubleProposeEvidence` | 同槽双提案被检测为出块双签证据 |
| `TestConcurrentMessageDelivery` | 并发重复消息（`-race`）无数据竞争、结果一致 |
| `TestConcurrentProposalsSameHeight` | 同高度竞争块以不同顺序并发到达，结论仍唯一 |

## 设计边界（有意为之的简化）

- 验证者集合与权重在创世固定，不包含运行期质押/解绑/权重变更交易。
- 区块时间戳不参与 fork choice（演示中固定为 0），选择完全由 round 与哈希
  决定，从而排除时间依赖。
- 网络是可靠的进程内队列：分区时消息保持、恢复后补发；重复与乱序由协议层
  幂等吸收，不模拟拜占庭式消息篡改（签名本身会拒绝伪造内容）。
