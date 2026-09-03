# S02 疏散安全契约

ID: S02。上游：I01, I03。下游：D02, D03。实现：M02#evacuate-rewrite。证据：E02#p0-fix-frag, E03#stress。

## Given/When/Then

- Given frag 负载 + `g1evac=1` 生产配置，When 跑 stress（`bench/stress.sh`），Then 零 `rewrite missed`、零 `accounting drifted`、零 `bad pointer` throw。
- Given `g1evac>=4` 诊断模式，When 每疏散窗口跑 `g1gcVerifyFullRewrite` + used-region recount，Then miss 计入诊断，`g1evac>=5` 输出逐 slot dump（含 `listed/abit/mbit/imc/tdbit`）。
- Given 疏散失败（pinned/预算/overflow），When `evacuateLocked` 返回 `ErrEvacuationFailure`，Then 失败 Region 保留 live 对象，周期仍 complete（`types.go:549-553`）。

## 历史门（由 NOTE 迁移）

- 2026-08-24d 前 `g1evac=1` 在 frag 上 UNSAFE：残留 miss 单数~250，`winreg=true` 全在覆盖区内，疑 mark-bit 布局（`NOTE.md:409-420`）；`g1gcVerifyFullRewrite` 把潜腐败转计数诊断（`NOTE.md:416-420`）。
- 2026-08-25 起 caveat 解除：cached-span 记账 + 粘性快照时序 + worker 节点堆分配三修复（`NOTE.md:206-284`）；frag + `g1evac=4,g1trace=1` 10/10 clean，生产 `g1evac=1` 5/5 clean。
- rebase 本身不引入回归：24e 以 A/B stress 确认两 fork 同行为（`NOTE.md:329-340`）。

## 迁移来源

- 本契约由 `NOTE.md:206-284,385-420` 收敛而成；诊断位定义见 `g1gc_evacuate.go:860 g1gcVerifyFullRewrite`。
