# Sidecar 音轨契约（V2）

MVP 主服务在进程内完成 TTS / 字幕 / LUFS。V2 将 ASR 对齐与降噪下沉到 Python sidecar。

## 端点（规划）

```
POST /v1/audio/denoise
POST /v1/audio/asr/align
```

## Denoise

**Request**

```json
{
  "uri": "file:///path/to/raw.wav",
  "profile": "speech"
}
```

**Response**

```json
{
  "uri": "file:///path/to/clean.wav",
  "snr_db": 12.5
}
```

## ASR Align

**Request**

```json
{
  "uri": "file:///path/to/clean.wav",
  "language": "zh",
  "seg_id": "hook-1"
}
```

**Response**

```json
{
  "tokens": [
    {"text": "你好", "start_ms": 0, "end_ms": 320, "confidence": 0.98}
  ],
  "source": "asr"
}
```

Token 形状与 `model.Token` 对齐；`source` 为 `asr`，供 timeline 与字幕分段消费。

## 错误

| Code | 含义 |
|------|------|
| `unsupported_format` | 非 WAV/PCM |
| `align_failed` | WhisperX 对齐失败 |

Sidecar 健康检查仍用现有 `GET /healthz`；主服务 `/readyz` 不因 sidecar 音轨能力缺失而 hard-fail。
