# vs

视频剪辑命令行工具。把常见的剪辑需求包装成一条命令。

```bash
vs transcribe talk.mp4        # 提取语音文字，带词级时间戳
vs filler talk.mp4            # 减除口水话：嗯、呃、结巴、长停顿
vs subtitle talk.mp4          # 生成并烧录字幕
```

**一次执行 = 一个原子操作。** 没有守护进程，没有工程文件，没有跨调用的状态。一条命令处理一批文件然后退出；两个操作的组合方式，就是后一条命令读前一条写出来的文件。

**ffmpeg 已经能做的，一律直接交给 ffmpeg。** vs 提供的价值只在那些真正难记的部分：「保留这 180 段、丢掉其余」该写成什么样的 filter graph，字幕路径要怎么转义 libass 才肯读，`-ss` 放在 `-i` 前面和后面到底哪个是 seek 而不是解码。

## 安装

```bash
make install                  # 装到 /usr/local/bin/vs
vs doctor                     # 检查依赖，缺什么直接告诉你怎么装
```

依赖两样东西：

| 依赖 | 谁需要 | 安装 |
|---|---|---|
| **ffmpeg / ffprobe** | 所有命令 | `brew install ffmpeg` / `apt install ffmpeg` |
| **faster-whisper** | 只有语音识别相关的命令 | `python3 -m pip install faster-whisper` |

烧录字幕要求 ffmpeg 编译时带了 **libass**。最小化的构建（不少服务器版和部分 homebrew 构建）没有 `subtitles` 滤镜，`vs doctor` 会单独报这一项——`-mode soft` 不需要 libass，任何构建都能用。

## 命令

```
提取与字幕
  transcribe  提取语音文字，输出词级时间戳（别名 asr）
  subtitle    加字幕：烧录进画面 / 嵌入为可选轨道 / 只出 .srt（别名 sub）

剪切
  filler      减除口水话：嗯呃、结巴、超长停顿
  silence     压缩或删除静音段
  cut         按时间区间保留或删除，拼接剩下的（别名 trim）

变换
  resize      改分辨率 / 画幅（别名 scale）
  speed       变速，音画同步、音高不变
  concat      按顺序拼接多个文件（别名 join）
  audio       抽出音轨

查看
  probe       看文件里到底是什么（别名 info）

配置
  doctor      检查外部依赖
  credential  管理 API key（别名 cred）
```

`vs --help` 看全部，`vs <命令> --help` 看某条命令的全部参数和示例。参数可以写在文件名后面——`vs filler talk.mp4 -aggressive` 和 `vs filler -aggressive talk.mp4` 等价。

## 三条主线

### 提取语音文字

```bash
vs transcribe talk.mp4                        # 写出 talk.json
vs transcribe -format srt -o talk.srt talk.mp4
vs transcribe -lang zh -model large-v3 talk.mp4
vs transcribe -prompt '沈括, 梦溪笔谈' lecture.mp4   # 给模型喂专有名词
```

默认输出是 vs 自己的 JSON，值得留着：**它是唯一带词级时间戳的格式**，而后面每条命令都要靠它。`vs subtitle` 和 `vs filler` 会自动捡起放在输入文件旁边的同名 `.json`，所以先转写一次，之后无论重剪多少遍、改多少版字幕样式，慢的那一步只跑一次。

首次使用某个模型会下载（需要联网），之后完全离线。

### 减除口水话

```bash
vs filler -list talk.mp4        # 只打印要剪什么，不动文件
vs filler talk.mp4              # 写出 talk.clean.mp4
vs filler -aggressive talk.mp4  # 连语气词一起剪
```

剪三类东西：

- **语气词** — 嗯 呃 um uh，只有内置的这一小份词表
- **结巴** — 半秒内重复的同一个词，删前一个留后一个（说完的是后一个）
- **长停顿** — 超过 `-max-pause` 的静音，**压缩到该长度而不是完全闭合**

默认词表刻意做得很短：只有没人真想发出来的那些音。「那个」「就是」「like」「basically」这类**说话人可能真的想说**的词放在 `-aggressive` 后面——删掉它们改变的是「说了什么」，不是「怎么说的」。

