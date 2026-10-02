# 权益证明链核心（本地多节点模拟）

一个纯 Go、零外部依赖的 PoS 区块链核心：用**进程内确定性模拟网络**与**独立内存状态**
承载多个验证者节点，演示出块、投票、终局性、罚没与状态承诺在节点间保持一致且可验证。
不监听任何端口、不依赖真实时间或 goroutine 调度，所有消息都由测试**显式投递**，
因此结果完全可复现。

## 目录结构

| 包 | 职责 |
|----|------|
| `identity` | Ed25519 验证者密钥：地址派生私钥、公钥公开、投票/出块签名与独立验签 |
| `types` | 交易、区块、投票、证据的结构，确定性编码与 SHA-256 哈希 / 模拟交易签名 |
| `state` | 账户世界状态、交易执行、余额/序号、罚没入公共账户、确定性状态根 |
| `pool`  | 有界交易内存池：准入校验、容量淘汰、序号连续性、确定性打包 |
| `consensus` | 验证者集合（含公钥 PKI）、加权轮转出块、分叉选择、投票池、终局性、证据验签与罚没 |
| `simnet` | 进程内消息交换机：队列、显式投递、延迟/乱序/重复/分区模拟 |
| `node` | 把上述组件装配成一个验证者进程（自持私钥、收块、出块、投票、广播、日志） |
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

## 二、验证者身份与签名核验（Ed25519）

身份由 `identity` 包用标准库 `crypto/ed25519` 的**非对称密钥**绑定，替代了此前
"谁都能算"的 SHA-256 模拟签名：

- **私钥只在本节点**：每个节点启动时携带自己的 Ed25519 私钥
  （`node.Config.PrivKey`，缺省时由地址确定性派生 `identity.PrivateKeyFor`，
  便于本地复现）。启动时校验私钥对应的公钥必须与验证者集合里公布的公钥一致，
  不一致直接 panic，绝不以"身份/密钥对不上"的状态运行
  （`TestNodeRejectsWrongPrivateKey`）。
- **公钥是创世 PKI**：`types.Validator.PubKey` 在创世验证者集合中公布。
  任何核验都只凭这一公开信息独立完成，不依赖消息来源节点。
- **投票**：对 `CanonicalVote(height,voter,blockHash)` 用投票者私钥签名
  （`identity.SignVote`）。接收方用集合中该地址的公钥 `Set.VerifyVote` 验签：
  未知投票者、签名缺失/错误、篡改目标块、或把别人的签名安到自己头上，一律拒绝
  （`TestVoteSignatureBinding`）。**没有某验证者私钥的一方无法伪造它的投票。**
- **出块**：区块的提案签名 `Block.ProposerSig` 覆盖"不含签名本身"的规范编码
  `CanonicalBlock`（`identity.SignBlock`）。区块哈希也只提交这份被签名的载荷，
  因此签名不可被替换成第二个身份（无延展性）。收块时先查调度者、再验提案签名，
  签名不符 `ErrBadProposerSig`（`TestProposerSignatureBinding`）。
- **同一验证者对同一区块重复广播同一条投票**：高度、投票者、目标哈希都相同，
  视为无害的重复广播，不报错、不产生证据（`TestDuplicateVoteHarmless`）。

## 三、规范链选择（确定性分叉选择）

`consensus/forkchoice.go` 中按**严格顺序**比较候选链头：

1. **终局保护**：只有"当前终局块的后代"才有资格（终局块不可回滚）；
2. **链更长者胜**：高度最大；
3. 高度相同：带 `>2/3` 正当性（justified）的链头胜；
4. 仍相同：**区块哈希字典序更小者胜**。

因此给定相同的区块集合，所有节点一定选出同一个链头——与区块到达先后、
是否有短暂分叉、是否重复广播无关（见 `TestForkConvergence`）。

重复广播同一区块返回 `ErrDuplicateBlock`，是无害幂等操作。

## 四、投票与终局性

