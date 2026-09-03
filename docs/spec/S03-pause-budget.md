# S03 暂停预算契约

ID: S03。上游：I01, I03。下游：D04, D05。实现：M01#collect-select, M02#threshold。证据：E02#b1270/p1b/p2。

## Given/When/Then

- Given 疏散窗口，When 选择 CSet，Then 待拷贝字节有界（fork copy budget 256 KiB，`NOTE.md:458`；模拟包 `pauseEstimate` 见 `collect.go`）。
- Given owner-span 投影集超 512 spans 或 live 对象超 16384，When 评估窗口，Then 整体 defer（`NOTE.md:644-650`）。
- Given inbound 索引溢出，When 疏散前检查，Then 整体 defer，不做无界全堆重写（`NOTE.md:645-646`）。
- Given 窗口分配区（window-alloc regions），When STW 重写，Then 纳入覆盖；list 溢出时降级为保守 defer（`NOTE.md:399-408`）。

## 频率界（由 NOTE 迁移）

- 窗口触发阈值 `max(2GiB, 8x heapLive)` + epoch 间隔 >= 32 cycles（`NOTE.md:483-486,507-512` 调整史：4GiB/16x → 2GiB/8x；无界窗口密度曾致 gc_cpu 1.28x / stw_max 3.6x）。
- 拷贝预算史：4 MiB → 1 MiB（`NOTE.md:644-650`）→ 256 KiB（`NOTE.md:452-458`）；选择扫描 724us → 40us（`NOTE.md:514-518` 有界 tagging）。

## 迁移来源

- 数值全部取自 NOTE 对应迭代，代码锚点见 M02#engagement；怀疑项（如 inline-mark 处理）未证实的不入契约，只入 D04 拒绝项。
