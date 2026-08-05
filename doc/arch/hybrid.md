# 混合画面生成（Hybrid visual）

## 路线

| Route | 用途 |
| --- | --- |
| `ai_video` | 情绪/一致性 hook 镜头（MVP 默认 60s 仅 1 镜） |
| `stock_footage` | 口播/数据段，走 Pexels/Pixabay 授权素材 |
| `ken_burns_still` | 默认 filler：静帧 + 可复现 Ken Burns |
| `motion_graphics` | 信息密度高的统计/图表段 |

判据见 `internal/hybrid/decide.go`；每 seg 持久化 `route` + `reason` 到 `hybrid_shots`。

## MVP 预算

60s 成片默认 `MaxAIShots=1`，仅 hook（ordinal 0 + continuity + 情绪标签）消耗 AI 预算。

## 素材拉取

Pexels/Pixabay 客户端带重试；测试与无 key 环境使用 `FixtureStockSource`。返回 `license` + `attribution`。

## Ken Burns

`KenBurnsSeed(seg_id, text)` 决定 pan/zoom 参数，`ComputeKenBurns` 可复现。

## HTTP

```
POST /v1/hybrid/plan                      # 规划并持久化
GET  /v1/hybrid/plans/{project_id}        # 读取已存计划
```

无 engine 时：`POST` 503，`GET` 空列表。
