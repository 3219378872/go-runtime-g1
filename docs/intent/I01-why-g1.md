# I01 为什么做 G1

ID: I01。上层：无。下游：S01, S02, S03。

## 目标

- 在 Go 上验证 JVM G1 主算法：固定 Region、SATB、RSet、并发标记、疏散复制。
- 真实 fork 上实现暂停可预测：`stw_max` / `stw_p99` 优于上游，吞吐至少 parity。
- 来源：`README.md:3-13`。

## 非目标

- 不替换 Go 编译器/运行时收集器；模拟包用 `g1gc.ObjectID` 句柄（`README.md:15-19`）。
- 模拟包不做可扩展性优化，部分操作 O(heap)（`NOTE.md:702-703` Known Limits）。
- fork 未达稳定性能 parity 前，不宣称超越上游；吞吐 parity 或更好仅在无 GODEBUG 默认路径成立（`NOTE.md:696-701`）。

## 迁移来源

- 本节由 `README.md:3-19`（算法清单+句柄约束）与 `NOTE.md:694-706`（Known Limits）迁移合并，原文件保留为档案，权威解读以本层为准。

## 成功标尺

见 I03。
