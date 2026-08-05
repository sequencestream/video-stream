# 视觉身份栈与 L2 样式包（Visual）

## 视觉身份栈

六层：`style_ref` + 色板 HEX + 光照预设 + 构图语法 + 品牌元素 + 场景环境卡 → 编译为 `style_seed` 注入镜头 prompt。

## L2 样式包

- import / export JSON（`schema_version: 1`）
- `RenderProfile.style_anchor = l2:<pack_id>`，参与 `render_cache_key`
- **换包 = style_anchor 边界 = 全量重跑**（见 `internal/recompile`）

## 诚实边界

**跨厂商光线不保证像素级一致**——只能接近全局风格；UI/API 在换包前返回 `full_rerun_warning`。

## 一致性量化

同包连续 5 镜头：色板 L1 距离 ≤0.15、构图 Jaccard ≥0.85（见 `coherence.go` 测试）。

## HTTP

```
GET/POST /v1/visual/packs
GET      /v1/visual/packs/{id}
GET      /v1/visual/packs/{id}/export
POST     /v1/visual/packs/import
POST     /v1/visual/packs/{id}/apply   # 含 full_rerun_warning
```
