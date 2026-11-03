# WaveAlign · 音视频波形同步

把单独录制的音频（录音笔、调音台、领夹麦…）按波形自动对齐到一个或多个视频上，在时间轴上试听、微调，再把「原视频画面 + 对齐后的音频」导出为新视频。**导出时视频流直接复制（`-c:v copy`），画面不会二次编码。**

基于 [MyGo](https://mygo.egoist.dev) 原生 UI（纯 Go、无 cgo，界面由 GPU 绘制）和 ffmpeg。

## 功能

- **匹配源 1 · 外部音频**：可添加多个音频（WAV / MP3 / FLAC / M4A …，也可以是带音轨的视频），拖动或用 ↑↓ 排序，按顺序拼接成一条整体音频。支持选择文件内的音轨。
- **匹配源 2 · 视频**：可添加多个视频（MP4 / MOV / MKV / MXF …），每个视频可单独选择用哪条音轨参与匹配。
- **立体声 / 多声道**：匹配时可选「全部声道混合」或只用某一路声道（例如多轨录音机里领夹麦所在的声道）；回放统一下混为立体声；导出可选「同外部音频（保留原声道布局，如 5.1 / 4 声道）」、立体声或单声道。
- **自动对齐**，分两步：
  1. 粗定位：8 kHz 下用 GCC-PHAT 互相关在整条外部音频里搜索视频音轨的多个片段，多段投票（RANSAC）给出大致偏移和置信度；
  2. 精对齐：在 48 kHz 下沿整段视频每隔几秒测一次偏移（亚采样精度），剔除掌声、静音等不可靠的段落后拟合出**精确偏移**和录音机与相机之间的**时钟漂移**（ppm）。
- **时钟漂移校正**：两台设备的晶振总有几 ppm 的差异（例如 TASCAM 相对 Sony 相机约 +7.9 ppm，一首 5 分钟的歌首尾差约 2.4 ms，混合试听时会有“相位/镶边”感）。导出、试听和时间轴绘制都会按测得的漂移对外部音频做高质量 sinc 重采样，使整段从头到尾都对齐；每个视频卡片显示逐段误差。
- **时间轴**：标尺 + 拼接音频轨 + 每个视频一条轨道；选中视频的波形会叠加在外部音频轨上，直观对比是否对齐。拖动片段微调（按住 Shift 精细 1/10），或用按钮/键盘按 1 帧、10 ms、1 ms 微调，也可直接输入偏移秒数。
- **试听对比**：外部音频与视频原声在同一个解码器里混音，保证采样级同步。监听模式：外部音频 / 视频原声 / 混合 / **左右分离**（外部音频在左、原声在右，戴耳机最容易听出错位），两路音量可调。
- **画面预览**：ffmpeg 硬件解码（NVDEC / D3D11VA，失败自动回退 CPU），播放时随音频时钟逐帧显示。
- **导出**：视频流复制；新音轨按对齐位置从拼接音频中截取（采样级精度），长度与视频一致，视频早于录音开始的部分补静音。音频编码 AAC 320k / PCM 24-bit / FLAC，可选保留原视频音轨作为附加音轨。
- **工程**：保存/打开工程（JSON），解码缓存可在设置中清理。

## 快捷键

| 操作 | 按键 |
| --- | --- |
| 播放 / 暂停 | 空格 |
| 选中视频 ±1 帧 | ← / → |
| ±10 ms | Shift + ← / → |
| ±1 ms | Alt + ← / → |
| 跳到选中视频片头 | Home |
| 时间轴平移 / 缩放 / 上下滚动 | 滚轮 / Ctrl+滚轮 / Shift+滚轮 |
| 拖动平移 | 中键拖动或 Alt+左键拖动 |
| 保存 / 打开工程 | Ctrl+S / Ctrl+O |

文件可以直接拖进窗口：音频扩展名进匹配源 1，其余进匹配源 2。

## 依赖

- Go 1.27+
- ffmpeg 与 ffprobe：放在 `WaveAlign.exe` 同目录（或其 `ffmpeg\bin` 子目录）、加入 PATH，或在「设置」中指定目录。
  Windows 可用 `winget install Gyan.FFmpeg`；需要 NVDEC 硬解请使用带 `--enable-cuvid/--enable-nvdec` 的完整版（如 gyan.dev full build）。

## 构建（国内镜像）

```powershell
.\build.ps1          # 设置 goproxy.cn 镜像后执行 go tool mygo build
```

或手动：

```bash
export GOPROXY=https://goproxy.cn,direct GOSUMDB=sum.golang.google.cn
go tool mygo dev      # 开发模式，改代码自动重启
go tool mygo build    # 输出 build/windows-amd64/WaveAlign.exe 和安装包
go test ./...         # 单元测试 + 用 ffmpeg 生成素材的端到端测试
```

`go tool mygo build` 生成 Windows 安装包时会下载 NSIS；如果网络不通，可只用 `go build -o WaveAlign.exe .` 得到可执行文件。

## 发布（GitHub Actions）

推送 `v` 开头的标签即自动发布：

```bash
git tag v0.2.0
git push origin v0.2.0
```

[`.github/workflows/release.yml`](.github/workflows/release.yml) 会依次：

1. 在 Windows runner 上下载 ffmpeg，运行 `go vet` 和全部测试（runner 没有 GPU 和声卡：GPU 测试跳过，预览回退 CPU）；
2. 在 Ubuntu runner 上交叉编译 Windows 版（无 cgo），版本号取自标签（`v0.2.0` → `0.2.0`，写入 `mygo.json`）；
3. 创建 GitHub Release，附带：
   - `WaveAlign-<版本>-windows-amd64.zip`：便携版，附带 gyan.dev 的 ffmpeg / ffprobe（在 `ffmpeg\bin`，程序自动找到）；
   - `WaveAlign-<版本>-windows-amd64-setup.exe`：安装包，不含 ffmpeg；
   - `SHA256SUMS.txt`。

标签带连字符（如 `v0.2.0-beta.1`）时发布为预发布版本；格式不对的标签会直接失败。在 Actions 页面手动运行（workflow_dispatch）只构建，产物在运行结果里下载，不创建 Release。程序未做代码签名，Windows SmartScreen 首次运行可能提示。

## 代码结构

| 路径 | 内容 |
| --- | --- |
| `internal/align` | FFT、GCC-PHAT 分块互相关、多段一致性，48 kHz 逐段跟踪拟合偏移与漂移 |
| `internal/dsp` | Kaiser 窗 sinc 重采样（漂移校正、亚采样偏移） |
| `internal/media` | ffprobe 探测、音频解码缓存（48 kHz 16-bit）、分析用解码、预览帧、导出 |
| `internal/wave` | 多级波形峰值，任意缩放下快速绘制 |
| `internal/playback` | 外部音频与视频原声的同步混音解码器 |
| `*.go`（根目录） | MyGo 界面：侧栏、预览、时间轴、导出与设置对话框 |

## 说明

- 互相关在 CPU 上多线程计算（纯 Go 无法直接调用 CUDA），1 小时音频通常几秒内完成；GPU 用于界面绘制和预览解码。
- 导出时由第一个 ffmpeg 解码外部音频，程序内完成亚采样偏移和漂移校正，再交给第二个 ffmpeg 编码新音轨并直接复制视频流。
- 旧版（v1）工程打开时会自动换算成新的偏移定义；建议重新「自动对齐」以获得逐段精对齐结果。
