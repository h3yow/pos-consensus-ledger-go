# 权益证明链核心（本地多节点模拟）

一个纯 Go、零外部依赖的 PoS 区块链核心：用**进程内确定性模拟网络**与**独立内存状态**
承载多个验证者节点，演示出块、投票、终局性、罚没与状态承诺在节点间保持一致且可验证。
不监听任何端口、不依赖真实时间或 goroutine 调度，所有消息都由测试**显式投递**，
因此结果完全可复现。

本轮在此基础上补齐了三块"长期在线 / 经得起对抗"的能力：

1. **有容量、有明确淘汰口径的交易池**（容量上限、按手续费/到达序淘汰、序号连续、
   同序冲突可区分拒绝、跨节点重排后结果一致）；
2. **作恶证据与验证者真实身份绑定**（Ed25519 公钥在验证者集合中、私钥只在本节点，
   投票与 proposal 都可被任何节点独立验签，伪造/篡改/张冠李戴的证据一律拒绝）；
3. **罚没资金有明确去处且账目守恒**（罚没进入全网一致的协议公共账户、计入状态承诺、
   罚后投票权重按剩余余额下降）。

## 目录结构

| 包 | 职责 |
|----|------|
| `types` | 交易/区块/投票/证据结构，确定性编码，**Ed25519 验证者身份与签名**，SHA-256 哈希 |
| `state` | 世界状态、交易执行、余额/序号、**罚没入协议公共账户**、确定性状态根 |
| `pool`  | 交易内存池：**容量上限与淘汰策略**、准入校验、确定性打包，goroutine-safe |
| `consensus` | 验证者集合（含公钥）、加权轮转出块、**验签**、分叉选择、投票池、终局、证据 |
| `simnet` | 进程内消息交换机：队列、显式投递、延迟/乱序/重复/分区模拟 |
| `node` | 把组件装配成验证者进程（启动时带自己的私钥；收块、出块、投票、广播、日志） |
| `cmd/demo` | 可直接运行的本地 4 验证者演示 |

## 运行

```bash
go run ./cmd/demo          # 本地 4 验证者演示
go test ./... -v           # 全量测试，打印每个输入与判定依据
go test ./... -race        # 含并发压力的竞态检测
```

---

## 一、验证者身份与签名核验（本轮新增）

- **密钥模型（Go 标准库 Ed25519，无外部依赖）**：
  - 每个验证者有一对密钥。**公钥**放在创世验证者集合 `types.Validator{Addr, Stake, PubKey}`
    里，随配置分发给所有节点——这是核验"谁发出的"所需的**唯一**公开信息；
  - 每个节点启动时通过 `node.Config.PrivKey` 只带**自己那把私钥**，私钥从不广播；
  - 地址与密钥**密码学绑定**：`Addr = 最后20字节(SHA-256(pub))`。创世建集合时
    （`consensus.NewSet`）会强校验 `AddressOf(PubKey)==Addr`，配置错了启动即 panic。
    测试/演示用 `types.GenerateKey(seed)` 由种子确定性派生密钥（替代真实密钥托管）。
- **投票验签**：`Vote{height, voter, blockHash, sig}`，`sig = Ed25519(priv, CanonicalVote)`。
  节点收到投票（`VotePool.AddVote`）必须同时满足：
  1. `voter` 在验证者集合中；
  2. 用该验证者的**公钥**验签通过；
  3. 公钥派生地址等于投票里声称的 `voter`（防止"用 B 的钥匙签、却写 A 的名字"）。
  任一不满足返回 `ErrBadVote`（未知验证者或签名错误），投票不入池、不产生证据。
- **proposal 验签**：`Block.Sig = Ed25519(proposerPriv, CanonicalBlock)`（签名本身不参与
  被签内容，也不影响区块哈希，诚实重广播仍是同一个块）。`AddBlock` 在调度校验之后立即
  用**调度出块者的公钥**验签：无签名、伪造签名、用别人钥匙签的一律 `ErrBadProposerSig`，
  所有节点结论一致。区块内嵌的 `JustifyVotes` 也逐票按同一规则验签（`JustificationPower`）。
