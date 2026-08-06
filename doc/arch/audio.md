# 音轨与字幕（Audio & subtitles）

## TTS 与时长预算

- 每 seg 的 TTS 时长必须在 `duration_budget_ms` 区间内。
- 允许 **±8%** 变速（`PlaybackRate`）；超出则拒绝并返回 **「需改字数」**（`ErrNeedsWordCountChange`），不做硬拉伸。
- MVP 使用 `StubTTS`；生产接入需选用 **具备商业授权** 的 TTS vendor（见下表）。

## TTS Vendor 商业授权（MVP 选型）

| Vendor | 授权 | 备注 |
|--------|------|------|
| Azure Neural TTS | 商业订阅含合成权 | 推荐默认 |
| Google Cloud TTS | 按量付费含商用 | 需项目 billing |
| ElevenLabs | Creator/Pro 及以上含商用 | 注意字符配额 |

接入时在 `config.yaml` 指定 provider；密钥走 credential chain，不落盘明文。

## 平台字幕规格

`GET /v1/audio/platforms` 返回默认表（可用 YAML 扩展）：

| Platform | LUFS | 每行字数 | 默认模式 |
|----------|------|----------|----------|
| youtube | -14 ±0.5 | 42 | soft (WebVTT) |
| douyin | -16 ±0.5 | 18 | burn_in |
| bilibili | -14 ±0.5 | 36 | soft |

软字幕输出 `subs.vtt`；烧录 fallback 输出 `subs-burned.mp4` stub（V2 接 FFmpeg drawtext）。

## HTTP

```
POST /v1/audio/synthesize   # body: project, platform, mode, voice
GET  /v1/audio/platforms
```

422 + `word_count_change` + message `需改字数` 当 TTS 超出预算。

## Render 接入

`audio` / `subtitles` / `loudness` stage 调用 `internal/audio.Engine`，产物写入 `{output_dir}/{project_id}/{run_id}/`。

Sidecar ASR / 降噪契约见 [sidecar-audio.md](./sidecar-audio.md)（V2，MVP 不实现）。