- 每个验证者对自己当前规范链头每高度最多投一票（`Vote{height, voter, blockHash}`，
  带 Ed25519 签名，核验规则见第二节）。
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

## 五、作恶检测、证据验签与罚没资金去向

两类典型作恶，检测逻辑在 `consensus/votes.go` / `evidence_codec.go`：

| 作恶 | 定义 | 证据载荷 |
|------|------|------|
| 双签投票 | 同一验证者在同一高度对**两个不同**区块投票 | `规范投票 ‖ 64B Ed25519 签名` ×2 |
| 双 proposal | 同一出块者在同一高度提出两个哈希不同的区块 | `规范区块 ‖ 提案签名 ‖ u32签名长度` ×2 |

- 同一高度对**同一**区块重复投票只视为重复广播，不罚。
- 证据必须通过 `Set.verifyEvidence`（公开入口 `Set.VerifyEvidenceExport`），
  它对两份冲突对象**分别用作恶者公钥独立验签**，并核对：
  1. `Kind` 合法、高度与证据声明一致；
  2. 两份对象的投票者/出块者字段都等于 `Evidence.Offender`；
  3. 两个目标哈希确实不同；
  4. 两份签名都能用 `Offender` 的创世公钥验证通过，且作恶者在集合内。

  任何一项不满足都在**所有节点上一致地**返回 `ErrEvidenceInvalid`：
  - 垃圾/截断/无法解码的载荷；
  - 篡改对象或签名字节；
  - 攻击者用**自己的**私钥签了两份冲突对象，却把 `Offender` 字段改写成
    某个诚实验证者（签名与该字段对不上）；
  - 用他人公钥验不通的任何"伪造双签"。

  因此**没有诚实验证者的私钥，就拼不出一份能罚到它的证据**
  （`TestEvidenceCannotFrameHonestValidator`、`TestEvidenceVerification`）。
- 证据随区块上链，执行区块时按作恶者**当前**余额的 **1/3** 划转
  （`SlashFraction=1/3`）；同一区块内同一作恶者的多份证据依次作用，
  金额各自封顶为当前余额，永不出现负余额。

### 罚没资金的去向与守恒核对

- 罚没**不是销毁**，而是把权益从作恶者账户划转到全网唯一的协议公共账户
  `state.PublicTreasury`（固定的 20 字节知名地址，所有节点相同）。
  `state.Slash` 一次完成"作恶者减、公共账户加"，划转额即返回值。
- 公共账户是普通账户，**其余额天然计入状态根** `state.Root`，因此不同节点对
  "同一笔罚没最后去了哪里、公共账户剩多少"必然得出相同结论
  （`TestSlashFundsTreasuryConservation` 逐节点核对公共账户余额与状态根）。
- 全网守恒等式在**任何高度、任何节点**都成立：

  ```
  所有账户余额之和（含公共账户） + 累计手续费销毁量 == 创世总量
  ```

  交易手续费仍是唯一被销毁的量（沿分支累计在 `BlockNode.burn`，只加 `t.Fee`）；
  罚没只在账户间转移，所以仍计入"余额之和"。`TotalBalance()` 已包含公共账户，
  `TreasuryBalance()` 可单独核对；`TestSlashToTreasury` 在状态层验证
  "作恶者减少额 == 公共账户增加额"、总量不变、封顶不转负、公共账户进入状态根。
- 作恶者被罚后**可用余额下降，投票权重同步下降**：投票/正当性权重就是该验证者
  在目标块后状态中的实时余额，被罚走的权益（已转入公共账户）不再贡献终局票数
  （`TestSlashReducesVotingWeight` 直接比较罚前后该验证者的 `JustificationPower`）。

## 六、交易池：准入、容量与淘汰口径

- 交易字段：`from/to/nonce/value/fee/...`，签名覆盖这些字段（`types.SignTx`）。
- **容量上限**：`pool.NewWithCap(st, cap)`（`New` 用 `DefaultCapacity`）。
  池满后新交易按下面的优先级规则决定去留，取舍结果完全可预测。
