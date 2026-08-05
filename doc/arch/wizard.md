# 端到端 7 步向导

## 步骤

1. **主题与对标** — 导入 radar 账号
2. **选题卡片** — ideation 跨类目迁移，3–5 张
3. **Hook 三选一** — 3×Writer + 划走理由，目标 30s 内确认
4. **脚本定稿** — scriptagents 打磨 → seg 工程
5. **素材与音轨** — hybrid 规划 + compliance 校验
6. **720p 预览** — render 720p；改一句 → recompile 仅失效子树
7. **1080p 出片** — render 1080p + label 注入 + YouTube synthetic=true

## API

```
POST /v1/wizard/sessions
GET  /v1/wizard/sessions/{id}
POST /v1/wizard/sessions/{id}/advance
```

## WebUI

`/wizard/1` … `/wizard/7` 调用上述 API。会话 ID 在步间由前端 state 持有；刷新后可用 GET 恢复。

## 成本

`cost_micros` 累计脚本 + 渲染估算；超过 `$1`（1_000_000 micros）拒绝继续。

## 续跑

`status=failed` 时 `POST .../advance` 带 `"resume": true` 从 `failed_step` 继续。
