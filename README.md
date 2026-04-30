# llm-proxy-lite

轻量级 LLM 协议代理，面向 **OpenAI-compatible 上游**，同时兼容 Anthropic 和 OpenAI 客户端。

- Anthropic 客户端：`/v1/messages` 会转换为 OpenAI `/chat/completions` 后请求上游，再转换回 Anthropic 响应。
- OpenAI 客户端：`/v1/*` 会通过统一 OpenAI-compatible 封装透传到上游。

---

## 核心能力

### Anthropic 兼容

- ✅ `POST /v1/messages` → OpenAI `/chat/completions` → Anthropic 响应
- ✅ `POST /messages` 兼容别名
- ✅ 非流式与流式 SSE
- ✅ `system` 字符串 / text block 数组
- ✅ text / image / tool_use / tool_result / thinking block 转换
- ✅ Tool Calling 双向转换，支持流式 tool call 分片重组
- ✅ `metadata.user_id` → OpenAI `user`
- ✅ `stop_sequences` → OpenAI `stop`
- ✅ `MODEL_MAP_JSON` 显式模型映射，未命中原样透传
- ✅ usage 来自上游响应并映射为 Anthropic usage
- ✅ `POST /v1/messages/count_tokens` 本地粗略估算
- ✅ Anthropic Message Batches 明确返回 `not_supported`

### OpenAI 兼容

- ✅ OpenAI `/v1/*` 原生透传到 `OPENAI_API_BASE`
- ✅ `/chat/completions` 和 `/models` 无 `/v1` 前缀兼容别名
- ✅ JSON 请求透传
- ✅ SSE 流式响应透传
- ✅ multipart/form-data、文件、音频、图片等请求体流式透传
- ✅ 二进制响应透传
- ✅ 上游 4xx/5xx 状态码与响应体保留
- ✅ 非流式响应 usage 日志记录

### 鉴权

默认模式下，所有入站请求统一使用 `CLIENT_API_KEY` 校验，支持以下客户端传法：

```http
x-api-key: your-client-key
```

```http
Authorization: Bearer your-client-key
```

或：

```http
Authorization: x-api-key your-client-key
```

转发到上游时，代理统一替换为服务端配置的：

```http
Authorization: Bearer $OPENAI_API_KEY
```

如果设置 `API_KEY_DIRECTY=true`，代理不再要求 `CLIENT_API_KEY`，而是直接把客户端传入的 API key 作为上游认证 key。

---

## 快速开始

### 构建

```bash
go build -o llm-proxy-lite ./cmd/llm-proxy-lite
```

### 启动

默认模式：

```bash
OPENAI_API_BASE=https://your-openai-compatible.example/v1 \
OPENAI_API_KEY=sk-xxxxxxxxxxxx \
CLIENT_API_KEY=your-client-key \
./llm-proxy-lite
```

直接透传客户端 API key 模式：

```bash
OPENAI_API_BASE=https://your-openai-compatible.example/v1 API_KEY_DIRECTY=true ./llm-proxy-lite
```

### Claude Code 接入

```bash
export ANTHROPIC_BASE_URL=http://localhost:3000
export ANTHROPIC_API_KEY=your-client-key
claude
```

### OpenAI 客户端接入

```bash
export OPENAI_BASE_URL=http://localhost:3000/v1
export OPENAI_API_KEY=your-client-key
```

### Docker 使用

拉取镜像：

```bash
docker pull ghcr.io/w101723/llm-proxy-lite:latest
```

默认模式运行：

```bash
docker run --rm -p 3000:3000 \
  -e OPENAI_API_BASE=https://your-openai-compatible.example/v1 \
  -e OPENAI_API_KEY=sk-xxxxxxxxxxxx \
  -e CLIENT_API_KEY=your-client-key \
  ghcr.io/w101723/llm-proxy-lite:latest
```

直接透传客户端 API key 模式：

```bash
docker run --rm -p 3000:3000 \
  -e OPENAI_API_BASE=https://your-openai-compatible.example/v1 \
  -e API_KEY_DIRECTY=true \
  ghcr.io/w101723/llm-proxy-lite:latest
```

---

## 环境变量

| 变量名 | 默认值 | 必填 | 说明 |
|---|---:|---:|---|
| `OPENAI_API_KEY` | — | 默认模式必填 | 上游 OpenAI-compatible 服务 API Key；`API_KEY_DIRECTY=true` 时不需要 |
| `CLIENT_API_KEY` | — | 默认模式必填 | 代理入站鉴权 Key；`API_KEY_DIRECTY=true` 时不需要 |
| `API_KEY_DIRECTY` | `false` | 否 | `true` 时直接透传客户端 API key 到上游认证 |
| `OPENAI_API_BASE` | `https://api.openai.com/v1` | 是 | 上游 OpenAI-compatible API Base |
| `PORT` | `3000` | 否 | 监听端口 |
| `HOST` | `0.0.0.0` | 否 | 监听地址 |
| `LOG_LEVEL` | `info` | 否 | `debug` / `info` / `none` |
| `MODEL_MAP_JSON` | — | 否 | 模型名映射 JSON 对象；未配置或未命中时原样透传 |