先跑 `-list`。它按时间戳列出每一处删除和理由，什么都不改：

```
00:00:00.500  filler 嗯     -400ms
00:00:04.550  pause 2.8s    -2.1s
00:00:07.000  filler 呃     -400ms
00:00:09.100  repeat 很     -250ms
----
cuts       4 (2 filler, 1 repeat, 1 pause)
removed    2.59s of 12s (21.6%)
result     9.41s
```

这是发现 `-aggressive` 正在吞掉半句话的最便宜的方式。

剪切点是帧精确的，所以要重编码。识别器给出的词边界是近似的，所以每一处删除在落地前都会按 `-pad-head`/`-pad-tail` 从两侧各收回一点，避免削掉相邻词的辅音。

一处没剪到也照样产出文件（此时是流拷贝）。`vs filler a.mp4 && vs subtitle a.clean.mp4` 不该恰好在「这条录得很干净」的时候断掉。`vs silence` 同理。

### 加字幕

```bash
vs subtitle talk.mp4                          # 烧录（默认）
vs subtitle -mode soft -lang-tag zho talk.mp4 # 嵌入为可选字幕轨
vs subtitle -mode file -o talk.srt talk.mp4   # 只出字幕文件
vs subtitle -font 'PingFang SC' -font-size 56 talk.mp4
vs subtitle -sub talk.srt -f talk.mp4         # 用手工校对过的字幕烧录
```

字幕会**按可读性重新断句**，而不是照搬识别结果——识别器是按停顿分段的，经常一段就是二十秒的文字。`-max-chars` 和 `-max-lines` 控制版式，断行位置优先落在句末标点、停顿和字符预算上，两行时长度尽量均衡（19+3 看着就是坏的，11+11 不是）。

三种模式的取舍：`burn` 抗平台（很多平台会丢掉字幕轨），但不可关闭且要重编码；`soft` 快、可逆，但播放器不支持就等于没有；`file` 只出 `.srt`，视频原样不动。

## 组合起来

命令之间靠文件传递，所以想怎么串就怎么串：

```bash
vs transcribe talk.mp4                          # 一次识别
vs filler -transcript talk.json talk.mp4        # 复用，剪口水话
vs subtitle -transcript talk.json talk.clean.mp4  # 复用，加字幕
vs resize -size vertical talk.clean.sub.mp4     # 转竖屏
```

批量处理直接给多个文件：

```bash
vs transcribe -outdir out/ recordings/*.mp4
vs resize -size 1080p -outdir normalized/ clips/*.mov
```

给 agent 或脚本用就加 `-json`：单个输入输出一个对象，多个输入输出数组，其余信息全部走 stderr，退出码就是成败。

```bash
vs filler -list -json talk.mp4 | jq '.removed_ms / .source_ms'
```

## 通用参数

每条命令都接受：

| 参数 | 作用 |
|---|---|
| `-json` | 输出机器可读的 JSON |
| `-f` | 允许覆盖已存在的输出文件 |
| `-n` | 只打印将要执行的 ffmpeg 命令，不执行 |
| `-v` | 显示 ffmpeg 自己的输出 |
| `-q` | 除错误外全部静默 |
| `-config` | 指定配置文件 |

不给 `-o` 时，输出落在输入旁边，文件名带上说明发生了什么的后缀：`talk.clean.mp4`、`talk.sub.mp4`、`talk.1080x1920.mp4`。**输出文件不会覆盖输入**，也不会默认覆盖任何已存在的文件——手滑一次就吃掉素材的剪辑工具没人会用第二次。

`-n` 打印出来的是可以直接粘进终端的完整命令，想知道 vs 到底做了什么、或者想在它的基础上手改参数时很有用：