- **准入校验顺序**（先到先判，失败原因两两可区分）：签名 → 溢出 → 余额 →
  序号落后 → 字节重复 → 同 `(发送者,序号)` 占位冲突。
- **一个序号槽位只允许一笔交易**：同一发送者同一 nonce，再来一笔内容不同的
  交易（哪怕费用更高）直接拒绝为 `ErrSameNonceConflict`，不替换在池交易；
  字节完全相同的才是 `ErrAlreadyKnown`。这两类与签名错、余额不足、序号问题
  互不混淆（`TestPoolSameNonceConflict`）。
- **池满淘汰口径**（`Add` 内部）：先把新交易暂存，若超出容量就反复淘汰
  "留存优先级最低"的在池交易——
  1. **手续费最低者先被淘汰**（高费优先留下）；
  2. 同费则**最新到达者先被淘汰**（更早到的优先留下）。

  为了**绝不因淘汰造成同一发送者等待序号断档**，当被淘汰者是发送者 S 的
  nonce k 时，会把 S 仍在池中所有 nonce ≥ k 的交易作为该连续后缀**一并淘汰**
  （只可能砍掉后缀，不可能在中间掏洞）。若级联最终连新交易自己也被淘汰，
  则回滚全部腾挪、池保持原样，新交易以 `ErrPoolFull` 拒绝——绝不会为了接纳
  新交易而打碎在池后缀（`TestPoolCapacityEviction`、
  `TestPoolEvictionCascadeRemovesSuffix`）。
- **打包顺序**（`pool.Ordered`）完全确定：

  1. `fee` 降序；
  2. 同费 `nonce` 升序；
  3. 再相同按到达序号 `seq` 升序（同费留早到）；
  4. 最后以交易哈希升序兜底（内容确定的决胜键，各节点一致）。

  同一发送者必须严格按 nonce 连续执行，跳号 / 重放 / 余额不足的交易被跳过。
- **新块到达后重整（Rebase）**：`Rebase` 把池底状态切到新头，并按到达序重新
  准入，静默丢弃已上链/重放/付不起的交易，保留到达序号。给定相同的创世、相同
  的已提交区块序列与相同的交易流，诚实节点在 rebase 后**保留集合一致、下一块
  打包顺序一致**（`TestPoolRebaseCrossNodeConsistency`）。
- 拒绝原因**可区分**（独立的 sentinel error）：

  | 错误 | 场景 |
  |------|------|
  | `ErrBadSignature` | 签名缺失或不匹配 |
  | `ErrNonceTooLow`  | 序号落后（重放） |
  | `ErrNonceTooHigh` | 序号超前（执行期乱序 / 跳号） |
  | `ErrInsufficient` | 余额不足以支付 value+fee |
  | `ErrOverflows`    | value+fee 溢出 |
  | `pool.ErrAlreadyKnown`     | 字节完全相同的重复交易 |
  | `pool.ErrSameNonceConflict`| 同发送者同序号已被一笔**不同**交易占位 |
  | `pool.ErrPoolFull`         | 池满且新交易优先级不足以挤掉在池后缀 |

- **状态根**：对账户按地址排序后逐个编码 `(nonce, balance)` 再 SHA-256
  （`state.Root`）。协议公共账户 `state.PublicTreasury` 也是普通账户，其余额
  一并进入状态根。相同状态在每个节点必然得到相同根；区块执行后把结果写入
  `Block.StateRoot`，接收节点用克隆状态重放并比对，不一致即 `ErrBadStateRoot`。
  `TestStateRootConsensus` 在多个高度、多节点间逐一核对状态根完全一致。

## 七、模拟网络如何保证测试确定性

`simnet.Network` 为每个节点维护一个 FIFO 队列：