- **没有私钥就无法陷害**：双签证据（投票/proposal）两份载荷都必须能用**作恶者公钥**
  验签通过，且高度、作恶者字段、目标冲突关系全部对得上才有效（`verifyEvidence`）。
  伪造、篡改签名字节、两份钥匙不一致、`Offender` 字段张冠李戴，全部 `ErrEvidenceInvalid`，
  绝不会因此罚到诚实验证者。证据载荷采用长度前缀帧 `frame(canonical || sig)` 传输。
- **无害重复保留**：同一验证者对同一区块在同一高度重复广播同一条投票（height 与
  blockHash 都相同），视为重复广播，不报错、不产生证据。

> 说明：普通**交易**仍沿用模拟签名（对规范字段取 SHA-256），因为本轮的身份绑定针对
> 共识消息（出块/投票）。交易侧保留独立、可区分的准入错误（见第四节）。

## 二、出块调度与确定性分叉选择（既有，未改口径）

- 验证者按地址排序，**加权轮转出块**：展开为长度=总权益的环，
  `ProposerAt(h) = ring[h % totalStake]`，纯函数，各节点一致；非调度者出块
  被 `ErrWrongProposer` 拒绝。
- 分叉选择严格顺序：**终局保护 → 链更长 → 有 >2/3 正当性 → 区块哈希更小**。
  重复区块返回 `ErrDuplicateBlock`（幂等）。`quorum(total)=floor(2*total/3)+1`。
- 区块正当性只依赖**内嵌投票 + 父块状态**（`JustificationPower`），与本地收票时序无关；
  终局高度单调不减，在被终局抛弃的分叉上续块被 `ErrFinalizedReorg` 拒绝。

## 三、罚没资金去向与守恒（本轮改动）

- 罚没的权益**不再被销毁**，而是转入全网一致的**协议公共账户**
  `state.ProtocolAccount`（零地址，协议级账户、无对应私钥可花费）：

  ```
  state.Slash(offender, x):
      x = min(x, offender 余额)
      offender 余额 -= x
      ProtocolAccount 余额 += x
  ```

- 公共账户是普通状态账户，其余额天然进入 `state.Root()` 状态承诺，因此不同节点对
  "同一笔罚没最后去了哪里、去了多少"必然提交到同一个状态根。
- **守恒口径**（任何高度、任何节点都能核出同一份账）：

  ```
  所有账户余额之和（含协议公共账户） + 累计销毁手续费 == 创世总余额
  ```

  其中只有**交易手续费**按既有约定销毁（沿链累计在 `BlockNode.feeBurn`，可经
  `Chain.FeeBurnOn` 读取；旧名 `BurnOn` 仍保留）。`demo` 与测试都会打印并断言该等式。
- **罚后权重下降**：投票/正当性权重等于验证者在目标状态中的**实时余额**。被罚后其
  可用余额下降，能贡献给法定人数的权重同步下降；被罚走的部分在公共账户里，不再是
  作恶者的票力。
- 罚没金额仍按作恶者**当前余额的 1/3**、封顶到余额，永不为负；同一高度既双 proposal
  又双投可能被连续两次 1/3（测试按链上证据的执行顺序回放期望值，而不是假设只有一次）。
- 同一作恶证据在任一有效块中至多被打包一次（`consumed` 去重），保证竞争分叉的状态根
  不会因重复罚没而分叉。

## 四、交易池：准入 / 容量 / 淘汰口径（本轮新增）

### 准入（结论可区分，各自独立 sentinel）

