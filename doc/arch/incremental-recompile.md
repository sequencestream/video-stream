# 增量重编译：先能测量，再谈优化

## 立场

一句话立场：**增量重编译是这个项目最大的技术赌注，所以它交付的第一件东西不是加速，而是失效率这个数字。**

赌注是这样的：用户改一句话，系统只重渲染受影响的片段，而不是整片重来。如果这个假设成立，
成本和等待时间降一个数量级；如果不成立，seg 图、区间预算、两个 hash、六条边界——所有为它
付出的复杂度都在为一笔不存在的收益买单。

问题在于「成立」与「不成立」之间没有先验答案。真实的编辑行为里有多少比例会波及全片，
只能测。所以本模块的交付顺序是反直觉的：

1. 先定义**什么算失效**（失效传播算法与六条边界）；
2. 先写死**多少算失败**（40% 失效率，在看到任何数据之前）；
3. 再把每次重编译的结果**记进库**，让这个数字随真实使用累积；
4. 最后开一个只读端点把它暴露出来。

**阈值必须写在看到数据之前。** 事后挑一个刚好高于实测值的阈值不是阈值，是自我安慰。

## 失效传播

一次重编译拿到两份 project：`previous`（上次编译的样子）和 `next`（这次要编译的样子）。
算法分三步。

### 第一步：算出改了什么

按 `seg_id` 对齐两份 seg 列表，分出两类改动：

| 类别 | 判据 | 语义 |
| --- | --- | --- |
| `content` | `render_cache_key` 变了 | 这个 seg 说的话/画面/管线变了 |
| `rewired` | `depends_on` 变了 | 这个 seg 在图里的位置变了 |

新增的 seg 两类都算，删除的 seg 只影响依赖它的那些。

**两类必须分开，不能并成一个集合**，这是本模块修掉的一个真实缺陷。理由见下一步。

### 第二步：沿反向依赖边求闭包

以 `content ∪ rewired` 为种子，沿 `depends_on` 的**反向边**求传递闭包，得到「下游」集合。
求闭包时**把种子本身排除在结果之外**——一个 `content` 改动过的 seg 仍然有资格命中缓存：
它的新 `render_cache_key` 可能对应着另一个项目里已经渲染过的产物，那正是跨项目复用的价值。

但 `rewired` 的 seg 不一样。它的 `render_cache_key` 可能一个字节都没变（`depends_on`
不进 key），于是缓存必然返回一份产物——一份按旧上下文渲染的产物。所以：

```
invalidated = dependentsOf(content ∪ rewired)  ∪  rewired
```

`rewired` 要单独并回来。早期版本把两类混成一个集合再整体排除，结果是改接线的 seg
自己永远不会失效，而缓存会兴高采烈地把旧产物还给它。这类 bug 的外在表现是
「我调整了顺序但成片没变」——和陈旧 `render_cache_key` 一样，是这套系统里最难查的一类。

### 第三步：逐个 seg 查缓存

对不在 `invalidated` 里的 seg，用 `Seg.CanReuse(cachedKey, cachedDurationMS)` 做两道门检查：
key 相同**且**产物实际时长落在本次预算区间内。查不到产物、或时长顶出区间，就失效。

命中一个 seg 就把它的 `cost_micros` 计入 `cost_saved_micros`。这个数字是省下的钱，
不是花掉的钱；规划阶段只读取历史 artifact 成本，不猜测本次执行的真实开销。

## 交给渲染执行器

向导在预览编辑时生成的 `recompile.Plan` 作为 `render.RunRequest.RecompilePlan` 原样传入
渲染执行器。执行器在启动任何 stage 前验证计划与当前工程匹配：project id 必须一致，
`invalidated` 与 `reused` 必须无重复地完整覆盖所有 seg；全量重跑还必须点名边界、失效全部
seg 且不能声明复用。缺失、未知或重复 seg 的计划会被拒绝，不能静默少渲染。

执行结果回传已接收的计划，向导据此发布失效数和总 seg 数。这让集成测试覆盖的是
“规划器 → 执行器 → 向导状态”的真实交接，而不是只验证规划器在旁路算出了一个数字。

`visuals` stage 严格执行这份分区：`reused` seg 直接从 `artifacts` 读取 URI，并把同一 URI
写入本次 run 的 `render_seg_artifacts` 追溯记录；只有 `invalidated` seg 会调用视频生成器，
生成成功后刷新全局 artifact。最终 mux 仍按当前工程的 seg 顺序接收所有 URI，因此同一 artifact
可以在时间线上出现多次，不能按文件路径去重。

计划声明复用、但 artifact 行或对应文件已经不存在时，执行器明确失败，不会临时把该 seg 改成
重生成。否则实际失效数会与已经持久化的计划不一致，失效率和节省成本都会失真。需要重试时应
重新规划，让缺失缓存自然进入 `invalidated`。

## 六条边界：诚实地承认做不到

有六种改动，增量地做在技术上可以硬算，但算出来的成片是错的。遇到它们，引擎**放弃增量、
整片重来**，并在记录里点名是哪一条。

| 边界 | 触发条件 | 为什么增量做不了 |
| --- | --- | --- |
| `style_anchor` | `render_profile.style_anchor` 变了 | 视觉基调换了，新旧片段并排会明显不是一套东西 |
| `duration_drift` | 总时长漂移超过 15% | 配乐、转场节奏全部要重新对齐 |
| `beat_reordered` | 沿渲染序的 `emotion_tag` 序列变了 | 情绪节拍是跨 seg 的整体结构，不是单点属性 |
| `hook_edited` | 渲染序第一个 seg 变了 | 开头决定整片的建立方式，后面每一段都是在它的前提上 |
| `continuity_broken` | `continuity_group` 内有 seg 改动或分组变化 | 同一个连续动作被切开渲染，接缝肉眼可见 |
| `batch_broken` | `generation_batch` 内有 seg 改动或分组变化 | 一次多镜生成调用的产物之间有隐含一致性，单独重生成会脱节 |

