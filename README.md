# video-stream

本地优先的 AI 视频生产流水线，**对外只有一个界面：`vs` 命令行**。

给一个标题、一段口播稿、一张背景图，一条命令产出成片：

```bash
vs video -title "好好吃饭，别那么多仇恨和敌视" -script script.txt -image bg.jpg -resolution 720p
# project id longcanting-20260814-010000
# segs       10
# duration   1m3.936s
# output     output/longcanting-20260814-010000/720p.mp4
```

这条命令背后是：按标点切句成 seg → 逐句真实合成一次测出时长 → 用测量值定预算 → 把背景图裁到目标画幅 → 渲染 → 烧录字幕 → 注入合规标识 → 解码校验。

**做成 CLI 而不是界面，是因为主要使用者是 agent。** 一条命令做一件事，`-json` 让输出可解析，退出码就是成败。仓库里没有 WebUI，也没有任何 JavaScript——一个界面会成为第二套需要同步的产品表面，而调用方并不用眼睛看。

## 架构

```
┌─────────────┐        ┌───────────────────────┐        ┌────────────────────┐
│   agent     │──exec─▶│  vs (CLI)             │──HTTP─▶│  vsd (Go 主服务)    │
└─────────────┘        │  · 一命令一件事         │        │  · HTTP API        │
                       │  · -json 可解析输出     │        │  · 进程内任务队列    │
                       └───────────────────────┘        │  · SQLite 持久化    │
                                                        └─────┬────────┬─────┘
                                          用时才取密钥          │        │ HTTP
                                    ┌───────────────────┐     │   ┌────▼─────────────┐
                                    │ 系统钥匙串/加密文件  │◀────┘   │ sidecar (Python) │
                                    └───────────────────┘         │ · 音频 / ASR      │
                                                                  │ · 剪映草稿        │
                                                                  │ · 浏览器自动化     │
                                                                  └──────────────────┘
```

- **Go 主服务**是单二进制：并发编排、任务队列、HTTP API。没有 Node 运行时，没有第二个容器，也没有需要单独构建的前端产物。
- **CLI 是唯一产品表面**，本身只是 HTTP 客户端，不直连数据库（`vs credential` 是唯一例外，它必须读写本机凭据存储）。所以一条命令和服务不可能对"什么是任务、什么是工程"有分歧。
- **Python sidecar**隔离 Python 生态。MVP 阶段它**只有契约、没有实现**——三类能力端点一律返回 `501` 加结构化原因，绝不返回伪造数据。
- 两者通过 loopback HTTP 通信，Go 侧唯一出口是 `internal/sidecar`。
- **密钥用户自持**：仓库不托管任何凭据，密钥只存在于用户自己的系统钥匙串或加密文件里，配置文件中没有存放密钥的字段。见 [`doc/arch/credentials.md`](doc/arch/credentials.md)。
- **核心数据模型**：`internal/model` 定义 seg 图与词级时间戳三层。其中 `duration_budget` 是浮动区间而非定值——增量重编译只有在这个前提下才成立。见 [`doc/arch/data-model.md`](doc/arch/data-model.md)。
- **增量重编译**：`internal/recompile` 算出一次编辑该重渲染哪些 seg，并把每次的失效率记进库。它交付的第一件东西不是加速，而是**失效率这个数字**——超过 40% 就该承认这条路走不通。见 [`doc/arch/incremental-recompile.md`](doc/arch/incremental-recompile.md)。
- **竞品雷达**：`internal/radar` 把用户导入的对标账号近 30 天公开指标校正为超预期残差，产出选题信号；不做全网爬虫。见 [`doc/arch/radar.md`](doc/arch/radar.md)。
- **脚本导入**：`internal/intake` 把标题加口播稿变成合法工程。预算是**测出来的**，不是猜的——每句先真实合成一次。见 [`doc/arch/intake.md`](doc/arch/intake.md)。

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

## inauthentic 差异化三道闸

`internal/compliance` 在渲染前强制执行三道可计算校验：结构指纹（cosine ≤0.7）、30 天复用上限（3 次）、非模板元素（用户原话/数据/独家来源）。无跳过开关。

```bash
curl -s -X POST localhost:8080/v1/compliance/check \
  -H 'Content-Type: application/json' \
  -d '{"account_id":"me","structure_card_id":"card-1","fingerprint":[0.5,0.5],"script_text":"Survey shows 42% quit","non_template_elements":[{"kind":"first_hand_data","content":"42%"}]}'
```

