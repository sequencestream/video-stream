# YouTube 发布与完成通知

## OAuth 凭据

YouTube OAuth token 存储在 credential chain，键为 `platform/youtube`：

```bash
vs credential set youtube   # 写入 platform/youtube
```

本地开发可用 `stub:<video-id>` 前缀跳过 Data API 网络调用。

## 上传字段

| 字段 | 说明 |
|------|------|
| title / description / tags | 常规元数据 |
| visibility | `private` / `unlisted` / `public` |
| synthetic | **恒为 true**（`containsSyntheticMedia`） |

## 错误

| HTTP | code | 含义 |
|------|------|------|
| 422 | no_credential | 未配置 OAuth |
| 429 | quota_exceeded | Data API 配额耗尽（可读 message） |

上传失败自动重试 3 次；配额错误不重试。

## 完成通知

`config.yaml` → `notifications`:

```yaml
notifications:
  webhook_url: "https://example.com/hooks/vs"
  email_to: "you@example.com"
  smtp_host: "smtp.example.com"
  smtp_port: 587
  smtp_from: "video-stream@example.com"
  smtp_user: "smtp-user"
```

SMTP 密码走环境变量 `VS_SMTP_PASS`。

## HTTP

```
POST /v1/youtube/publish
GET  /v1/youtube/uploads/{id}
GET  /v1/delivery/download?project_id=
```

未配置发布凭据时，用户通过 **download** 取回 `{output_dir}/{project_id}/1080p.mp4`。

## Wizard

Step 7 出片后：有凭据则 publish + notify；无凭据则仅 notify（含 download 路径）。
