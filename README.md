# video-stream

本地优先的 AI 视频生产流水线。当前仓库处于**工程骨架 + 核心数据模型 + 增量重编译引擎**阶段：架构、进程边界、任务队列、配置、可观测性，描述视频本身的那套数据结构，以及"改一句话该重渲染哪些片段"的算法与失效率埋点都已落地；真正的渲染执行由后续意图填充。

## 架构

```
┌─────────────┐        ┌───────────────────────┐        ┌────────────────────┐
│  vs (CLI)   │──HTTP─▶│  vsd (Go 主服务)       │──HTTP─▶│ sidecar (Python)   │
└─────────────┘        │  · HTTP API           │        │  · 音频 / ASR       │
┌─────────────┐        │  · 内嵌 WebUI 静态导出  │        │  · 剪映草稿         │
│   浏览器     │──HTTP─▶│  · 进程内任务队列       │        │  · 浏览器自动化      │
└─────────────┘        │  · SQLite 持久化       │        └────────────────────┘
                       └───────────┬───────────┘
                                   │ 用时才取密钥
                       ┌───────────▼───────────┐
                       │  系统钥匙串 / 加密文件   │
                       └───────────────────────┘
```

- **Go 主服务**是单二进制：并发编排、任务队列、HTTP API，连 WebUI 都编译进去了。生产环境没有 Node 运行时，也没有第二个容器，界面与它对话的 API 不可能版本错配。
- **Python sidecar**隔离 Python 生态。MVP 阶段它**只有契约、没有实现**——三类能力端点一律返回 `501` 加结构化原因，绝不返回伪造数据。
- 两者通过 loopback HTTP 通信，Go 侧唯一出口是 `internal/sidecar`。
- **密钥用户自持**：仓库不托管任何凭据，密钥只存在于用户自己的系统钥匙串或加密文件里，配置文件中没有存放密钥的字段。见 [`doc/arch/credentials.md`](doc/arch/credentials.md)。
- **核心数据模型**：`internal/model` 定义 seg 图与词级时间戳三层。其中 `duration_budget` 是浮动区间而非定值——增量重编译只有在这个前提下才成立。见 [`doc/arch/data-model.md`](doc/arch/data-model.md)。
- **增量重编译**：`internal/recompile` 算出一次编辑该重渲染哪些 seg，并把每次的失效率记进库。它交付的第一件东西不是加速，而是**失效率这个数字**——超过 40% 就该承认这条路走不通。见 [`doc/arch/incremental-recompile.md`](doc/arch/incremental-recompile.md)。
- **竞品雷达**：`internal/radar` 把用户导入的对标账号近 30 天公开指标校正为超预期残差，产出选题信号；不做全网爬虫。见 [`doc/arch/radar.md`](doc/arch/radar.md)。

## 核心数据模型

```
Project ──┬─ Seg[]      seg 图：说什么、允许多长、依赖谁、产物能否复用（depends_on 构成 DAG）
          └─ Timeline   Event → Utterance → Token 三层词级时间戳，由 TTS 对齐产出
```

`duration_budget_ms` 是 `[min, max]` 闭区间，**定值会被拒绝**。原因是渲染缓存的命中条件为
「`render_cache_key` 相同 **且** 缓存产物的实际时长落在预算区间内」；预算若是定值，第二个
条件就要求 TTS 每次合成到毫秒一致，于是改一个字就得全片重渲染。区间半宽被限制在 ±2%~±8%：
上限是音质红线，下限是增量重编译的存活条件。

`content_hash` 标识「说了什么」，`render_cache_key` 标识「产物能否复用」，两者都带版本前缀、
逐字段长度前缀编码，且**都不覆盖 `duration_budget`**。完整字段语义、每个字段在 MVP 阶段
是否有消费者、hash 的稳定性保证，见 [`doc/arch/data-model.md`](doc/arch/data-model.md)。

## 增量重编译

改一句话，只重渲染受影响的片段——这是本项目最大的技术赌注。`internal/recompile` 交付的
不是加速，而是**判断这个赌注是否成立所需的那个数字**：

```
invalidation_rate = Σ invalidated_segs / Σ total_segs    按 seg 加权，不按 run 平均
```

