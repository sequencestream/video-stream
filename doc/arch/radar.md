# 竞品雷达：热点是校正后的残差，不是播放量

## 立场

一句话立场：**脚本生成需要外部输入，但全网爬虫的合规与稳定性风险与其价值不成比例；用户自己导入的对标账号加上自有账号后台数据，是唯一零灰区信号，且已覆盖约 90% 的价值。**

「热点」在这里不是播放量排行榜。两百万粉丝的账号拿到二十万播放，只是证明了它有两百万粉丝；两千粉丝的账号拿到同样数字，才说明帖子本身有可复制之处。所以本模块里每一个数字都是**残差**——在账号体量、发布时间、类目基线校正之后，仍然异常的那部分。绝对指标从不横向比较。

接受的代价是信号覆盖面窄于全网爬虫，且依赖用户自己会挑对标账号。我们明确不做：自建全网热点爬虫（合规与稳定性风险不值得）；全局最佳发布时间预测（样本量根本不支持，必然是噪声）；评论区自动回复（封号风险 + 直接踩 inauthentic 红线）。

## 残差计算

算法分三步，且阈值在见到任何数据之前就写死。

### 第一步：按类目拟合基线

每个类目单独拟合，绝不合并。烹饪频道的普通帖 outperform 科技频道最好的帖，共享基线会让两个类目永久失真。

视图数用 log-log 线性回归拟合：`log(1+maturedViews) = intercept + slope·log(1+followers)`。斜率是拟合出来的，不是固定为 1——固定为 1 等价于按 views/followers 排序，而播放量是次线性的，那样会把每个大号判冷、每个小号判热。

保存率与完播率用中位数 + MAD 描述离散度。MAD 而非标准差是故意的：我们要找的那个离群点，也正是会抬高标准差的那一个。

### 第二步：成熟度校正

短视频绝大部分播放集中在 48 小时内。六小时的新帖还没跑完量，不能和十天前的帖比绝对播放。

```
maturity(ageHours) = max(1 - exp(-ageHours / 48), 0.05)
maturedViews = views / maturity(ageHours)
```

### 第三步：算 z 分并取最大

```
view_z       = z(log(1+maturedViews), fitted, view_scale)
save_rate_z  = z(saveRate, category_median, save_scale)
completion_z = z(completionRate, ...)   # 仅自有账号
score        = max(view_z, save_rate_z, completion_z)
hot          = score >= 2.0
```

保存率单独成 z，不并入 view_z：少人看但多人收藏的帖，是格式信号，加权平均会把它除以三抹掉。

## 约束表

| 边界 | 触发条件 | 为什么做不了 |
| --- | --- | --- |
| 观察窗口 | 发布超过 30 天 | 老帖说明账号，不说明现在什么在跑 |
| 基线样本 | 类目内帖子 < 8 | 离散度估计本身是噪声，除出来是自信地胡说 |
| 完播率 | 非自有账号 | 平台不对第三方公开完播率 |
| 评论正文 | 任何情况 | 只存未答问句计数，不存他人评论全文 |
| 平台爬虫 | MVP 阶段 | 无官方 API 的抓取是设计刻意排除的灰区 |
| 自动回评 | 任何情况 | 平台 inauthentic 规则，风险在用户账号 |

## 指标定义

**热点判定**

```
hot ⇔ max(view_z, save_rate_z, completion_z) ≥ 2.0
```

| 条件 | 结论 |
| --- | --- |
| 类目样本 < 8 | `insufficient: true`，分数为零 |
| score ≥ 2.0 | `hot: true` |
| 否则 | 普通帖 |

**四项衍生测度**

| 测度 | 含义 | 要点 |
| --- | --- | --- |
| identity_mismatch | 作者身份错配度 [0,1] | reach/followers 与 view_z 相乘；大号常规 reach 得 0 |
| acceleration | 保存率/完播率二阶导 | 至少三次观测；间隔变化不改变符号 |
| arbitrage | 残差 / 时长（分钟） | 减速则窗口关闭；时长只是公开页能拿到的成本代理 |
| questions.density | 未答问句 / 采样评论 | 关键词检测，不求 NLP 解析 |

## 数据

```
radar_accounts(id PK, platform, handle UNIQUE(platform,handle),
               display_name, category, followers, owned,
               added_at, last_polled_at)

radar_observations(id PK, account_id FK, post_id,
                   title, duration_seconds, published_at, observed_at,
                   views, likes, comments, shares, saves,
                   completion_rate, comment_samples, unanswered_questions)
```

`radar_accounts` 上限 20 在应用层而非 DDL：这是限速预算，不是存储限制。`(platform, handle)` 唯一，同一 handle 在不同平台是两个受众。

`radar_observations` **追加**而非 upsert。同一 post_id 多次出现是设计：二阶导需要时间序列。这与 `artifacts` 表「新行替换旧行」相反——那里更新的测量更好，这里每次测量是序列上的一个点。

评论采样只落 `comment_samples` 与 `unanswered_questions`，不落原文。问句识别在 ingest 时完成，存储层只见计数。

## 接口

```
GET  /v1/radar/accounts[?platform=]
POST /v1/radar/accounts
GET  /v1/radar/signals[?platform=&category=&hot=true&limit=]
POST /v1/radar/ingest
POST /v1/radar/poll
```

信号列表在服务端算好残差与四项衍生测度再下发。基线始终用全量类目样本拟合，查询过滤只影响返回子集——否则同一帖在不同筛选条件下 score 会变，列表不可信。

引擎未接上时 GET 路由仍应答空 `items`，不说「路由不存在」。导入与 ingest 返回 503，因为空列表是诚实状态，静默丢数据不是。

平台 token 存在凭据库的 `platform/<name>` 下，配置里不放 token。轮询间隔与每平台限速见 `config.radar`。

## 明确不做

- **全网热点爬虫**。合规与维护成本与价值不成比例；用户导入是对齐合规边界的唯一办法。
- **全局最佳发布时间**。二十个账号、三十天窗口推不出可信的全局规律。
- **评论区自动回复**。inauthentic 红线，且风险在用户账号。
- **评论正文持久化**。只为一个密度指标存全文，是把话题工具变成陌生人评论档案。
- **自适应热点阈值**。2.0 和 8 样本下限写死在代码里；事后调参不是阈值。
- **MVP 内置平台 scraper**。`Source` 接口留给官方 API 或用户自备导出器；默认 `PollOnce` 对无 source 的账号计 `no_source` 而非报错。