规则与申诉方式见 [`doc/arch/compliance.md`](doc/arch/compliance.md)。

## 视觉身份栈与 L2 样式包

`internal/visual` 把 style_ref、色板、光照、构图、品牌与场景卡编译为 `style_seed`；L2 样式包可 import/export，换包更新 `style_anchor` 并触发全量重跑。

```bash
curl -s -X POST localhost:8080/v1/visual/packs/{id}/apply \
  -H 'Content-Type: application/json' \
  -d '{"project":{...}}'   # 响应含 full_rerun_warning
```

跨厂商光线不保证像素一致，见 [`doc/arch/visual.md`](doc/arch/visual.md)。

## 混合画面生成

`internal/hybrid` 为每个 seg 选择 AI 视频 / 授权 stock / Ken Burns 静帧 / motion graphics，默认 60s 仅 hook 走 AI；每镜头 `route` + `reason` 持久化。

```bash
curl -s -X POST localhost:8080/v1/hybrid/plan \
  -H 'Content-Type: application/json' \
  -d '{"project":{...}}'   # 响应含 plans 与 ai_routes
```

路线判据与素材策略见 [`doc/arch/hybrid.md`](doc/arch/hybrid.md)。

## 脚本导入（标题 + 口播稿 → 工程）

`internal/intake` 是流水线原先缺的那一步。下游的一切都定义在 seg 图上，但没有任何东西能从
作者手里真正有的东西——一段写好的稿子——造出这张图。

```bash
vs project create -title "好好吃饭" -script script.txt
cat script.txt | vs project create -title "好好吃饭" -script -    # 也可以走 stdin
```

切句规则按顺序执行：句末标点断句 → 仍超过 48 字的在最后一个逗号处切 → 不足 8 字的并入前一句。
找不到安全切点的长句**保持原样**：太长还能看，读断了不能。

**预算是测出来的，不是猜的。** 每句先真实合成一次，拿实际时长作为 `duration_budget` 的中点。
这一步不能省：mux 阶段按预算中点裁视频轨，音轨却是 TTS 的实际输出，预算猜错不是取整误差，
而是句尾被切掉。导入产出的 seg **互不依赖**——旁白句子之间没有解析顺序，线性 `depends_on`
会让改第一句失效后面全部，正是增量重编译要避免的。

见 [`doc/arch/intake.md`](doc/arch/intake.md)。

## 背景素材

`internal/media` 把一张图裁到目标画幅并按 seg 归位：

```bash
vs project background <id> -image poster.jpg -anchor top -resolution 720p
```

渲染器读到本地素材时是「放大到覆盖画幅再裁掉溢出」，`-anchor` 决定竖直方向留哪一条。
海报类素材的标题在顶部，默认居中裁会把它切掉——这个选择放在渲染**之前**做，而不是跑完
40 秒管线后才发现标题没了。

## 渲染管线（720p / 1080p）

`internal/render` 以 FFmpeg stage 化直出 MP4；720p 预览与 1080p 出片共享 prompt/seed/ref，高清阶段无 LLM。BGM 卡点与旁链混音仅定稿后可跑，通过请求 `bgm` 参数或 `media/<project_id>/bgm.*` 提供本地音乐。Mux 后使用 FFprobe 校验 MP4 容器、目标分辨率、时长和音轨，并用 FFmpeg 完整解码音视频；校验失败的文件不会交付。

本地视觉素材可按 `media/<project_id>/<seg_id>.jpg|png|mp4|mov` 放置（目录可用 `VS_MEDIA_DIR` 修改）。图片自动应用可复现 Ken Burns，视频自动循环、缩放和裁剪到 seg 时长；未提供素材的 seg 使用确定性 motion graphics，因此默认渲染路径也会产出真实 H.264 画面。请求带 `still_images: true` 可关掉图片的 Ken Burns 保持静止——素材本身已排好版（海报、带字的图）时，缓慢推近会把边缘的字推出画面。该开关只作用于图片，且不进任何派生 hash。

字幕默认按平台走：`platform` 为 `douyin` 时烧进画面，`youtube` / `bilibili` 时封装为软字幕，也可用 `subtitle_mode` 显式指定 `burn_in` / `soft`。烧录经 FFmpeg `subtitles` 滤镜，因此需要构建时带 libass 的 FFmpeg 与一套 CJK 字体；容器镜像已包含 `font-noto-cjk`，Homebrew 的精简构建则会以 `No such filter: 'subtitles'` 失败。