引擎对比前后两份 project，沿 `depends_on` 反向边求失效闭包，再逐个 seg 用
`Seg.CanReuse` 查缓存。另有**六条边界**——视觉基调变更、总时长漂移 >15%、情绪节拍重排、
开场改动、连续动作组被切开、多镜生成批次被切开——增量地做在技术上可以硬算，但成片是错的，
遇到就诚实地整片重来并记下是哪一条。

阈值写死在看到任何数据之前：失效率 > **40%** 判 `scrap`，不足 **20** 次记录判
`insufficient_data`（不默认成"可行"）。查看当前数字：

```bash
curl -s localhost:8080/v1/recompile/report | jq
```

失效传播算法、六条边界的判定顺序、为什么按 seg 加权，见
[`doc/arch/incremental-recompile.md`](doc/arch/incremental-recompile.md)。

## 竞品雷达

热点是脚本生成的输入源，但全网爬虫的合规与稳定性风险与其价值不成比例。`internal/radar` 的赌注是：**用户自己导入 5–20 个对标账号，加上自有账号后台数据，是唯一零灰区信号，且已覆盖约 90% 的价值。**

「热」不是播放量，是校正后的超预期残差：

```
score = max(view_z, save_rate_z, completion_z)
hot   ⇔ score ≥ 2.0
```

引擎按类目拟合 log-log 基线，用 48 小时成熟度曲线校正帖子年龄，再算 z 分。四项衍生测度——身份错配度、保存率/完播率二阶导、创作成本套利窗口、评论区未答问句密度——各自独立实现；评论正文不持久化，只存计数。

```bash
# 导入对标账号
curl -s -X POST localhost:8080/v1/radar/accounts \
  -H 'Content-Type: application/json' \
  -d '{"platform":"douyin","handle":"cook_daily","category":"cooking","followers":12000}'

# 查看热点信号（也可 POST /v1/radar/ingest 写入观测数据）
curl -s 'localhost:8080/v1/radar/signals?hot=true' | jq
```

数据来源、合规边界、阈值为何写死，见 [`doc/arch/radar.md`](doc/arch/radar.md)。

## 结构卡片与跨类目选题

雷达给出「什么在爆」，`internal/ideation` 回答「怎么把结构迁到你的类目」。结构卡片只存形式六维（hook、前 3 秒视觉、节拍、信息密度、情绪动线、争议锚点），**绝不迁事实**；图边记录卡片关系，向量只做召回候选。

```bash
# 从爆款元数据提取结构卡
curl -s -X POST localhost:8080/v1/ideation/extract \
  -H 'Content-Type: application/json' \
  -d '{"post_id":"p1","category":"cooking","title":"Why does sourdough crack?","duration_seconds":45}'

# 跨类目迁移 → 3–5 张选题卡
curl -s -X POST localhost:8080/v1/ideation/migrate \
  -H 'Content-Type: application/json' \
  -d '{"structure_card_id":"<id>","user_theme":"home fitness","target_category":"fitness"}'
```

六维断言、图查询与向量召回回归，见 [`doc/arch/ideation.md`](doc/arch/ideation.md)。

## 多 Agent 脚本打磨

`internal/scriptagents` 运行 Writer×3、Audience-Simulator、Judge + 确定性 Skill 闭环；Critic 只诊不治，定稿为合法 seg 结构。

```bash
curl -s -X POST localhost:8080/v1/script/polish \
  -H 'Content-Type: application/json' \
  -d '{"topic":"home fitness","spike":"nobody talks about this","project_id":"demo-1"}'
```

终止阈值与验收对照见 [`doc/arch/script-agents.md`](doc/arch/script-agents.md)。

## 快速开始

### 方式一：Docker Compose（推荐）

前置：Docker 与 Docker Compose。

```bash
git clone git@github.com:sequencestream/video-stream.git
cd video-stream
docker compose up --build -d
```

> 如果构建机访问不了 `proxy.golang.org`，用 `GOPROXY` 指向可达的镜像：
> `GOPROXY=https://goproxy.cn,direct docker compose up --build -d`

等待健康检查转绿（约 30 秒）：

```bash
docker compose ps
```

验证双向探测：

