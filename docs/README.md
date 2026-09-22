# 权益证明链核心（本地多节点模拟）

一个纯 Go、零外部依赖的 PoS 区块链核心：用**进程内确定性模拟网络**与**独立内存状态**
承载多个验证者节点，演示出块、投票、终局性、罚没与状态承诺在节点间保持一致且可验证。
不监听任何端口、不依赖真实时间或 goroutine 调度，所有消息都由测试**显式投递**，
因此结果完全可复现。

## 目录结构

| 包 | 职责 |
|----|------|
| `types` | 交易、区块、投票、证据的结构，确定性编码与 SHA-256 哈希 / 模拟签名 |
| `state` | 账户世界状态、交易执行、余额/序号、罚没、确定性状态根 |
| `pool`  | 交易内存池：准入校验、按费用/序号/到达序的确定性打包 |
| `consensus` | 验证者集合、加权轮转出块、分叉选择、投票池、终局性、证据与罚没 |
| `simnet` | 进程内消息交换机：队列、显式投递、延迟/乱序/重复/分区模拟 |
| `node` | 把上述组件装配成一个验证者进程（收块、出块、投票、广播、日志） |
| `cmd/demo` | 可直接运行的本地 4 验证者演示 |

## 运行

```bash
# 运行本地多节点演示
go run ./cmd/demo

# 运行全部测试（-v 可看到每个输入与判定依据日志）
go test ./... -v
```

## 一、验证者集合与出块调度

- 创世在 `consensus.MakeGenesis` 中给定验证者及其初始权益，并预注资账户。
- 验证者集合按地址排序，**加权轮转出块**：把每个验证者展开为长度等于其权益的
  连续槽位，构成一个长度为 `总权益` 的环；高度 `h` 的出块者由
  `ProposerAt(h) = ring[h % totalStake]` 决定。
  权益为 3 倍的验证者长期出块频率约为 3 倍（见 `TestWeightedRoundRobin`）。
- 该函数是纯函数：同样的创世集合在每个节点上对每个高度算出**唯一**出块者。
- 非调度验证者产生的区块被 `ErrWrongProposer` 拒绝（见 `TestConcurrentPropose`）。

## 二、规范链选择（确定性分叉选择）

`consensus/forkchoice.go` 中按**严格顺序**比较候选链头：

1. **终局保护**：只有"当前终局块的后代"才有资格（终局块不可回滚）；
2. **链更长者胜**：高度最大；
3. 高度相同：带 `>2/3` 正当性（justified）的链头胜；
4. 仍相同：**区块哈希字典序更小者胜**。

因此给定相同的区块集合，所有节点一定选出同一个链头——与区块到达先后、
是否有短暂分叉、是否重复广播无关（见 `TestForkConvergence`）。

重复广播同一区块返回 `ErrDuplicateBlock`，是无害幂等操作。

## 三、投票与终局性

- 每个验证者对自己当前规范链头每高度最多投一票（`Vote{height, voter, blockHash}`，
  带模拟签名）。
- 区块必须携带父块的 `JustifyVotes`，且这些投票满足严格多数才有效：

  ```
  quorum(total) = floor(2*total/3) + 1     // 严格大于 2/3
  ```

  校验只依赖**区块内嵌的投票与父块状态**（`JustificationPower`），不依赖接收节点
  本地恰好收到过哪些投票，所以"同一区块在所有节点要么都有效、要么都无效"。
- 投票权重 = 投票者在**目标区块执行后状态中的实时余额**，被罚没削减的权益
  不能再贡献终局票数。
- 一个区块获得法定人数后进入终局；终局高度单调不减。任何试图在"已被终局抛弃的
  分叉"上延续的区块都会被 `ErrFinalizedReorg` 拒绝（分叉测试中专门构造了带完整
  正当性的迟到区块来验证这一点）。

## 四、作恶检测与罚没

两类典型作恶，检测逻辑在 `consensus/votes.go` / `evidence_codec.go`：

| 作恶 | 定义 | 证据 |
|------|------|------|
| 双签投票 | 同一验证者在同一高度对**两个不同**区块投票 | 两份带签名的规范投票编码 |
| 双 proposal | 同一出块者在同一高度提出两个哈希不同的区块 | 两份规范区块编码 |

