# S02 疏散安全契约

ID: S02。上游：I01, I03。下游：D02, D03。实现：[M02 疏散重写](../impl/M02-fork-map.md#evacuate-rewrite)。证据：[E02 p0-fix-frag](../evidence/E02-bench-matrix.md#p0-fix-frag)、[E03 stress](../evidence/E03-correctness-stress.md#stress)。

## Given/When/Then

- Given frag 负载 + `g1gc=1,g1evac=1`，When 用 `STRESS_GODEBUG` 显式选择该配置运行 `bench/stress.sh`，Then 进程成功退出且无脚本列出的故障串。脚本默认使用 `g1evac=4,g1trace=1` 诊断配置；非诊断配置不会执行所有重写和记账自检。
- Given `g1evac>=4` 诊断模式，When 会计刷新或对象疏散发生，Then 分别执行 `g1gcValidateIncremental` 或 `g1gcVerifyFullRewrite`；重写 miss 计入诊断，`g1evac>=5` 输出逐 slot dump（含 `listed/abit/mbit/imc/tdbit`）。
- Given 模拟器选中对象被 pinned 或复制空间不足，When `evac.go#evacuateLocked` 返回 `ErrEvacuationFailure`，Then 失败 Region 保留 live 对象，周期仍 complete。Runtime fork 的索引溢出/投影超限走 S03 的 defer 路径，不使用模拟器的错误 API。

## 历史门（由 NOTE 迁移）

- `NOTE.md` 2026-08-24d 记录 frag 残留重写 miss（单数至约 250），当时 `g1evac=1` 尚不安全；mark-bit 布局是调查中的假设，后续原因见下一条。
- `NOTE.md` 2026-08-25 记录 cached-span 记账、粘性快照时序、worker 节点存储三项修复；当次 frag + `g1evac=4,g1trace=1` 10/10 clean，`g1evac=1` 5/5 clean。
- `NOTE.md` 2026-08-24e 以 A/B stress 记录迁移前后两个 fork 的相同故障行为；以上均为对应迭代证据，不替代当前提交的安全性验证。

## 迁移来源

- 历史来源为 `NOTE.md` 2026-08-24d、2026-08-25；当前诊断入口为 `g1gc_evac_rewrite.go#g1gcVerifyFullRewrite`、`g1gc_account.go#g1gcValidateIncremental`，压力测试配置与故障串以 `bench/stress.sh` 为准。
