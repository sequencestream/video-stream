# 音轨与字幕（Audio & subtitles）

## TTS 与时长预算

- 每 seg 的 TTS 时长必须在 `duration_budget_ms` 区间内。
- 允许 **±8%** 变速（`PlaybackRate`）；超出则拒绝并返回 **「需改字数」**（`ErrNeedsWordCountChange`），不做硬拉伸。
- 默认使用 `EdgeTTS`：显式请求服务端 `WordBoundary`，并将压缩音频转换为 48 kHz、单声道、
  signed 16-bit PCM WAV。音频与词级时间戳使用同一个、最多 ±8% 的 playback rate；超出预算仍拒绝。
- `StubTTS` 仅可通过 `audio.provider: stub` 显式启用，供离线开发和单元测试使用。
- Edge provider 依赖 Python `edge-tts` 包和 FFmpeg。它适合本地验证；商业发布仍须确认服务条款，
  或替换为下表中具备明确商业授权的 vendor。

## TTS Vendor 商业授权（MVP 选型）

| Vendor | 授权 | 备注 |
|--------|------|------|
| Azure Neural TTS | 商业订阅含合成权 | 推荐默认 |
| Google Cloud TTS | 按量付费含商用 | 需项目 billing |
| ElevenLabs | Creator/Pro 及以上含商用 | 注意字符配额 |

TTS provider 在 `config.yaml` 的 `audio` 段指定。需要密钥的商业 provider 接入时仍须走
credential chain，不落盘明文。

## 平台字幕规格

`GET /v1/audio/platforms` 返回默认表（可用 YAML 扩展）：

| Platform | LUFS | 每行字数 | 默认模式 |
|----------|------|----------|----------|
| youtube | -14 ±0.5 | 42 | soft (WebVTT) |
| douyin | -16 ±0.5 | 18 | burn_in |
| bilibili | -14 ±0.5 | 36 | soft |

字幕阶段统一输出有效的 `subs.vtt`。`soft` 模式在最终 MP4 中封装为 `mov_text` 字幕轨；
`burn_in` 模式使用 FFmpeg `subtitles`/libass 滤镜烧入最终画面，不再保留独立字幕轨。
运行环境必须提供 libass 字幕滤镜和覆盖目标语言的字体；官方镜像安装 Noto CJK 字体。

## HTTP

```
POST /v1/audio/synthesize   # body: project, platform, mode, voice
GET  /v1/audio/platforms
```

422 + `word_count_change` + message `需改字数` 当 TTS 超出预算。

## Render 接入

`audio` / `subtitles` / `loudness` stage 调用 `internal/audio.Engine`，产物写入 `{output_dir}/{project_id}/{run_id}/`。
渲染请求可传 `platform` 和 `subtitle_mode`；未传模式时采用平台默认值。相同 `run_id` 不允许
更换平台或字幕模式后续跑，防止错误复用旧产物。

Sidecar ASR / 降噪契约见 [sidecar-audio.md](./sidecar-audio.md)（V2，MVP 不实现）。