- 同一高度对**同一**区块重复投票只视为重复广播，不罚。
- 证据必须通过 `verifyEvidence`：高度一致、作恶者一致、目标哈希不同、签名有效；
  伪造 / 篡改证据被 `ErrEvidenceInvalid` 拒绝（见 `TestEvidenceVerification`）。
- 证据随区块上链，执行区块时按作恶者当前余额的 **1/3** 销毁（`SlashFraction=1/3`），
  金额封顶为其当前余额，永不出现负余额。
- **守恒**：交易手续费与罚没金额都被"销毁"（burn），并沿分支累计在
  `BlockNode.burn`。任何高度都满足

  ```
  当前所有账户余额之和 + 累计销毁量 == 创世总余额
  ```

  `TestDoubleVoteSlashing` 在每个节点上校验该等式以及精确的 1/3 罚没额。

## 五、交易池与状态承诺

- 交易字段：`from/to/nonce/value/fee/...`，签名覆盖这些字段（`types.SignTx`）。
- 打包顺序（`pool.Ordered`）完全确定：

  1. `fee` 降序；
  2. 同费 `nonce` 升序；
  3. 再相同按到达序号 `seq` 升序。

  同一发送者必须严格按 nonce 连续执行，跳号 / 重放 / 余额不足的交易被跳过。
- 拒绝原因**可区分**（独立的 sentinel error）：

  | 错误 | 场景 |
  |------|------|
  | `ErrBadSignature` | 签名缺失或不匹配 |
  | `ErrNonceTooLow`  | 序号落后（重放） |
  | `ErrNonceTooHigh` | 序号超前（乱序 / 跳号） |
  | `ErrInsufficient` | 余额不足以支付 value+fee |
  | `ErrOverflows`    | value+fee 溢出 |
  | `pool.ErrAlreadyKnown` | 重复交易 |

- **状态根**：对账户按地址排序后逐个编码 `(nonce, balance)` 再 SHA-256
  （`state.Root`）。相同状态在每个节点必然得到相同根；区块执行后把结果写入
  `Block.StateRoot`，接收节点用克隆状态重放并比对，不一致即 `ErrBadStateRoot`。
  `TestStateRootConsensus` 在多个高度、多节点间逐一核对状态根完全一致。

## 六、模拟网络如何保证测试确定性

`simnet.Network` 为每个节点维护一个 FIFO 队列：

- `Gossip` / `GossipTo` / `SendTo` 只入队，不执行；
- `DeliverOne` / `DeliverN` / `DeliverAll` 才按固定注册顺序同步投递；
- `Requeue`（延迟）、`Duplicate`（重复广播）、`Drop`（丢包/分区）可精确编排
  消息场景。

因此分叉、双签、并发出块测试都**不使用 sleep、不使用真实网络、不依赖调度时序**。
拜占庭行为通过"同一验证者身份的第二个进程"（twin）自然产生：它独立执行状态、
独立出块，从而在同一高度提出不同但结构合法的区块。

## 七、测试覆盖

```bash
go test ./... -v
```

- `TestStateRootConsensus`：多高度多节点状态根一致、交易生效。
- `TestForkConvergence`：双 proposal 分叉、延迟/分区/重复广播下规范链收敛、
  终局后迟到分叉块被 `ErrFinalizedReorg` 拒绝。
- `TestDoubleVoteSlashing`：双签投票识别、证据上链、1/3 罚没、余额守恒。
- `TestConcurrentPropose`：同一高度所有验证者并发出块，仅调度者有效。
- `TestRejectReasons` / `TestPoolRejectAndOrder`：拒绝原因可区分、费用序确定。
- `TestWeightedRoundRobin` / `TestQuorum` / `TestEvidenceVerification`：
  调度比例与周期、法定人数阈值、证据校验。

运行 `-v` 时每条输入（区块/投票/交易）与每个判定依据（接受、拒绝原因、
分叉胜出哈希、证据收集、罚没额、终局高度）都会打印到测试日志。