```bash
# 主服务自检
curl -s localhost:8080/healthz

# 主服务 -> sidecar
curl -s localhost:8080/readyz

# sidecar -> 主服务
curl -s localhost:8090/health/upstream
```

跑一个 echo 假任务，拿到任务回执：

```bash
docker compose exec app vs create -title "first task" -wait
```

WebUI 向导空壳与 API 同端口：<http://localhost:8080/wizard/1>

停止：

```bash
docker compose down
```

### 方式二：源码运行

前置：Go 1.26+、Python 3.11+；只有构建 WebUI 时才需要 Node.js 20+。

```bash
# 1. 配置（可选，缺省值即可跑通）
cp config.example.yaml config.yaml

# 2. 构建 WebUI 静态导出（可选；不做的话 vsd 的 / 会给出构建提示页，API 照常工作）
make webui-build

# 3. 启动 sidecar（终端 A）
make sidecar

# 4. 启动主服务（终端 B），WebUI 在同一端口
make run     # http://localhost:8080/wizard/1

# 5. 提交一个 echo 假任务（终端 C）
make build
./bin/vs create -title "first task" -wait
./bin/vs status <task-id>
```

改 WebUI 代码时用 Next 的开发服务器，热更新比每次重新嵌入快得多：

```bash
make webui   # http://localhost:3000
```

## CLI

```
vs create [-type echo] [-title T] [-message M] [-wait]   提交任务
vs render <project> [-resolution 1080p]                  提交渲染任务（当前为占位，必然失败）
vs status <task-id>                                      查询任务
vs credential set|status|rm <provider>                   管理供应商 API key
vs version                                               版本号
```

任务类命令支持 `-server`（默认 `http://127.0.0.1:8080`，或环境变量 `VS_SERVER`）与 `-json`。

`vs credential` 是例外：它直接读写本机凭据存储，不走 HTTP。密钥从 stdin 读入、终端下不回显，**永远不作为命令行参数**，因此不会出现在 `ps` 输出或 shell 历史里。

```bash
vs credential set openai                       # 交互输入
printf %s "$KEY" | vs credential set openai    # 管道，供脚本使用
vs credential status                           # 每个 provider 的密钥来自哪个后端
```

## HTTP API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 主服务自检，不依赖 sidecar |
| GET | `/readyz` | 附带 sidecar 探测；sidecar 不可达时报 `degraded` 但仍返回 200 |
| GET | `/v1/meta` | 脱敏后的配置视图（只报告密钥是否就位，不回显密钥） |
| POST | `/v1/tasks` | 提交任务 |
| GET | `/v1/tasks` | 列出任务 |
| GET | `/v1/tasks/{id}` | 查询任务 |
| GET | `/v1/recompile/report` | 增量重编译失效率报告；`?project=<id>` 可限定单个工程 |
| GET | `/v1/radar/accounts` | 竞品雷达：已导入的对标账号列表；`?platform=` 可筛选 |
| POST | `/v1/radar/accounts` | 导入一个对标账号（上限 20） |
| GET | `/v1/radar/signals` | 热点信号列表；`?hot=true` 仅返回超阈值帖 |
| POST | `/v1/radar/ingest` | 写入一批公开指标观测（评论正文不落库） |
| POST | `/v1/radar/poll` | 触发一轮轮询（无注册 source 时计 no_source） |
| POST | `/v1/ideation/extract` | 提取结构卡片（六维、去领域化） |
| GET | `/v1/ideation/cards` | 结构卡片列表；`?category=` 可筛选 |
| GET | `/v1/ideation/cards/{id}` | 单张结构卡 + 图邻居 |
| POST | `/v1/ideation/migrate` | 跨类目迁移，产出 3–5 选题卡 |
| GET | `/v1/ideation/topics` | 选题卡列表；`?card_id=` 可筛选 |
| POST | `/v1/ideation/recall` | 向量召回 top-k 结构卡 |
| POST | `/v1/script/polish` | 多 Agent 脚本打磨 → 合法 seg 工程 |
| GET | `/` 及其他 | 内嵌的 WebUI 静态资源；未构建 WebUI 时返回 503 与构建指引 |

