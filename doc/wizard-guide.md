# 7 步编排操作说明

这条流程走 radar → ideation → script → hybrid → render → label 的完整链路，每步落库、可断点
续跑。它原本为界面的分步交互而写；界面去掉后仍然保留，因为「卡在哪一步、从哪一步续」对
调用方一样有用。

只想从一段稿子出片，不需要这套编排——用 `vs video` 或 `vs project create` + `vs render`。

## 一次会话

```bash
# 1. 建会话（步骤 1），记下 session id 与 expected_version
curl -s -X POST localhost:8080/v1/wizard/sessions \
  -H 'Content-Type: application/json' \
  -d '{"topic":"home fitness","category":"fitness","operation_id":"'$(uuidgen)'"}' | jq

# 2. 查会话：current_step、status、各步产物
curl -s localhost:8080/v1/wizard/sessions/<id> | jq

# 3. 前进一步。每次写请求都要新的 operation_id，
#    expected_version 取上一次响应里的值
curl -s -X POST localhost:8080/v1/wizard/sessions/<id>/advance \
  -H 'Content-Type: application/json' \
  -d '{"operation_id":"'$(uuidgen)'","expected_version":1}' | jq
```

步骤依次是：1 setup、2 topics、3 hook、4 script、5 assets、6 preview（720p）、7 deliver（1080p）。
第 7 步完成后，成片路径在会话状态里。

## 幂等与并发

重复的 `operation_id` 返回首次结果，过期的 `expected_version` 返回最新会话而不执行——重试
不会串步。

## 续跑

`status=failed` 时，用新的 operation id、当前 version 和 `"resume": true` 调用 advance，从失败
的那一步继续。

## 成本闸

`cost_micros` 累计脚本与渲染估算，超过 `$1`（1_000_000 micros）拒绝继续。见
[`doc/arch/costwarden.md`](arch/costwarden.md)。