判定顺序是固定的：**先全局后局部**（style → drift → beat → hook → continuity → batch）。
一次改动可能同时越过好几条，报告需要一个稳定的归因，而全局性的原因比局部的更能解释问题。
顺序若依赖 map 遍历，同一次改动在两台机器上会归因到不同边界，报告立刻失去意义。

时长漂移用整数比较（`drift * 100 <= 15 * before`），和预算校验同一个理由：
在阈值上因浮点舍入而翻转的判定，没人能复现。

`continuity_group` 与 `generation_batch` **不进任何 hash**。它们描述的是「哪些 seg 必须
一起处理」，不是「这个 seg 长什么样」。放进 `render_cache_key` 会让重新分组打掉所有产物，
而重新分组恰恰是要用边界机制来处理的事。

## 失效率报告

```
invalidation_rate = Σ invalidated_segs / Σ total_segs      （跨所有记录的 run）
```

**按 seg 加权，不是各 run 失效率的平均。** 按 run 平均会让一次三个 seg 的微调和一次
两百个 seg 的全片重来权重相同，于是一串小编辑就能把那次昂贵的掩盖掉——而恰恰是那次昂贵的
决定了这件事划不划算。

全片重来按全额 seg 数计入，没有「用户本来就要求了个大改动」的折扣。这个数字要说的是
重编译在实践中的真实代价，不是为设计辩护。

判定规则：

| 条件 | 结论 |
| --- | --- |
| 记录的 run < 20 | `insufficient_data` |
| 失效率 > 40% | `scrap` |
| 否则 | `viable` |

`insufficient_data` 是一个独立取值，不是默认成 `viable`。否则第一天的空数据会被读成
「运转良好」。20 这个下限是判断，不是统计结论：大约是一下午的真实编辑量，是最小的值得
争论的样本。第一次编辑碰巧越过边界就报 100% 失效率并据此宣判，不该发生。

run 记录**落库**而不是内存计数。这个问题需要几周的真实编辑才能回答，内存计数每次重启清零，
永远攒不出证据。

## 数据

```
artifacts(render_cache_key PK, duration_ms, uri, cost_micros, created_at)
recompile_runs(id PK, project_id, planned_at, total_segs, invalidated_segs,
               full_rerun, boundary, cost_saved_micros)
```

`artifacts` 以 `render_cache_key` 为主键而不是 seg id——两个 seg 共用一份产物正是把
`seg_id` 排除出 key 的全部目的。同一个 key 重复写入是**替换**而不是拒绝：同样的内容走同样的
管线，新一行是对「它要花多少钱、实际多长」更好的测量。

`duration_ms` 必须为正。零或负数会通过每一道从零开始的预算检查，等于给一份坏产物发放复用许可。

`recompile_runs` 只记 `cost_saved_micros`，不记「花掉的成本」——渲染器还不存在，
写一个花费字段就是写一个恒为零的字段。

## 接口

```
GET /v1/recompile/report[?project=<id>]
```

只读，只有这一个。速率和判定都在服务端算好再连同原始计数一起下发：客户端自己做除法，
迟早有人按 run 平均而不是按 seg 加权，然后悄悄报出一个比阈值定义友好的数字。

引擎没接上时（`Deps.Recompile == nil`）路由仍然应答，返回空计数 + `insufficient_data`。
计数归零而判定默认成 `viable` 会说「赌注正在兑现」，而实际上什么都没测过。

## schema v2

v2 加了三个字段（seg 的 `continuity_group`、`generation_batch`，render profile 的
`style_anchor`），并把 `render_cache_key` 前缀从 `rk1:` 抬到 `rk2:`——因为 `style_anchor`
进了 key。

前缀一动，所有 v1 文档的 `render_cache_key` 都对不上重算结果。放着不管，这类文档读出来
一切正常，然后在下一次保存时报 `ErrStaleDerived`，把 schema 升级造成的问题算在调用方头上。
所以 `stepV1ToV2` 做的事只有一件：**整份重新 Seal**。

这一步是通过 `Project` 结构体往返做的，而 `Step` 的文档明确警告过这个做法：目标版本删掉的
字段在反序列化那一刻就没了。这里安全只因为一个具体理由——**v2 是 v1 的纯超集，没有删任何
字段**。以后任何删字段的迁移步骤都不能照抄这个写法。

`style_anchor` 必须进 `render_cache_key`，这是跨项目复用的正确性要求：`SegsByRenderCacheKey`
会把另一个项目里 key 相同的 seg 当作可复用来源，如果视觉基调不在 key 里，两套基调完全不同的
项目会互相复用对方的产物。

## 明确不做

- **自动调阈值**。40% 和 20 是写死的常量。能自适应的阈值等于没有阈值。
- **失效率之外的成本模型**。当前尚未持久化本次执行的真实开销，`cost_saved_micros` 完全来自
  `artifacts` 里记录的历史值。
- **边界的可配置化**。六条边界现在是代码里的六个函数。在有数据说明哪条画得太宽之前
  就做成配置项，是把「我们不知道」包装成「你自己选」。
- **产物的垃圾回收**。`artifacts` 只增不减。第一次撑爆磁盘之前，任何回收策略都是猜的。
