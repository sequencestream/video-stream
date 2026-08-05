# 合规标识（Label）

## 字段

| 字段 | 来源 | 格式 |
| --- | --- | --- |
| `content_attribute` | 固定常量 | `AI_GENERATED` |
| `service_provider_code` | 固定常量 | `sequencestream:video-stream` |
| `content_id` | 工程 + run | `{project_id}:{run_id}` |

## 注入时机

**FFmpeg mux 完成之后**，写入隐式标识（MVP 用 sidecar JSON 模拟容器 metadata），随后读回并校验 SHA-256 哈希。哈希不一致则**拒绝产出**成片。

## 不可关闭

- 配置、UI、HTTP、任务 payload **均无** skip/disable/bypass 开关
- `label.InjectAndVerify(nil, …)` 返回 `ErrBypassForbidden`

## YouTube

`internal/youtube.BuildUploadRequest` 中 `synthetic` **恒为 true**；`EncodeUploadRequest` 在序列化前再次强制为 true。
