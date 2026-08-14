# 核心数据模型：预算是区间，不是数字

## 立场

一句话立场：**`duration_budget` 必须是浮动区间，因为增量重编译只有在这个前提下才成立。**

渲染缓存的命中条件不是"参数一致"，而是两步：

```
render_cache_key 相同   且   缓存产物的实际时长 ∈ 本次的 duration_budget
```

如果预算是一个定值，第二个条件就退化成"这次 TTS 合成的毫秒数恰好等于上次"。合成引擎每次
输出都有几十毫秒抖动，这个等式基本不会连续成立两次——**于是每改一个字，全片重渲染**。
增量重编译不是工程量问题，是这条数据结构定不对就物理上做不成的事。

因此 `DurationBudget` 是闭区间 `[min_ms, max_ms]`，并且：

| 规则 | 值 | 理由 |
| --- | --- | --- |
| `min == max` | 拒绝 | 定值即上面那个失败模式 |
| 半宽上限 | ±8% | TTS 变速超过 8% 人耳能听出来，自然度就毁了 |
| 半宽下限 | ±2% | 再窄，改一个词就顶出区间，把邻接 seg 一起拖进重渲染 |

上限是音质约束，下限是增量重编译的存活条件，两条都是硬的。判定全部走整数
（`2*(min+max) <= 100*(max-min) <= 8*(min+max)`），不用浮点，免得边界随平台漂。
`NewDurationBudget(target)` 产出 `[ceil(0.92T), floor(1.08T)]`——**两端都向内取整**，
因为向外取整会让真实容差略微越过 8% 那条音质红线。

## seg：一个可独立渲染的片段

| 字段 | 语义 | MVP 有消费者吗 |
| --- | --- | --- |
| `seg_id` | 稳定身份。改文本不改 id | 是 |
| `text` | 这一段要说的话 | 是 |
| `content_hash` | 派生。`ch1:<sha256>`，标识"说了什么" | 是（TTS 缓存键） |
| `duration_budget_ms` | `{min_ms, max_ms}` 闭区间 | 是（校验、缓存第二道门） |
| `emotion_tag` | TTS 情绪档位 | 是（进 `content_hash`） |
| `breath` | 本段之后的换气停顿档位 | 是（进 `content_hash`） |
| `visual_prompt_slot` | 画面**槽位名**，不是提示词本身 | **否**，等画面意图 |
| `subtitle_breaks` | 允许断行的位置（rune 下标） | 是（字幕断句） |
| `depends_on` | 依赖的 seg id，构成 DAG | 是（拓扑序） |
| `continuity_group` | 同一个连续动作的 seg 编组，组内不可独立重编译 | 是（`continuity_broken` 边界） |
| `generation_batch` | 同一次多镜生成调用的 seg 编组 | 是（`batch_broken` 边界） |
| `render_cache_key` | 派生。`rk2:<sha256>`，标识"产物能否复用" | 是（增量重编译查缓存） |
| `protected` | 用户锁定，重生成不得覆盖 | **否**，重生成路径尚不存在 |
| `audio_source` | V2 真人素材通路占位，MVP 恒为 nil | **否** |

几个刻意的选择：

`visual_prompt_slot` 存的是槽位名字而不是提示词。提示词一旦进了 seg 就会进
`content_hash`，改一句画面描述会连音频缓存一起打掉——而这两件事本来毫无关系。

`breath` 是枚举（`none` / `short` / `long`）而不是毫秒数。写毫秒数是在假装 TTS 会精确遵守
我们给的数字；各家引擎实际上只认 SSML 的粗档位。`PauseMS()` 是我们自己的换算，不是供应商的
承诺，用枚举把这层不精确摆在明处。

**所有枚举字段的空串一律非法**，必须显式写 `neutral` / `none` / `kept`。理由不是洁癖：
如果 `""` 和 `"neutral"` 都表示中性，同一段内容就会有两个 `content_hash`，缓存永远打不满。
构造用 `NewSeg(segID, text, targetMS)` 给默认值，不靠零值。

`audio_source` 在 MVP 里没有生产者也没有消费者。它存在的唯一理由是 V2 接真人素材时不必改
结构、不必迁移历史文档——这是本模型明确接受的一笔冗余。

## 两个 hash 各自覆盖什么

| | `content_hash` | `render_cache_key` |
| --- | --- | --- |
| `text` | ✓ | ✓（经由 `content_hash`） |
| `emotion_tag` / `breath` | ✓ | ✓（同上） |
| `audio_source` | ✓ | ✓（同上） |
| `visual_prompt_slot` | ✗ | ✓ |
| `subtitle_breaks` | ✗ | ✓ |
| `render_profile`（voice / renderer / style_anchor） | ✗ | ✓ |
| `seg_id` | ✗ | ✗ |
| `duration_budget_ms` | ✗ | **✗** |
| `depends_on` / `protected` | ✗ | ✗ |
| `continuity_group` / `generation_batch` | ✗ | **✗** |