| 错误 | 触发条件 |
|------|----------|
| `state.ErrBadSignature` | 交易签名缺失/不通过 |
| `state.ErrOverflows` | `value+fee` 溢出 |
| `state.ErrNonceTooLow` | 序号落后于已提交序号（重放） |
| `state.ErrInsufficient` | 余额不足 `value+fee` |
| `pool.ErrAlreadyKnown` | **纯重复**：规范哈希相同的同一笔交易已在池中 |
| `pool.ErrSameNonceConflict` | 同发送者**同序号、内容不同**的另一笔交易；直接拒绝，不覆盖原交易 |
| `pool.ErrPoolFull` | 容量已满且新交易优先级不足以淘汰任何可淘汰者 |

一个 `(发送者, nonce)` 槽位同一时刻**只允许一笔交易**占位；后来的同序异容体被明确
拒绝，而不是替换。

### 容量与淘汰（结果可预测）

- `pool.New(st, capacity)` 设硬上限（`<=0` 表示不限；节点默认 `node.DefaultPoolCap=1024`，
  可经 `node.Config.PoolCap` 配置）。
- 池子满了以后，在"现有全部占用者 + 新来交易"中选**全局优先级最低者**处理：

  ```
  优先级 = 手续费高者胜；同费则更早到达者胜（即同费淘汰最新的）；
           仍相同按交易哈希排序（保证比较是全序、确定）
  ```

- **序号连续性硬约束**：只有"某个发送者当前等待中的**最大 nonce**"才允许被淘汰
  （淘汰尾巴不会制造断档）。中间/低序号交易即使全局手续费最低也被保护；搜索会跳过
  它们，去淘汰"可淘汰尾巴里优先级最低的"。若没有任何可淘汰者，或最低优先级就是新来
  交易本身，则新交易被 `ErrPoolFull` 拒绝、什么都不踢。
- 池内部用 `map[hash]` 与 `map[(from,nonce)]hash` 双索引，并有 `sync.RWMutex` 保护，
  并发 gossip 投递与块后 rebase 同时发生也安全（`-race` 下有压测）。

### 新块到来后的整理与跨节点一致

- `Rebase(newState)` 按**到达序**把幸存交易重新准入：已上链/重放的因 nonce 落后丢弃，
  其余保留并重新编号，整个过程不再触发淘汰（幸存数不会超过容量）。
- 出块打包 `Ordered(limit)` 是幸存集合的**纯函数**：`fee 降序 → nonce 升序 → 交易哈希
  升序`。跳号/重放/余额不再够的交易被跳过，返回列表可直接顺序执行。
- 因此，只要各节点持有的等待集合相同，**保留谁、按什么顺序出块在每个节点上完全一致**，
  与本地到达先后无关（到达序只用于"同费留更早"的淘汰取舍）。模拟网络的全序 gossip
  本身也保证良连接节点看到的到达序一致。

### 状态根（既有）

对账户按地址排序后逐个编码 `(nonce, balance)` 再 SHA-256（`state.Root`）。相同状态在
每个节点得到相同根；公共账户余额为非零时同样包含在内。接收节点用克隆状态重放并比对
`Block.StateRoot`，不一致即 `ErrBadStateRoot`。

## 五、模拟网络与测试确定性

`simnet.Network` 为每个节点维护 FIFO 队列：`Gossip/GossipTo/SendTo` 只入队；
`DeliverOne/DeliverN/DeliverAll` 才按固定注册顺序同步投递；`Requeue/Duplicate/Drop`
可精确编排延迟、重复广播、丢包/分区。测试**不用 sleep、不连真网、不依赖时序**。
拜占庭行为由"同一验证者身份的第二个进程（twin）"自然产生——twin 持有该验证者私钥，
从而能制造**签名真实有效**的双 proposal / 双投；攻击者若不持有私钥则连一条能被接受的
伪造投票/证据都造不出来（这正是身份测试要证明的）。

## 六、测试覆盖

```bash
go test ./... -v
```

新增行为：

