# video-stream

本地优先的 AI 视频生产流水线。当前仓库处于**工程骨架**阶段：架构、进程边界、任务队列、配置与可观测性已经落地，具体的视频生产能力由后续意图填充。

## 架构

```
┌─────────────┐        ┌──────────────────┐        ┌────────────────────┐
│  vs (CLI)   │──HTTP─▶│  vsd (Go 主服务)  │──HTTP─▶│ sidecar (Python)   │
└─────────────┘        │  · HTTP API      │        │  · 音频 / ASR       │
┌─────────────┐        │  · 进程内任务队列  │        │  · 剪映草稿         │
│ webui (Next)│──HTTP─▶│  · SQLite 持久化  │        │  · 浏览器自动化      │
└─────────────┘        └──────────────────┘        └────────────────────┘
```

- **Go 主服务**是单二进制：并发编排、任务队列、HTTP API 都在这里。
- **Python sidecar**隔离 Python 生态。MVP 阶段它**只有契约、没有实现**——三类能力端点一律返回 `501` 加结构化原因，绝不返回伪造数据。
- 两者通过 loopback HTTP 通信，Go 侧唯一出口是 `internal/sidecar`。

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

WebUI 向导空壳：<http://localhost:3000/wizard/1>

停止：

```bash
docker compose down
```

### 方式二：源码运行

前置：Go 1.26+、Python 3.11+、Node.js 20+。

```bash
# 1. 配置（可选，缺省值即可跑通）
cp config.example.yaml config.yaml

# 2. 启动 sidecar（终端 A）
make sidecar

# 3. 启动主服务（终端 B）
make run

# 4. 提交一个 echo 假任务（终端 C）
make build
./bin/vs create -title "first task" -wait
./bin/vs status <task-id>

# 5. WebUI（终端 D）
make webui   # http://localhost:3000
```

## CLI

```
vs create [-type echo] [-title T] [-message M] [-wait]   提交任务
vs render <project> [-resolution 1080p]                  提交渲染任务（当前为占位，必然失败）
vs status <task-id>                                      查询任务
vs version                                               版本号
```

所有命令支持 `-server`（默认 `http://127.0.0.1:8080`，或环境变量 `VS_SERVER`）与 `-json`。

## HTTP API

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/healthz` | 主服务自检，不依赖 sidecar |
| GET | `/readyz` | 附带 sidecar 探测；sidecar 不可达时报 `degraded` 但仍返回 200 |
| GET | `/v1/meta` | 脱敏后的配置视图（只报告密钥是否就位，不回显密钥） |
| POST | `/v1/tasks` | 提交任务 |
| GET | `/v1/tasks` | 列出任务 |
| GET | `/v1/tasks/{id}` | 查询任务 |

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

**模型供应商的 API key 不从 YAML 读取**。供应商通过 `api_key_env` 声明环境变量名，密钥只从环境读取，因此不会被提交进仓库。

## 任务类型

| 类型 | 状态 |
| --- | --- |
| `echo` | 已实现。回显 payload，用于验证 CLI → 队列 → 存储链路 |
| `render` | 占位。直接失败并说明原因，不伪造成功 |
| `transcribe` | 转发到 sidecar；sidecar 当前返回 501，该错误会原样上浮 |

## 开发

```bash
make help     # 列出所有 target
make check    # go vet + go test
make build    # 构建 bin/vsd 与 bin/vs
```

## 目录结构

```
cmd/vsd            主服务守护进程
cmd/vs             CLI
internal/config    统一配置加载
internal/logging   结构化日志
internal/telemetry 埋点上报接口
internal/store     SQLite 任务持久化
internal/queue     进程内队列，接口预留 Temporal
internal/tasks     任务 handler
internal/sidecar   sidecar 契约与客户端
internal/httpapi   HTTP 路由
sidecar/           Python sidecar
webui/             Next.js 7 步向导空壳
```

## 当前不做

云端多租户、用户体系与计费、Kubernetes 部署，以及任何真实的 ASR / 剪映草稿 / 浏览器自动化 / 渲染逻辑。