sidecar（默认 `:8090`）：

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/health` | sidecar 自检 |
| GET | `/health/upstream` | 反向探测主服务 |
| POST | `/v1/audio/transcribe` | 占位，返回 501 |
| POST | `/v1/jianying/draft` | 占位，返回 501 |
| POST | `/v1/browser/automate` | 占位，返回 501 |

## 配置

优先级：内置默认值 < `config.yaml` < `VS_` 前缀环境变量。完整字段与对应的环境变量名见 [`config.example.yaml`](config.example.yaml)。

**配置里没有存放 API key 的字段**。provider 条目只有 `name`、`base_url`、`model`、`protocol`，密钥在真正发请求那一刻才从凭据存储取出。`credentials.backend` 默认 `auto`，按 **环境变量 → 系统钥匙串 → 加密文件** 依次查找。完整说明与轮换步骤见 [`doc/arch/credentials.md`](doc/arch/credentials.md)。

## 任务类型

| 类型 | 状态 |
| --- | --- |
| `echo` | 已实现。回显 payload，用于验证 CLI → 队列 → 存储链路 |
| `chat` | 已实现。用 [`vogo/aimodel`](https://github.com/vogo/aimodel) 调用配置好的 provider，需要先 `vs credential set` |
| `render` | 占位。直接失败并说明原因，不伪造成功 |
| `transcribe` | 转发到 sidecar；sidecar 当前返回 501，该错误会原样上浮 |

## 开发

```bash
make help          # 列出所有 target
make check         # go vet + go test + 明文密钥扫描
make secrets       # 只跑密钥扫描
make build         # 构建 bin/vsd 与 bin/vs
make webui-build   # 构建 WebUI 静态导出到 internal/webui/dist
```

`internal/webui/dist` 是生成产物，不进版本库；仓库里只保留一个 `.gitkeep`，好让 `//go:embed` 在全新 clone 上也能编译。因此**没装 Node 也能 `go build`**，只是二进制里不带界面。

## 目录结构

```
cmd/vsd              主服务守护进程
cmd/vs               CLI
internal/config      统一配置加载（不含任何密钥字段）
internal/credential  凭据存储：环境变量 / 三平台钥匙串 / 加密文件
internal/redact      日志与回执的两层脱敏
internal/provider    模型供应商调用，密钥用时才取
internal/logging     结构化日志
internal/telemetry   埋点上报接口
internal/model       核心数据模型：seg 图、词级时间戳、派生 hash、schema 迁移
internal/recompile   增量重编译：失效传播、六条边界、失效率报告
internal/radar       竞品雷达：残差热点、四项衍生测度、限速轮询
internal/ideation    结构卡片提取、图存储、向量召回、跨类目选题
internal/scriptagents 多 Agent 脚本打磨闭环
internal/store       SQLite 持久化：任务、视频工程、渲染产物与重编译记录
internal/queue       进程内队列，接口预留 Temporal
internal/tasks       任务 handler
internal/sidecar     sidecar 契约与客户端
internal/httpapi     HTTP 路由
internal/webui       内嵌的 WebUI 静态导出
scripts/             仓库检查脚本
doc/arch/            架构决策文档
sidecar/             Python sidecar
webui/               Next.js 7 步向导空壳（源码）
```

## 当前不做

云端多租户、用户体系与计费、Kubernetes 部署，以及任何真实的 ASR / 剪映草稿 / 浏览器自动化 / 渲染逻辑。

密钥方面明确不做：云端密钥托管、团队共享凭据、MVP 阶段的浏览器 cookie 抓取。

数据模型方面明确不做：编辑标签求值（`filler` / `silence` 在纯 TTS 通路下无输入可处理）、可视化时间轴编辑器、`/v1/projects` HTTP 入口、数据库 DDL 迁移框架。理由见 [`doc/arch/data-model.md`](doc/arch/data-model.md) 的「明确不做」。

增量重编译方面明确不做：真的去执行渲染（引擎只产出计划）、自适应阈值、边界的可配置化、产物垃圾回收。理由见 [`doc/arch/incremental-recompile.md`](doc/arch/incremental-recompile.md) 的「明确不做」。

竞品雷达方面明确不做：全网热点爬虫、全局最佳发布时间预测、评论区自动回复、评论正文持久化、MVP 内置平台 scraper。理由见 [`doc/arch/radar.md`](doc/arch/radar.md) 的「明确不做」。
