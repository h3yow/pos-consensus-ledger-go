# pos-consensus-ledger-go

纯 Go、零外部依赖的**权益证明链核心**：进程内确定性模拟网络承载多个验证者，
验证出块、投票、终局性、罚没与状态承诺在节点间一致且可验证。投票与出块用
**Ed25519 真实身份密钥**签名（公钥在验证者集合、私钥只在本节点），交易池带
**容量上限与可预测淘汰**，罚没权益进入**全网一致的协议公共账户**（计入状态根、
账目守恒、罚后权重随余额下降）。

## 快速开始

```bash
go run ./cmd/demo     # 本地 4 验证者演示（无端口、无真实网络、无时序依赖）
go test ./... -v      # 全量测试，打印每个输入与判定依据
```

## 测试场景

- 分叉收敛（延迟 / 重复广播 / 分区）与终局保护：`node/fork_test.go`
- 双签投票 / 双 proposal 识别、验签、1/3 罚没入**协议公共账户**、余额守恒、权重下降：`node/slashing_test.go`、`node/identity_test.go`
- 并发出块仅调度者有效：`node/concurrent_test.go`
- 跨节点状态根一致（含公共账户）：`node/root_test.go`、`node/identity_test.go`
- 交易池容量淘汰 / 序号连续 / 同序冲突 / 重排一致 / 并发压力：`pool/capacity_test.go`、`pool/concurrent_test.go`
- 伪造投票、伪造/篡改证据被拒且不误罚：`node/identity_test.go`、`consensus/consensus_test.go`
- 交易拒绝原因可区分、费用序确定：`state/`、`pool/`

完整共识规则、罚没 / 终局条件与本地多节点验证方法见
[`docs/README.md`](docs/README.md)。
