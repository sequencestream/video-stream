# 渲染管线（Render pipeline）

## 唯一路径

FFmpeg 直出 MP4；不解析剪映草稿回流。Stage 顺序：

1. `visuals` — 按 seg 生成画面（共享 prompt/seed/ref）
2. `audio` — 音轨
3. `subtitles` — 字幕
4. `loudness` — 响度
5. `mux` — 合成 MP4
6. `bgm_beat` — BGM 卡点（**仅定稿后**）

生产路径默认使用本机 `ffmpeg` 执行器；测试若不提供真实媒体 fixture，须显式注入 `StubFFmpeg` 和 `StubVideoGenerator`。视觉执行器优先读取 `media/<project_id>/<seg_id>` 下的本地视频或图片：视频会循环、缩放、裁剪到 segment 时长，图片会应用由 seg 内容确定的可复现 Ken Burns；没有本地素材时生成确定性的 motion graphics。所有片段统一输出 H.264/yuv420p MP4。Mux 执行器按输入顺序拼接同规格视频片段，选取最后生成的音轨，将 WebVTT/SRT 作为 `mov_text` 软字幕封装，并通过同目录临时文件原子发布最终 MP4。运行镜像内置 FFmpeg。

## 720p / 1080p 共享缓存

- 720p 预览写入 `render_shared_context`（prompt + seed + ref）
- 1080p 出片读取同一 context，**只重发视频模型 API**，不调用 LLM
- 与 `render_cache_key` 对齐；产物写入 `artifacts` 表

## 续跑

`render_runs.last_completed_stage` 记录进度；`resume_from` 从失败 stage 继续。

## HTTP

```
POST /v1/render/run
GET  /v1/render/runs/{id}
```

任务类型 `render` 通过 project id + resolution 触发同一引擎。