- 交易池
  - `TestPoolCapacityEvictionOrder`：容量打满时手续费/到达序的淘汰顺序，以及"便宜的中间
    序号交易被保护、只淘汰尾巴、序号不断档"。
  - `TestPoolSameNonceConflict`：纯重复 vs 同序异容冲突 vs 坏签名三类拒绝相互可区分。
  - `TestPoolRebaseDeterministic`：两个池子以相反顺序收同一批交易，rebase 后幸存集合与
    打包顺序一致。
  - `TestPoolConcurrentStress`（`-race`）：多 goroutine 持续压力下容量不被突破、槽位不重复。
  - `TestPoolCappedNode`（节点级）：小容量高压下保留高费交易、冲突拒绝、出块重排后两节点一致。
- 身份与证据
  - `TestForgedVoteRejected`：垃圾签名 / 用别人钥匙签却署他人名 / 未知验证者，一律拒绝；
    诚实验证者真实投票被接受；同一投票重复广播为无害重复。
  - `TestForgedProposalAndEvidenceRejected`：无签名/他钥签名 proposal 被 `ErrBadProposerSig`
    拒绝；篡改证据、两份钥匙不一致、`Offender` 字段对不上的证据一律 `ErrEvidenceInvalid`，
    不会误罚诚实验证者。
  - `TestEvidenceVerification`：合法双投证据成立、同目标不算作恶、篡改签名失败、他钥
    relabel 失败。
- 罚没去向 / 守恒 / 权重
  - `TestSlashToProtocolAccount`（state）：罚没**搬入**公共账户、总量不变、公共账户计入
    状态根、独立节点相同操作得到相同根。
  - `TestSlashToProtocolAccountConservationAndWeight`（多节点）：真实双投上链后，每个节点
    上作恶者余额、**公共账户余额、状态根**完全一致；`余额(含公共账户)+销毁手续费==创世`；
    作恶者投票权重下降为剩余余额。

无回归（仍在跑）：

- `TestForkConvergence`：双 proposal 分叉、延迟/分区/重复广播下收敛、终局后迟到分叉块
  被 `ErrFinalizedReorg` 拒绝。
- `TestConcurrentPropose`：同一高度并发出块仅调度者有效（冲突块改动后需用本人钥匙重签，
  否则先被签名校验拒绝）。
- `TestStateRootConsensus`：多高度多节点状态根一致、交易生效。
- `TestDoubleVoteSlashing`：双投识别、证据上链、按 1/3 顺序罚没、守恒（公共账户口径）。
- `TestWeightedRoundRobin` / `TestQuorum`：调度比例/周期、法定人数阈值。

`-v` 日志逐条打印输入（区块/投票/交易）与判定依据（接受、拒绝原因、分叉胜出哈希、
证据收集、罚没额与去向、公共账户余额、终局高度、状态根）。

## 七、本地多节点如何验证这几项新能力

```bash
# 1) 跑演示：末尾打印 live balances(含 protocol public account)、burned fees、
#    genesis total 与 conservation=true，并显示所有节点 head hash/state root 一致。
go run ./cmd/demo

# 2) 只看新能力
go test ./pool/ -v         # 容量淘汰/同序冲突/rebase 一致/并发压力
go test ./state/ -v -run SlashToProtocolAccount
go test ./node/ -v -run 'Forged|SlashToProtocol|PoolCapped|DoubleVote|Fork|Concurrent|StateRoot'

# 3) 竞态
go test ./... -race
```

关注点：

- **交易池**：日志里 `decision: ... evicted ...` 显示踢掉的是低优先级尾巴且 nonce 连续；
  冲突拒绝打印 `ErrSameNonceConflict`；两节点 rebase 后打包 fee 序列相同。
- **身份**：伪造投票/ proposal 日志为 `REJECT (...bad signature / unknown voter)`；
  被篡改证据的块在证据校验阶段就被拒，后续余额、状态根无任何变化（未误罚）。
- **罚没去向**：日志 `protocol public account=<x>`，且所有节点该行数值相同、状态根相同；
  `live + burned fees == 创世总量`；随后作恶者 `stake=<剩余余额>`。