```bash
$ vs cut -n -keep 0:02-0:05 -keep 0:08-0:10 sample.mp4
# filter graph (2 segments):
[0:v]trim=start=2.000:end=5.000,setpts=PTS-STARTPTS[v0];
[0:a]atrim=start=2.000:end=5.000,asetpts=PTS-STARTPTS[a0];
[0:v]trim=start=8.000:end=10.000,setpts=PTS-STARTPTS[v1];
[0:a]atrim=start=8.000:end=10.000,asetpts=PTS-STARTPTS[a1];
[v0][a0][v1][a1]concat=n=2:v=1:a=1[outv][outa]
ffmpeg -hide_banner -loglevel error -nostdin -y -i sample.mp4 \
  -filter_complex_script /tmp/vs-filter-616447322.txt \
  -map '[outv]' -c:v libx264 -crf 20 -preset medium -pix_fmt yuv420p \
  -map '[outa]' -c:a aac -b:a 192k -movflags +faststart sample.cut.mp4
```

## 精确 vs 快速

凡是剪切的命令都有 `-fast`：

- **默认（重编码）** — 剪切点落在你指定的那一帧。长视频要跑几分钟。
- **`-fast`（流拷贝）** — 几秒钟完事，画质零损失，但**每个剪切点都会回退到前一个关键帧**，通常早 1 到 10 秒。

从长录像里粗选一段用 `-fast`；剪一个语气词绝对不要用。`vs filler` 因此不提供这个选项。

## 配置

所有参数都有能用的默认值，没有配置文件也能跑。想省下反复敲的那八个参数时，复制 [`config.example.yaml`](config.example.yaml) 到 `vs doctor` 报出的路径即可。

优先级：内置默认 < 配置文件 < `VS_` 环境变量 < 命令行参数。

配置文件里不存放任何密钥。需要调用云端模型的命令，密钥用 `vs credential set` 存进系统钥匙串或加密 vault：

```bash
vs credential set openai            # 提示输入，不回显
printf %s "$OPENAI_API_KEY" | vs credential set openai
vs credential status
```

密钥永远不作为命令行参数传入，因此不会出现在进程列表或 shell 历史里。剪辑命令本身不需要任何密钥——语音识别和全部 ffmpeg 操作都在本地。

## 数据格式

转写 JSON 是命令之间的交换格式，schema 稳定，也可以手工编辑：

```json
{
  "version": "vs.transcript.v1",
  "language": "zh",
  "duration_ms": 12000,
  "cues": [
    {
      "start_ms": 500, "end_ms": 4200,
      "text": "我们今天来聊一聊视频剪辑",
      "words": [
        {"text": "我", "start_ms": 500, "end_ms": 700, "score": 0.98}
      ]
    }
  ]
}
```

时间戳是相对源文件时间轴的**整数毫秒**。浮点秒在剪切点之间反复平移后会累积舍入误差，而晚 40ms 的字幕是看得出来的。

改完 `text` 或删掉某个 `cue` 再传回 `-transcript`，后续命令就按你改过的版本工作。

## 结构

```
cmd/vs/              命令定义：每条命令的参数、帮助、示例
internal/ffmpeg/     ffmpeg / ffprobe 调用层：参数构造、原子写出、错误提取
internal/asr/        语音识别接口 + faster-whisper 后端（内嵌 Python 脚本）
internal/transcript/ 转写数据模型、字幕断句、SRT/VTT 编码
internal/timespan/   区间与时间戳：所有命令共用的时间词汇
internal/filler/     口水话检测：只产出区间列表，不碰任何媒体文件
internal/credential/ 密钥存储：系统钥匙串 / 加密 vault / 环境变量
```

两条贯穿始终的规则：

- **输出文件要么完整，要么不存在。** 每次运行都先写到目标目录里的临时文件，成功后再 rename。中断的编码不会留下一个看起来已经完成的半截 MP4。
- **失败必须带上 ffmpeg 说了什么。** 退出码本身没有信息量，stderr 的最后几行才是诊断。

语音识别是唯一无法交给 ffmpeg 的能力，所以也是唯一引入第二套运行时的地方。它被关在 `internal/asr` 后面并且跑在独立进程里：Python 包缺失必须退化成 stderr 上的一句人话，而不是编译失败或者一个起不来的 Go 二进制。

## 开发

```bash
make check     # go vet + go test + 密钥扫描
make build     # 构建到 ./bin/vs
```

测试不需要 ffmpeg 也能跑（会自动跳过依赖它的用例），但装了 ffmpeg 的话会额外跑真实的端到端剪切——filter graph 光是「格式正确」不够，得 ffmpeg 真的接受才行。