- `Gossip` / `GossipTo` / `SendTo` 只入队，不执行；
- `DeliverOne` / `DeliverN` / `DeliverAll` 才按固定注册顺序同步投递；
- `Requeue`（延迟）、`Duplicate`（重复广播）、`Drop`（丢包/分区）可精确编排
  消息场景。

因此分叉、双签、并发出块、池压测试都**不使用 sleep、不使用真实网络、不依赖
调度时序**。拜占庭行为通过"持有同一验证者私钥的第二个进程"（twin）自然产生：
它独立执行状态、用同一私钥独立签名出块/投票，从而在同一高度提出不同但签名
真实的冲突对象——这正是证据可被独立验签、而外人无法陷害诚实验证者的原因。

## 八、测试覆盖

```bash
go test ./... -v
```

原有能力（无回归）：

- `TestStateRootConsensus`：多高度多节点状态根一致、交易生效。
- `TestForkConvergence`：双 proposal 分叉、延迟/分区/重复广播下规范链收敛、
  终局后迟到分叉块被 `ErrFinalizedReorg` 拒绝。
- `TestDoubleVoteSlashing`：双签投票识别、证据上链、1/3 罚没、守恒。
- `TestConcurrentPropose`：同一高度所有验证者并发出块，仅调度者有效。
- `TestRejectReasons` / `TestPoolRejectAndOrder`：拒绝原因可区分、费用序确定。
- `TestWeightedRoundRobin` / `TestQuorum`：调度比例与周期、法定人数阈值。

本轮新增：

- 交易池：`TestPoolCapacityEviction`（容量打满时高费留下、同费留早到）、
  `TestPoolEvictionCascadeRemovesSuffix`（淘汰只砍后缀、序号不断档）、
  `TestPoolSameNonceConflict`（同序号内容不同被拒，且与签名/余额/重放区分）、
  `TestPoolRebaseCrossNodeConsistency`（新块后各节点保留集合与打包顺序一致）。
- 身份/证据：`TestSignVerifyRoundTrip`（Ed25519 跨身份不可验）、
  `TestVoteSignatureBinding`（无私钥不能伪造投票）、
  `TestProposerSignatureBinding`（出块签名绑定、篡改/冒充被拒）、
  `TestEvidenceCannotFrameHonestValidator`（伪造/篡改/字段对不上的证据一致被拒、
  不误罚诚实验证者）、`TestDuplicateVoteHarmless`（同票重复广播无害）、
  `TestNodeRejectsWrongPrivateKey`（节点私钥与公钥不匹配即拒绝启动）。
- 罚没去向：`TestSlashToTreasury`（罚没进公共账户、总量不变、封顶、进入状态根）、
  `TestSlashFundsTreasuryConservation`（全网守恒 + 跨节点公共账户余额与状态根
  一致）、`TestSlashReducesVotingWeight`（罚后投票权重随剩余余额下降）。

运行 `-v` 时每条输入（区块/投票/交易）与每个判定依据（接受、拒绝原因、
分叉胜出哈希、证据收集、罚没额、公共账户余额、终局高度）都会打印到测试日志。

## 九、本地多节点如何验证这几项新能力

```bash
# 1) 直接跑本地 4 验证者演示（无端口、无真实网络、无时序依赖）
go run ./cmd/demo
# 结尾会打印 live(含公共账户) / feeBurn / 守恒结论 / 各节点 head 与 root 一致

# 2) 跑全量测试并阅读 -v 判定日志
go test ./... -v

# 3) 只看本轮三块能力
go test ./pool/   -run 'Capacity|Cascade|SameNonce|Rebase' -v
go test ./consensus/ -run 'Binding|Frame|Duplicate|Evidence' -v
go test ./state/  -run SlashToTreasury -v
go test ./node/ -run 'Treasury|VotingWeight|WrongPrivateKey' -v
```

手工核对守恒（任意高度、任意节点都成立）：

```
Σ 全部账户余额(含 state.PublicTreasury) + Chain.BurnOn(head)(仅手续费) == 创世总量
公共账户增量 == Σ 作恶者被罚金额
```
