# 密钥与平台凭据：用户自持

## 立场

密钥属于用户，不属于本项目。本仓库不托管、不代管、不中转任何模型供应商或平台凭据：密钥只存在于用户自己的机器上，由用户自己的操作系统或用户自己设的口令保护。

这条立场决定了下面所有设计：没有云端密钥服务，没有团队共享凭据，没有"帮你保管"的选项。

## 密钥去了哪里

| 后端 | 位置 | 谁在保护它 |
| --- | --- | --- |
| `env` | 进程环境变量 | 用户的 shell / 容器编排 |
| `keychain` (macOS) | 登录钥匙串，service = `video-stream` | macOS，登录口令解锁 |
| `keychain` (Linux) | Secret Service（`secret-tool`） | GNOME Keyring / KWallet |
| `keychain` (Windows) | 凭据管理器（`CredRead`/`CredWrite`） | Windows DPAPI，绑定用户账户 |
| `vault` | `<data_dir>/credentials.vault`，权限 `0600` | PBKDF2-HMAC-SHA256（600k 轮）派生密钥 + AES-256-GCM |

配置里**没有**存放密钥的字段。`config.yaml` 的 provider 条目只有 `name`、`base_url`、`model`、`protocol`；密钥在真正发请求的那一刻才从 `internal/credential` 取出。这是刻意的：`Config` 会被到处传递、还会被 `/v1/meta` 序列化，多一个明文字段就多一次泄漏机会。

### 链式回退

`credentials.backend: auto`（默认）按顺序尝试：**环境变量 → 系统钥匙串 → 加密文件**。

- 环境变量永远排第一。容器和 CI 没有钥匙串，把钥匙串放前面会让每次查询都先付一次注定失败的 IPC 开销；这个顺序也让已经 `export` 了密钥的部署无需改动。
- 某个后端"不可用"或"未解锁"会被**跳过**，而不是让整次查询失败——没有钥匙串的机器仍应能从环境变量读到密钥。
- 写入落到第一个可写的后端，因此永远不会是只读的环境变量层。

把 `backend` 显式设成 `keychain` 时，缺少钥匙串是**启动错误**而不是静默降级：用户明确要求了它，悄悄换一个更弱的存储是不可接受的。

### 环境变量名

键 `provider/openai` 会依次查找：

1. `VS_CREDENTIAL_PROVIDER_OPENAI`
2. `OPENAI_API_KEY`（供应商自己文档里的名字）

第二个是为了让已经按厂商约定 export 过的用户不必重命名。

### 为什么没有 cgo

三个平台的钥匙串都通过非 cgo 途径访问：macOS 调 `/usr/bin/security`，Linux 调 `secret-tool`，Windows 用 `golang.org/x/sys/windows` 直接 syscall `advapi32.dll`。单二进制、交叉编译、`CGO_ENABLED=0` 静态链接这些属性一个都不能丢。

> macOS 上有一个坑值得记下来：`security add-generic-password -w`（不带值）会从**控制终端**而不是 stdin 读口令，用管道喂密钥会直接挂住。实现改用 `security -i` 交互模式，密钥既不进 argv（不会出现在 `ps` 里），也不需要 TTY。

## 日常操作

```bash
vs credential set openai      # 交互输入，不回显
printf %s "$KEY" | vs credential set openai   # 管道，供脚本使用
vs credential status          # 每个 provider 的密钥来自哪个后端
vs credential rm openai       # 从所有持有它的后端删除
```

密钥**永远不作为命令行参数**传入，因此不会进 `ps` 输出，也不会进 shell 历史。

`vs credential` 是唯一不走 HTTP 的子命令。密钥不应该为了存到同一台机器上而先过一遍 socket；而且钥匙串属于登录用户，不属于守护进程。

`status` 会告诉你密钥来自哪一层。这正是"我明明设了密钥却不生效"唯一有用的诊断信息——它能区分"环境里有个过期变量盖住了钥匙串"和"根本没有密钥"。`set` 之后如果发现更高优先级的后端仍会胜出，也会当场警告。

## 轮换步骤

供应商侧签发新密钥后：

1. `vs credential set <provider>`，输入新密钥。它覆盖同一条目，不会留下旧值。
2. `vs credential status`，确认 `CREDENTIAL` 列指向你期望的后端。如果显示 `env` 而你刚写的是钥匙串，说明 shell 里还有旧的 `OPENAI_API_KEY`，先 `unset` 它。
3. 重启 `vsd`。凭据在请求时读取，但环境变量层是进程启动时的快照，改了 shell 变量必须重启才生效。
4. 到供应商控制台**吊销旧密钥**。这一步不能省：只要旧密钥没被吊销，它泄漏与否就仍然是个未知数。

疑似泄漏（密钥进了提交、进了日志、贴进了聊天窗口）时，顺序反过来：**先吊销，再换新**。改写 git 历史不算数——推送过的对象可能已经被克隆或被缓存。

### 用加密文件时

`vault` 后端需要口令。守护进程没有终端可以提问，因此只能从 `VS_VAULT_PASSPHRASE` 读；CLI 在有终端时会当场询问。

口令本身不落盘。忘记口令等于丢失文件里的全部密钥——这是"用户自持"的代价，不是缺陷。换口令目前的做法是：逐个 `vs credential set` 重新写入到一个新路径的 vault。

## 兜底：两层脱敏

`internal/redact` 是**第二道防线，不是第一道**。设计目标是任何代码路径都不会把密钥写进文件；脱敏存在是为了当这个目标被违反时，损害是有限的。因此不要因为"反正会脱敏"就放松对密钥流向的要求。

两层同时生效：

- **按字段名**：`slog` 的 `ReplaceAttr` 会把 `api_key`、`token`、`password`、`secret` 这类字段名对应的值换掉。`X-Api-Key`、`APIKey`、`api_key` 归一化后是同一个名字。
- **按值**：任何交给 `provider.Registry` 的密钥都会注册进 `redact.Registry`，此后它作为**子串**出现在任何日志、任务回执或错误信息里都会被替换成 `[REDACTED]`——包括供应商 401 响应体原样回显密钥这种情况。

任务回执（`result` 与 `error`）在写进 SQLite **之前**过一遍脱敏，因为回执会被 API 返回、被 WebUI 展示，是最容易被截图外发的东西。

`/v1/meta` 只报告 `has_credential` 与 `credential_from`，永远不回显密钥本身。

## 仓库扫描

```bash
make secrets        # 或 scripts/check-secrets.sh
```

只扫 git 跟踪的文件，只匹配形状上明确是密钥的东西（各家带前缀的 key、PEM 私钥块、赋值给 `api_key` 这类字段的非空非 `${}` 字面量）。它已经并入 `make check`。

范围刻意收窄：误报连篇的扫描器最终会被人用 `--no-verify` 绕过，而被绕过的扫描器保护不了任何东西。报告只输出文件名、行号和规则名，**不输出匹配到的内容**——把密钥打进 CI 日志会让这个检查本身变成第二次泄漏。

## 明确不做

- 云端密钥托管
- 团队共享凭据
- MVP 阶段的浏览器 cookie 抓取
