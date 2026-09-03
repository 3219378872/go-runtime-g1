# D05 Region-aware 分配（refill 选密区）

ID: D05。上游：S03。实现：M02#dense-refill。证据：E02#ra1。

## 设计

- `mcentral.cacheSpan` 的 `partialSwept` 首 pop（最常见 refill 路径）在 `debug.g1gc != 0` 时走 `g1PreferDenseSpan`：再 pop 至多 7 个（`g1AllocChoiceSpans = 8`），留最优、余者推回同一 swept 集。
- 排名（`g1SpanAllocRank`，读 mark-termination 计数作 hint）：tier 0 密区（`live*2 >= used`，优先装填）> tier 1 未记录/空区（中性）> tier 2 本窗 `scanTag` 已选或稀疏（`live*2 < used`，留空待疏散）。密区内按 live 占比 cross-multiply 择优。
- STW-only 的 `scanTag` 读安全：refill 永不在 STW 跑。推回保持 swept 集不变式；返回 span 仍满足原 free-space 契约。`g1gc=0` 时行为逐字节一致。

## 拒绝/后续（由 NOTE 迁移）

- grow 时按密区取页（`mheap_.alloc` region 输入）：需动页分配器，invasive，defer（`NOTE.md` 2026-09-03b）。
- sweep 侧按 region 归还 span：与 `sweepgen/central` 所有权协议交织，defer。
- 每窗建 CSet 无条件消费：沿用 D04 拒绝结论。

## 来源

- `NOTE.md` 2026-09-03b；对照组 `NOTE.md` 2026-09-03a（`matrix-0903`）。
