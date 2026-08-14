# CostWarden 与七级降级阶梯

## 目标

在**脚本定稿阶段**按 seg 结构预估总成本，超 `$1`（1_000_000 micros）时在出片前自动降级，而非渲染中断。

## 供应商能力契约

`GET /v1/cost/capabilities` 返回 `Capability` 列表：supplier、tier、service、单价（micros）、可用性、限流。

不可用的 capability 在 **Level 1（同档换供应商）** 自动 failover。

## 七级降级阶梯

| Level | Action | 说明 |
|-------|--------|------|
| 0 | original | 原始计划 |
| 1 | switch_supplier_same_tier | 同档换供应商 |
| 2 | downgrade_tier | premium → standard → economy |
| 3 | downgrade_resolution | 1080p → 720p（视觉成本 ×0.65） |
| 4 | reduce_ai_shots | AI 镜头数降至 0 |
| 5 | ken_burns_still | 路线下限 Ken Burns |
| 6 | stock_footage | 路线下限素材 |
| 7 | motion_graphics_only | 最便宜路线 |

每次降级写入 `model.CostPlan.decisions`（原因、省下金额）。

## 预估口径

```
total = visual(segs, hybrid routes, resolution scale)
      + tts_per_seg × seg_count
      + script_cost_micros
      + render_overhead
```

**预估 vs 实际**：验收阈值 **±15%**（`WithinTolerance`）。

## HTTP

```
POST /v1/cost/estimate   # 仅预估，不降级
POST /v1/cost/plan       # 预估 + 阶梯直到入预算
GET  /v1/cost/capabilities
```

## 工程文件

`project.cost_plan` 随工程一起持久化，调用方在渲染前通过 `/v1/cost/plan` 取得并读取它。
