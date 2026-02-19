# RapidSpeech.cpp 安装指南

## ⚠️ Windows 平台编译问题

**当前状态**: RapidSpeech.cpp 在 Windows 平台使用 MSVC 编译时存在 DLL 导出宏问题，导致编译失败。

错误信息：
```
error C2491: 'rs_*': definition of dllimport function not allowed
```

## 解决方案

### 方案 1: 在 Linux/macOS 上编译 ✅（推荐）

RapidSpeech.cpp 在 Linux/macOS 上编译顺利：

```bash
# 克隆仓库
git clone https://github.com/RapidAI/RapidSpeech.cpp
cd RapidSpeech.cpp

# 初始化子模块
git submodule sync && git submodule update --init --recursive

# 编译
cmake -B build -DCMAKE_BUILD_TYPE=Release
cmake --build build --config Release -j$(nproc)

# 可执行文件位于
# build/examples/rs-asr-offline
```

### 方案 2: 使用 WSL (Windows Subsystem for Linux)

在 Windows 上可以通过 WSL 编译：

```bash
# 在 WSL 中执行上述 Linux 编译步骤
wsl
# 然后按方案 1 的步骤编译
```

### 方案 3: 等待项目修复或使用 SenseVoice.cpp

如果急需在 Windows 上使用，可暂时使用 SenseVoice.cpp：

```bash
# SenseVoice.cpp 有 Windows 预编译版本
# https://github.com/lovemefan/SenseVoice.cpp/releases
```

需要将代码回退到 SenseVoice：参考 git commit history。

---

## 模型下载

### RapidSpeech 模型（GGUF 格式）

**Hugging Face（推荐）**:
```bash
# 使用 git-lfs 下载
git lfs install
git clone https://huggingface.co/RapidAI/RapidSpeech
```

**ModelScope（国内镜像）**:
```bash
# 访问 https://www.modelscope.cn/models/RapidAI/RapidSpeech
# 下载所需的 GGUF 模型文件
```

### 推荐模型

| 模型名称 | 大小 | 精度 | 速度 | 适用场景 |
|---------|------|------|------|----------|
| Q4 | 最小 | 较低 | 最快 | 边缘设备，CPU only |
| **Q5** | 中等 | 平衡 | 快 | **通用推荐** |
| Q6 | 较大 | 高 | 较慢 | 高精度需求 |

---

## 系统配置

### 在管理界面配置

1. 登录管理后台
2. 进入 **多模态** 设置
3. 填写：
   - **RapidSpeech 可执行文件路径**:
     - Linux: `/path/to/RapidSpeech.cpp/build/examples/rs-asr-offline`
     - WSL: 在 Windows 中使用 `\\wsl$\Ubuntu\path\to\rs-asr-offline`
   - **RapidSpeech 模型文件路径**: `/path/to/model.gguf`
4. 点击 **重新检测** 验证配置
5. **保存设置**

---

## 测试

测试 RapidSpeech 是否正常工作：

```bash
# 准备一个 16kHz 单声道 WAV 音频文件
ffmpeg -i input.mp4 -vn -acodec pcm_s16le -ar 16000 -ac 1 test.wav

# 运行转录
./rs-asr-offline -m /path/to/model.gguf -w test.wav
```

应该输出转录文本。

---

## 故障排除

### 编译错误：C2491

**原因**: Windows MSVC 的 DLL 导出宏问题

**解决**:
- 使用 Linux/macOS/WSL 编译
- 或等待项目官方修复

### 模型加载失败

**检查**:
1. 模型文件是否为 `.gguf` 格式（不是 `.bin`）
2. 模型文件路径是否正确
3. 文件权限是否可读

### FFmpeg 未找到

RapidSpeech 依赖 FFmpeg 提取音频，确保：
```bash
ffmpeg -version  # 应该能正常输出版本信息
```

---

## 参考资源

- [RapidSpeech.cpp GitHub](https://github.com/RapidAI/RapidSpeech.cpp)
- [模型下载 - Hugging Face](https://huggingface.co/RapidAI/RapidSpeech)
- [模型下载 - ModelScope](https://www.modelscope.cn/models/RapidAI/RapidSpeech)
- [ggml 文档](https://github.com/ggml-org/ggml)

---

## 当前实现状态

- ✅ 后端代码已更新为 RapidSpeech.cpp
- ✅ 前端界面已更新
- ✅ 配置结构已迁移
- ⚠️ Windows MSVC 编译存在问题（项目本身的问题）
- ✅ Linux/macOS 编译正常

如需在 Windows 上立即使用，建议使用 WSL 或等待项目修复。