示例：

```bash
MODEL_MAP_JSON='{"claude-sonnet-4-6":"mimo-v2.5"}' \
OPENAI_API_BASE=https://your-openai-compatible.example/v1 \
OPENAI_API_KEY=sk-xxxxxxxxxxxx \
CLIENT_API_KEY=your-client-key \
./llm-proxy-lite
```

---

## 模型映射

默认不设置任何模型映射，收到的模型名会原样透传到上游。

如需将客户端模型名映射为上游模型名：

```bash
MODEL_MAP_JSON='{"claude-sonnet-4-6":"mimo-v2.5","gpt-4o":"mimo-v2.5"}'
```

规则：

- 命中 `MODEL_MAP_JSON`：使用映射后的模型名
- 未命中：原样透传
- 未传模型：原样交给上游校验

---

## 接口兼容

### 本地服务接口

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/` | 返回 `{ "status": "ok" }` |
| `HEAD` | `/` | 返回 200 空响应 |
| `GET` | `/health` | 返回 `{ "status": "ok", "uptime": ... }` |

### Anthropic 接口

| 方法 | 路径 | 状态 | 说明 |
|---|---|---:|---|
| `POST` | `/v1/messages` | ✅ | Anthropic Messages 转 OpenAI Chat Completions |
| `POST` | `/messages` | ✅ | `/v1/messages` 兼容别名 |
| `POST` | `/v1/messages/count_tokens` | ⚠️ | 本地粗略估算 `input_tokens` |
| `POST` | `/messages/count_tokens` | ⚠️ | count_tokens 兼容别名 |
| `ALL` | `/v1/messages/batches` | ❌ | 返回 Anthropic 格式 `not_supported` |
| `ALL` | `/v1/messages/batches/:batch_id` | ❌ | 返回 `not_supported` |
| `ALL` | `/v1/messages/batches/:batch_id/cancel` | ❌ | 返回 `not_supported` |
| `ALL` | `/v1/messages/batches/:batch_id/results` | ❌ | 返回 `not_supported` |
| `POST` | `/v1/complete` | ❌ | 旧版接口，返回 `not_supported` |

### OpenAI 接口

除 Anthropic 专属路由外，所有 `/v1/*` 均透传到上游 `OPENAI_API_BASE` 对应路径。

| 方法 | 路径 | 状态 | 说明 |
|---|---|---:|---|
| `ALL` | `/v1/*` | ✅ | OpenAI-compatible 上游透传 |
| `ALL` | `/chat/completions` | ✅ | `/v1/chat/completions` 兼容别名 |
| `ALL` | `/models` | ✅ | `/v1/models` 兼容别名 |

常见可透传接口包括：

- `/v1/chat/completions`
- `/v1/responses`
- `/v1/completions`
- `/v1/embeddings`
- `/v1/moderations`
- `/v1/images/*`
- `/v1/audio/*`
- `/v1/files/*`
- `/v1/batches/*`
- `/v1/fine_tuning/*`
- `/v1/assistants/*`
- `/v1/threads/*`
- `/v1/vector_stores/*`

实际是否可用取决于上游 `OPENAI_API_BASE`。

---

## Anthropic `/v1/messages` 转换能力

### 请求侧

| Anthropic 字段 / Block | 状态 | OpenAI 映射 |
|---|---:|---|
| `model` | ✅ | 按 `MODEL_MAP_JSON` 映射后传入 OpenAI `model` |
| `messages` | ✅ | 转 OpenAI `messages` |
| `system` string | ✅ | 转 OpenAI system message |
| `system` text block array | ✅ | 合并 text block，过滤 `cache_control` |
| `max_tokens` | ✅ | OpenAI `max_tokens` |
| `temperature` | ✅ | 透传 |
| `top_p` | ✅ | 透传 |
| `stop_sequences` | ✅ | OpenAI `stop` |
| `stream` | ✅ | OpenAI streaming + Anthropic SSE 转换 |
| `tools` | ✅ | OpenAI function tools |
| `tool_choice` | ✅ | OpenAI `tool_choice` |
| `metadata.user_id` | ✅ | OpenAI `user` |
| text block | ✅ | OpenAI text content |
| image block | ✅ | OpenAI `image_url` |
| tool_use block | ✅ | OpenAI `tool_calls` |
| tool_result block | ✅ | OpenAI `role: "tool"` |
| thinking block | ✅ | OpenAI `reasoning_content` |
| redacted_thinking block | ⚠️ | 请求侧 data 透传为 `reasoning_content` |
| `top_k` | 忽略 | OpenAI 不支持 |
| 顶层 `thinking` / `extended_thinking` | 忽略 | OpenAI 不支持 |
| `cache_control` | 忽略 | OpenAI Chat Completions 无等价字段 |

### 响应侧

| OpenAI 字段 | Anthropic 映射 |
|---|---|
| `message.content` | text block |
| `message.reasoning_content` | thinking block |
| `message.tool_calls` | tool_use block |
| `finish_reason: stop` | `stop_reason: end_turn` |
| `finish_reason: length` | `stop_reason: max_tokens` |
| `finish_reason: tool_calls` | `stop_reason: tool_use` |
| `usage.prompt_tokens` | `usage.input_tokens` |
| `usage.completion_tokens` | `usage.output_tokens` |
| `usage.prompt_tokens_details.cached_tokens` | `usage.cache_read_input_tokens` |
| — | `usage.cache_creation_input_tokens = 0` |

---

## 日志

日志时间戳使用东八区：

```text
[2026-04-29 14:36:36] [INFO] ← OpenAI 200 POST /v1/chat/completions | 248in/113out/361total | cache_read=192 cache_creation=0 | 2072ms
```

Anthropic 请求日志示例：

```text
[2026-04-29 14:36:36] [INFO] → Anthropic POST /v1/messages | model=claude-sonnet-4-6 → mimo-v2.5 | stream=true | tools=62
[2026-04-29 14:36:38] [INFO] ← Anthropic end_turn | 248in/113out/361total | cache_read=192 cache_creation=0 | 2072ms
```

OpenAI 请求日志示例：

```text
[2026-04-29 14:36:38] [INFO] → OpenAI POST /v1/chat/completions | model=mimo-v2.5 | stream=false
[2026-04-29 14:36:39] [INFO] ← OpenAI 200 POST /v1/chat/completions | 248in/59out/307total | cache_read=192 cache_creation=0 | 1381ms
```

`LOG_LEVEL=debug` 时会打印转换前后的请求体。

---

## 手动测试

### Anthropic Messages

```bash
curl -X POST http://localhost:3000/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: your-client-key" \
  -d '{
    "model": "claude-sonnet-4-6",
    "max_tokens": 100,
    "messages": [{"role": "user", "content": "Say hello"}]
  }'
```

### OpenAI Chat Completions

```bash
curl -X POST http://localhost:3000/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-client-key" \
  -d '{
    "model": "mimo-v2.5",
    "messages": [{"role": "user", "content": "Say hello"}]
  }'
```

### Health Check

```bash
curl http://localhost:3000/health
```

---

## 二进制打包

项目使用 Go 构建单文件可执行程序。

```bash
make build-bin
```

Make 目标：

```bash
make build-bin
make release-linux-amd64
make release-linux-arm64
make release-darwin-arm64
make release-windows-amd64
```

默认产物位于 `dist/<platform>-<arch>/`，例如：

- `dist/linux-amd64/llm-proxy-lite-linux-amd64`
- `dist/linux-arm64/llm-proxy-lite-linux-arm64`
- `dist/darwin-arm64/llm-proxy-lite-darwin-arm64`
- `dist/win32-amd64/llm-proxy-lite-win32-amd64.exe`

### macOS 运行注意事项

Apple Silicon 上二进制需要签名。项目构建脚本已自动执行 ad-hoc 签名；如需手动处理：

```bash
codesign --force --sign - <二进制文件路径>
xattr -cr <二进制文件路径>
chmod +x <二进制文件路径>
```

---

## 已知限制

- Anthropic Prompt Caching 标记 `cache_control` 会被忽略。
- Anthropic Message Batches 不做 OpenAI Batch 伪转换，明确返回 `not_supported`。
- `/v1/messages/count_tokens` 是本地粗略估算，不等同于真实 tokenizer 结果。
- OpenAI `role: "tool"` 消息不支持图片，Anthropic tool_result 图片会降级为文本占位符。
- OpenAI `/v1/*` 为透传模式，实际支持能力取决于上游 `OPENAI_API_BASE`。
- 非 JSON 流式请求体无法安全重试，遇到网络错误会直接返回错误。

---

## 文件结构

```text
.
├── cmd/llm-proxy-lite/main.go
├── internal/
├── Makefile
├── Dockerfile
├── go.mod
└── README.md
```