三处排除值得单独解释。

**`seg_id` 不进 hash**：两段文字相同的 seg 必须命中同一份 TTS 产物。把 id 放进去，缓存就
只能在"同一个 seg 的两次编辑之间"命中，命中率会低到没有意义。

**`duration_budget` 不进 `render_cache_key`**：这是前面那条立场的直接推论。预算若进了 key，
把预算从 `[920,1080]` 调到 `[1104,1296]` 就会丢掉一份实际时长 1100ms、本来仍然可用的产物。
预算不参与 key，而是命中之后作为第二道门——`Seg.CanReuse(cachedKey, cachedDurationMS)` 同时
检查这两件事。

**`continuity_group` / `generation_batch` 不进任何 hash**：它们描述的是"哪些 seg 必须一起
处理"，不是"这个 seg 长什么样"。放进 `render_cache_key` 会让重新分组打掉所有产物，而重新分组
恰恰是增量重编译要用边界机制来处理的事。见
[`doc/arch/incremental-recompile.md`](incremental-recompile.md)。

`RenderProfile{Voice, Renderer, StyleAnchor}` 由调用方传入，代表"哪套管线产出的这份产物"。
MVP 传零值，因为渲染器还不存在；接入真实渲染器时所有 key 会整体失效一次，而那时缓存里本来
也没有可用的产物，代价为零。

`style_anchor`（视觉基调）**必须**进 key，这是跨项目复用的正确性要求：
`SegsByRenderCacheKey` 会把另一个项目里 key 相同的 seg 当作可复用来源，基调不在 key 里，
两套视觉完全不同的项目就会互相复用对方的产物。

### 稳定性保证

1. **确定性**。不经过 JSON、不遍历 map、不用反射，同输入在任何机器、任何 Go 版本上同输出。
2. **版本自描述**。`ch1:` / `rk2:` 前缀随规则变化。规则改了，旧值可识别、可重算，
   而不是静默地对不上。`render_cache_key` 已经用掉一次：`style_anchor` 进 key 时
   `rk1:` 抬到了 `rk2:`，对应 schema v1→v2 的整份重 Seal。
3. **抗拼接歧义**。逐字段 `len|name|len|value` 编码，因此
   `voice="arenderer"` 与 `voice="a", renderer="renderer"` 不会撞——裸拼接会。
4. **文本任一字符变更必变**，包括前后空格、全角空格、emoji、零宽连接符。
   **不做 NFC/NFD 归一化**：归一化要引入 `x/text` 依赖，而且部分 TTS 引擎对两种形式的输出
   确实不同，抹平它是在说谎。

### 派生字段会陈旧，所以校验会重算

`content_hash` 与 `render_cache_key` 存在结构体里（`segs` 索引表需要它们作为列），
就有和 `text` 脱节的风险。处理办法：

- `Project.Seal()` 统一重算全部派生字段；
- `Project.Validate()` **重算并逐一比对**，不一致直接报 `ErrStaleDerived`，
  错误信息里点名"call Project.Seal after editing"。

不做"发现不一致就帮你自动重算"的兜底。陈旧的 `render_cache_key` 的外在表现是
"我改了文案但成片没变"，这是这套系统里最难查的一类 bug；自动重算会把"调用方忘了 Seal"
这个真实缺陷永久掩盖掉。

## 词级时间戳三层

```
Event ──┬─ Utterance ──┬─ Token{id,text,start_ms,end_ms,confidence,speaker,source,edit_state}
        │              └─ Token …
        └─ Utterance …
```

- **Token** 是字幕断句真正消费的单位。
- **Utterance** 通过 `seg_id` 把一串 token 系回它来自的 seg。
- **Event** 把若干 utterance 归成一个语义块（一场、一镜）。

MVP 由 TTS 对齐产出，`source` 恒为 `tts_align`；`asr` 与 `manual` 是给后续通路留的，
让不同来源的时间戳能共存在一条 timeline 上，同时让下游知道一个边界该信多少。

`edit_state` 是"只打标签不删数据"这条原则的落点：标成 `dropped` 的 token 保留原文与原始
时间戳，只是渲染时跳过，因此任何编辑都能撤销而不必重跑对齐。**MVP 不实现任何标签求值**——
纯 TTS 通路下既没有 filler 也没有需要处理的静音，写一个打标器等于空转。这里只交付字段与
取值校验。

校验的不变式：`start < end`；同层内严格递增且**不重叠**；`confidence ∈ [0,1]`；
全 timeline 内 id 唯一。不重叠是硬约束而不是警告——字幕断句遇到重叠区间会同屏输出两条字幕，
那是观众直接看得见的缺陷。

## seg 图

`depends_on` 构成 DAG。`Project.RenderOrder()` 返回确定性拓扑序，同层按 `seg_id`
字典序——顺序若依赖 Go 的 map 遍历，缓存决策就会带上一层只在生产环境才暴露的不确定性。