音频默认由 Edge TTS 合成，使用服务端原始词边界生成时间线，并输出 48 kHz 单声道 PCM WAV。
本地运行需安装 Python `edge-tts` 包与 FFmpeg；容器镜像已包含二者。离线开发可设置
`VS_TTS_PROVIDER=stub`，但该模式不会产生可播放音频。

```bash
curl -s -X POST localhost:8080/v1/render/run \
  -H 'Content-Type: application/json' \
  -d '{"project":{...},"resolution":"720p"}'
```

见 [`doc/arch/render.md`](doc/arch/render.md)。

## 合规标识（强制）

mux 之后注入 `content_attribute` / `service_provider_code` / `content_id` 并读回校验；失败则拒绝产出。YouTube 上传 `synthetic` 恒为 true。无关闭开关。

见 [`doc/arch/label.md`](doc/arch/label.md)。

## 7 步端到端编排

`internal/wizard` 把 radar → ideation → script → hybrid → render → label 串成一条带断点续跑的
会话，通过 `/v1/wizard/sessions` 驱动。它原本是为界面的分步交互写的；界面去掉后仍然保留，
因为「哪一步失败了、从哪一步续」这件事对调用方一样有用。操作说明见
[`doc/wizard-guide.md`](doc/wizard-guide.md)。

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

出一条片子（容器内已带 FFmpeg、中文字体和 edge-tts）：

```bash
docker compose cp script.txt app:/tmp/script.txt
docker compose cp poster.jpg app:/tmp/poster.jpg
docker compose exec app vs video -title "好好吃饭" -script /tmp/script.txt -image /tmp/poster.jpg -resolution 720p
```

停止：

```bash
docker compose down
```

### 方式二：源码运行

前置：Go 1.26+、Python 3.11+、FFmpeg。没有 Node.js，也不需要。

```bash
# 1. 配置（可选，缺省值即可跑通）
cp config.example.yaml config.yaml

# 2. 启动 sidecar（终端 A）
make sidecar

# 3. 启动主服务（终端 B）
make run

# 4. 出片（终端 C）
make build
./bin/vs video -title "好好吃饭" -script script.txt -image poster.jpg -resolution 720p
```

烧录字幕需要构建时带 libass 的 FFmpeg 加一套 CJK 字体。Homebrew 的精简构建会以
`No such filter: 'subtitles'` 失败；容器镜像里两者都有。

## CLI

CLI 是这个项目对外的全部界面。

```
vs video -title T -script FILE [-image IMG]              稿子到成片，一条命令
vs project create -title T -script FILE                  导入稿子为工程（FILE 用 - 读 stdin）
vs project list|show <id>|rm <id>                        工程列表 / 详情 / 删除
vs project background <id> -image FILE [-anchor top]     背景图裁到画幅并按 seg 归位
vs render <project> [-resolution 720p|1080p] [-wait]     渲染已存工程
vs create [-type echo] [-title T] [-message M] [-wait]   提交任务
vs status <task-id>                                      查询任务
vs credential set|status|rm <provider>                   管理供应商 API key
vs version                                               版本号
```

`vs render` 与 `vs video` 还接受 `-still-images`（图片不做 Ken Burns）、`-subtitle-mode`
（`burn_in` / `soft`）、`-platform`（决定字幕默认模式与响度目标）。

任务类命令支持 `-server`（默认 `http://127.0.0.1:8080`，或环境变量 `VS_SERVER`）与 `-json`。

### 给 agent 的用法

每个命令都接受 `-json`，输出是缩进过的单个 JSON 文档；失败时错误进 stderr，退出码非零。
一条龙那条命令是给 agent 准备的，它把三步压成一次调用，中间不需要调用方保存状态：

```bash
vs video -title "$TITLE" -script /tmp/script.txt -image /tmp/bg.jpg -json \
  | jq -r .output_uri
```

