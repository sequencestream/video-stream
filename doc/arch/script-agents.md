# 多 Agent 脚本打磨（Script agents）

## 定位

脚本质量是护城河。质量来自结构约束——Critic 只诊不治、特征级杂交、写死终止——而非堆叠互评 Agent。

## Agent 与 Skill

| 角色 | 输出 |
| --- | --- |
| Writer ×3 | question / story / contrarian 三稿并行 |
| Audience-Simulator | 3s/8s/15s：`{second, reason}`，禁止好坏评价 |
| Critic | 问题 + 证据位置；含代写则 `ErrCriticRewrote` |
| Judge | 排名 + 触发 hook 杂交 |
| Skills | 事实核查、平台政策、呼吸点、成本（确定性） |

## 终止条件（配置化）

`script_agents.max_rounds=3`、`metric_improvement_min=0.03`、`max_new_issues=1`（即新增有效问题 <2）、`stagnant_rounds=2`。

## HTTP

`POST /v1/script/polish` → 合法 `model.Project` + token/cost 记录。

## 验收

见 `internal/scriptagents/scriptagents_test.go`。