成环报 `ErrDependencyCycle`，**错误信息带完整环路径**（`depends_on cycle: a -> b -> c -> a`）。
在一张几百个 seg 的图里，只说"有环"等于没说。指向不存在 seg 的依赖报
`ErrUnknownDependency` 并点名缺失的 id。

## schema 版本与迁移

文档里带 `schema_version`，当前 `2`。

迁移**在 `map[string]any` 上做，不在当前版本的结构体上做**。用结构体迁移是个经典陷阱：
v2 删掉的字段在反序列化那一刻就没了，迁移函数根本看不到它要搬的数据。

`Migrate` 的硬规则：

- `schema_version` 缺失 → 报错。不猜、不默认——猜错版本号就会用错一串迁移步骤。
- 版本高于本二进制 → `ErrSchemaTooNew`，拒绝。**永不降级**：旧二进制回写新文档会静默丢掉
  它没有字段可以承载的数据。
- 链条中间缺一步 → 报错点名缺哪一段，不静默停在半路。

`DefaultMigrator` 当前有**一个步骤**：v1→v2。v2 加了三个字段并把 `render_cache_key` 前缀
抬到 `rk2:`，于是所有 v1 文档的派生字段都对不上重算结果——放着不管，这类文档读出来一切正常，
然后在下一次保存时报 `ErrStaleDerived`，把 schema 升级造成的问题算在调用方头上。
所以 `stepV1ToV2` 做的事只有一件：**整份重新 Seal**。

这一步是通过 `Project` 结构体往返做的，正是上面警告过的那个陷阱。它在这里安全只因为一个具体
理由——**v2 是 v1 的纯超集，没删任何字段**。以后任何删字段的迁移步骤都不能照抄这个写法。

迁移链本身的逻辑（顺序、缺口、越界）由测试自建的多步 migrator 覆盖，而不是在生产代码里硬塞
虚构的历史迁移。

## 持久化

```
projects(id, title, schema_version, document, created_at, updated_at)
segs(project_id, seg_id, ordinal, content_hash, render_cache_key,
     duration_min_ms, duration_max_ms, protected)     ← 派生投影
artifacts(render_cache_key, duration_ms, uri, cost_micros, created_at)
recompile_runs(id, project_id, planned_at, total_segs, invalidated_segs,
               full_rerun, boundary, cost_saved_micros)
```

**为什么整份文档存一列 JSON，同时又投影出一张表**：整个增量重编译的故事最终落在一条查询上
——"全库有没有哪个 seg 的 `render_cache_key` 等于这个值"。这必须走索引，不能扫文档。
另一边，模型加字段是高频事件，如果每个字段都是一列，每次加字段都要一次 DDL 迁移。

折中是：`document` 列是唯一权威，`segs` 是每次保存时在同一事务里**整体删掉重建**的派生投影，
只投影需要被查询的那几列。重建而不是增量 diff，是因为 diff 要处理改名与删除，算错就留下
一行孤儿，而孤儿行会被缓存查询当成有效命中返回。投影表可以随时从文档重建，所以它自己不需要
迁移。

`SaveProject` 在写库前调 `Project.Validate()`。存储是边界，边界上校验：一份不自洽的文档一旦
落盘，之后每个读者都得替它兜底。

`artifacts` 以 `render_cache_key` 为主键而不是 seg id——两个 seg 共用一份产物正是把 `seg_id`
排除出 key 的全部目的。`recompile_runs` 一次重编译一行，是失效率报告的全部证据来源。
两张表的语义见 [`doc/arch/incremental-recompile.md`](incremental-recompile.md)。

`SegsByRenderCacheKey` 交付时没有消费者，现在有了：增量重编译引擎靠它把另一个项目里 key 相同
的 seg 认作可复用来源。当初连同索引一起先建后用，省掉了渲染路径上来第一件事就改 schema。

## 明确不做

- **编辑标签求值**（`filler` / `silence` / `repeat`）。纯 TTS 通路下没有输入可处理。
- **可视化时间轴编辑器**，以及任何 UI。产品表面只有 CLI。
- ~~**HTTP / CLI 入口**~~。已交付。当初的判断是「形状应该由第一个真正使用它的意图来定」，
  那个意图后来出现了：agent 通过 CLI 驱动出片，必须能创建和读取工程。所以入口不是按
  想象中的 CRUD 建的——`POST /v1/projects` 收的是标题加口播稿而不是 seg 数组，因为手写
  seg 数组正是这条路径要消灭的东西。见 [`intake.md`](intake.md)。
- **数据库 DDL 迁移框架**。本次只有 `CREATE TABLE IF NOT EXISTS`，没有破坏性 DDL。
  一个零条记录的迁移框架是没被执行过的代码；等第一次真要改列时再写，那时才知道它该长什么样。
  文档级迁移（`model.Migrator`）是另一回事，已经交付。
- **Unicode 归一化**。
- **跨 project 共享 seg 行**。同一段文字在两个项目里是两行，它们靠相同的 `content_hash`
  命中同一份音频，而不是靠共享同一行。
