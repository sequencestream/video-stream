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

所有写请求携带 UUID `operation_id`。`advance` 还须携带 GET/上次响应中的
`expected_version`；重复 operation 返回首次结果，过期版本返回最新会话，避免重试串步。

## 调用方

编排没有界面。会话 ID 由调用方自己保存，服务重启后通过 GET 恢复，一律以服务端
`current_step` 为准——步骤进度是服务端状态，不是客户端记账。

## 成本

`cost_micros` 累计脚本 + 渲染估算；超过 `$1`（1_000_000 micros）拒绝继续。

## 续跑

`status=failed` 时，用新的 operation id、当前 version 和 `"resume": true` 调用 advance。
服务端从失败 operation journal 读取原输入并从 `failed_step` 继续。服务启动会把遗留的
`running` operation 标记为 `interrupted`，等待用户显式续跑，不自动重复外部调用。

SQLite 内的会话、成本和渲染记录严格幂等。YouTube 等平台调用使用稳定上传记录尽力去重；
平台已接受但本地尚未来得及确认时崩溃，不承诺跨系统 exactly-once。
