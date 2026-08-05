# 结构卡片与跨类目选题（Ideation）

## 定位

竞品雷达回答「什么在爆」；本模块回答「怎么把**结构**迁到你的类目」。护城河在选题 + 脚本，不在画面拼装。

**只迁结构、绝不迁事实。** 结构卡片六个维度描述形式（hook 类型、前 3 秒视觉模式、节拍序列、信息密度曲线、情绪动线、争议锚点），不含原视频的领域名词。迁移事实即抄袭且会产出错误内容。

## 数据来源

- 输入：雷达热点帖的 `post_id`、类目、标题/描述等元数据（通过 `POST /v1/ideation/extract`）。
- 不重复存储播放量——结构卡只引用 `source_post_id` + `source_category`。

## 存储模型

### 结构卡片 `structure_cards`

六维字段 + `embedding`（JSON float 数组）。向量**只负责召回候选**，最终选题靠结构迁移逻辑，不做纯向量检索排序。

### 图 `structure_edges`

有向边 `(from_id, to_id, rel)`，`rel` 取 `similar` / `variant` / `derived`。关系可查询；向量与图正交——边记录已知结构关系，向量找近似候选。

### 选题卡 `topic_cards`

每次迁移产出 3–5 张，硬约束 `MinTopics=3`、`MaxTopics=5`。每张含：

- `title`、`angle`
- `migration_source`（来源结构卡 id）
- `why_fits`（为何适合用户主题）

## 提取与迁移

| 组件 | MVP 实现 | 生产扩展 |
| --- | --- | --- |
| `Extractor` | `RuleExtractor`（确定性，单测） | `LLMExtractor`（接 provider） |
| `Migrator` | `RuleMigrator`（确定性，单测） | `LLMMigrator` |

`ContainsForbiddenTerms` / `ForbiddenTerms` 是验收断言钩子：给定爆款标题中的领域词，提取结果不得包含。

## HTTP 接口

```
POST /v1/ideation/extract
GET  /v1/ideation/cards?category=
GET  /v1/ideation/cards/{id}     # 含 graph neighbors
POST /v1/ideation/migrate
GET  /v1/ideation/topics?card_id=
POST /v1/ideation/recall
```

`Deps.Ideation == nil` 时列表路由返回空集合；写路由返回 503——与 radar / recompile 一致。

## 验收对照

| 验收项 | 测试 |
| --- | --- |
| 六维齐全 | `card.Validate()` + `extract_test` |
| 不含领域事实 | `ContainsForbiddenTerms` 断言 |
| 跨类目 3–5 选题 + 迁移来源 | `migrate_test` + `ideation_test` API |
| 图可查询 | `GraphNeighbors` + store 测试 |
| 向量召回回归 | `vector_test` + recall API 测试 |

## 明确不做

- 纯向量检索选题（丢失结构关系）
- 迁移原视频事实/台词/数据
- 无上限选题列表
- 用户跳过「去事实」检查的开关