需要分步控制（换背景重渲、只改一句重跑）时再用 `vs project` 那组命令。

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
| POST | `/v1/compliance/check` | 渲染前三道闸校验（无 bypass） |
| GET/POST | `/v1/visual/packs` | L2 样式包列表 / 创建 |
| POST | `/v1/visual/packs/import` | 导入样式包 JSON |
| GET | `/v1/visual/packs/{id}/export` | 导出样式包 |
| POST | `/v1/visual/packs/{id}/apply` | 应用到工程（含整段重跑提示） |
| POST | `/v1/hybrid/plan` | 混合画面路线规划并持久化 |
| GET | `/v1/hybrid/plans/{project_id}` | 读取已存的混合画面计划 |
| POST | `/v1/render/run` | 启动/续跑渲染管线 |
| GET | `/v1/render/runs/{id}` | 渲染 run 状态与 seg 产物追溯 |
| POST | `/v1/wizard/sessions` | 创建向导会话（步骤 1） |
| GET | `/v1/wizard/sessions/{id}` | 查询会话与任务状态 |
| POST | `/v1/wizard/sessions/{id}/advance` | 完成当前步并前进 / 续跑 |
| POST | `/v1/projects` | 导入标题 + 口播稿为已 seal 的工程（逐句测时长） |
| GET | `/v1/projects` | 工程列表；`?limit=` 可限制条数 |
| GET | `/v1/projects/{id}` | 读取单个工程 |
| DELETE | `/v1/projects/{id}` | 删除工程 |
| POST | `/v1/projects/{id}/background` | 背景图裁到画幅并按 seg 归位 |

根路径没有路由。服务只讲 JSON，未知路径一律 404。

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
| `render` | 已实现。按 project id 跑 staged FFmpeg 管线（720p 预览 / 1080p 出片） |
| `transcribe` | 转发到 sidecar；sidecar 当前返回 501，该错误会原样上浮 |

## 开发

```bash
make help          # 列出所有 target
make check         # go vet + go test + 明文密钥扫描
make secrets       # 只跑密钥扫描
make build         # 构建 bin/vsd 与 bin/vs
```

构建链上没有 Node，也没有需要先生成再嵌入的前端产物：`go build` 就是全部。

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
internal/compliance   inauthentic 三道闸（渲染前必经）
internal/visual       L2 视觉样式包与身份栈
internal/hybrid       混合画面路线（AI / stock / Ken Burns / motion graphics）
internal/render       FFmpeg 渲染管线（720p/1080p 共享 context）
internal/label        mux 后合规标识注入与读回校验（不可关闭）
internal/youtube      YouTube 上传适配（synthetic 恒 true）
internal/wizard       7 步端到端编排（断点续跑）
internal/intake       标题 + 口播稿 → 已 seal 的工程（预算逐句测量）
internal/media        背景图裁到画幅并按 seg 归位
internal/store       SQLite 持久化：任务、视频工程、渲染产物与重编译记录
internal/queue       进程内队列，接口预留 Temporal
internal/tasks       任务 handler
internal/sidecar     sidecar 契约与客户端
internal/httpapi     HTTP 路由
scripts/             仓库检查脚本
doc/arch/            架构决策文档
sidecar/             Python sidecar
```

## 当前不做

云端多租户、用户体系与计费、Kubernetes 部署，以及任何真实的 ASR / 剪映草稿 / 浏览器自动化。

**任何形式的图形界面**。产品表面就是 CLI；界面会变成第二套要跟着 API 一起演进的东西，而
真正的调用方是 agent，它需要的是可解析的输出和退出码。

密钥方面明确不做：云端密钥托管、团队共享凭据、MVP 阶段的浏览器 cookie 抓取。

数据模型方面明确不做：编辑标签求值（`filler` / `silence` 在纯 TTS 通路下无输入可处理）、可视化时间轴编辑器、数据库 DDL 迁移框架。理由见 [`doc/arch/data-model.md`](doc/arch/data-model.md) 的「明确不做」。

脚本导入方面明确不做：LLM 改写口播稿（`vs project create` 一字不改地照搬输入，改写是
`/v1/script/polish` 的事）、并发探测、按语义而非标点切句。理由见
[`doc/arch/intake.md`](doc/arch/intake.md) 的「明确不做」。

增量重编译执行器会直接复用计划中的未失效 seg artifact，仅为 invalidated seg 调用视频生成器。
仍明确不做：自适应阈值、边界的可配置化、产物垃圾回收。理由见
[`doc/arch/incremental-recompile.md`](doc/arch/incremental-recompile.md) 的「明确不做」。

竞品雷达方面明确不做：全网热点爬虫、全局最佳发布时间预测、评论区自动回复、评论正文持久化、MVP 内置平台 scraper。理由见 [`doc/arch/radar.md`](doc/arch/radar.md) 的「明确不做」。
