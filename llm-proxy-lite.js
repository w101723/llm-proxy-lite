/**
 * Anthropic / OpenAI → OpenAI-compatible 协议代理
 * - Anthropic Messages API 转 OpenAI Chat Completions
 * - OpenAI /v1/* 原生透传到 OpenAI-compatible 上游
 *
 * 启动方式:
 *   OPENAI_API_KEY=sk-xxx CLIENT_API_KEY=your-client-key node llm-proxy-lite.js
 *   API_KEY_DIRECTY=true node llm-proxy-lite.js
 *
 * Claude Code 接入:
 *   export ANTHROPIC_BASE_URL=http://localhost:3000
 *   export ANTHROPIC_API_KEY=your-client-key
 *   claude -p "fix the bug"
 *
 * OpenAI 客户端接入:
 *   export OPENAI_BASE_URL=http://localhost:3000/v1
 *   export OPENAI_API_KEY=your-client-key
 *
 * 支持自定义上游:
 *   OPENAI_API_BASE=https://your-proxy.com/v1 node llm-proxy-lite.js
 *
 * 已处理能力:
 *   ✅ Anthropic /v1/messages 转 OpenAI /chat/completions（非流式 + 流式 SSE）
 *   ✅ OpenAI /v1/* 统一透传（实际能力取决于上游）
 *   ✅ 入站 CLIENT_API_KEY 鉴权，或 API_KEY_DIRECTY=true 直接透传客户端 API key
 *   ✅ system 字段（字符串 / block 数组，过滤 cache_control）
 *   ✅ 多模态图片（base64 / url → OpenAI image_url 格式）
 *   ✅ Tool Calling 双向转换（含流式 tool call 分片重组）
 *   ✅ tool_result → role:"tool" 消息转换，图片降级为文本占位符
 *   ✅ thinking block ↔ reasoning_content；顶层 thinking / extended_thinking 参数忽略
 *   ✅ top_k 参数忽略（OpenAI 不支持）
 *   ✅ content 为空时补空文本 block（防止客户端报错）
 *   ✅ usage 来自上游响应，并映射 input/output/cache_read 字段
 *   ✅ metadata.user_id → OpenAI user 字段
 *   ✅ MODEL_MAP_JSON 显式模型映射；未命中则原样透传
 *   ✅ /v1/messages/count_tokens 本地粗略估算
 *   ✅ Anthropic /v1/messages/batches 明确返回 not_supported
 */

import http from 'http'
import https from 'https'
import express from 'express'
import fetch from 'node-fetch'
import { createParser } from 'eventsource-parser'

// 禁用 keep-alive，避免上游 HTTP/2 连接复用导致 INTERNAL_ERROR
const httpAgent  = new http.Agent({ keepAlive: false })
const httpsAgent = new https.Agent({ keepAlive: false })
const upstreamAgent = (url) => url.startsWith('https') ? httpsAgent : httpAgent

// ─── 配置 ───────────────────────────────────────────────────────────────────

const PORT             = process.env.PORT             || 3000
const HOST             = process.env.HOST             || '0.0.0.0'
const OPENAI_API_KEY   = process.env.OPENAI_API_KEY || ''
const CLIENT_API_KEY    = process.env.CLIENT_API_KEY || ''
const API_KEY_DIRECTY   = String(process.env.API_KEY_DIRECTY ?? 'false').toLowerCase() === 'true'
const OPENAI_API_BASE  = (process.env.OPENAI_API_BASE || 'https://api.openai.com/v1').replace(/\/$/, '')
const LOG_LEVEL        = process.env.LOG_LEVEL        || 'info'   // debug | info | none

// ─── 日志 ───────────────────────────────────────────────────────────────────

const timestamp = () => {
  const utc8 = new Date(Date.now() + 8 * 60 * 60 * 1000)
  const iso = utc8.toISOString().replace('T', ' ')
  return iso.slice(0, 19)
}

const MODEL_MAP = (() => {
  const raw = process.env.MODEL_MAP_JSON
  if (!raw) return {}

  try {
    const parsed = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
      throw new Error('MODEL_MAP_JSON must be a JSON object')
    }

    return Object.fromEntries(
      Object.entries(parsed).map(([key, value]) => [String(key), String(value)])
    )
  } catch (err) {
    console.error(`[${timestamp()}] [ERROR] 无效的 MODEL_MAP_JSON:`, err.message)
    process.exit(1)
  }
})()

// ─── 日志 ───────────────────────────────────────────────────────────────────

const log = {
  debug: (...args) => LOG_LEVEL === 'debug' && console.log(`[${timestamp()}] [DEBUG]`, ...args),
  info:  (...args) => LOG_LEVEL !== 'none'  && console.log(`[${timestamp()}] [INFO]`, ...args),
  warn:  (...args) => LOG_LEVEL !== 'none'  && console.warn(`[${timestamp()}] [WARN]`, ...args),
  error: (...args) => console.error(`[${timestamp()}] [ERROR]`, ...args),
}

// ─── 工具函数 ────────────────────────────────────────────────────────────────

/**
 * 把 Anthropic model 名映射到 OpenAI model 名
 * - MODEL_MAP_JSON 有映射 → 使用映射后的模型
 * - 无映射 → 透传原始模型名
 */
function mapModel(claudeModel) {
  if (claudeModel in MODEL_MAP) return MODEL_MAP[claudeModel]
  return claudeModel
}

/**
 * 把单个 Anthropic image block 转换成 OpenAI image_url content part
 * Fix #2: 正确转换多模态图片格式，而非降级成文本占位符
 *
 * Anthropic: { type:"image", source:{ type:"base64", media_type, data } }
 * OpenAI:    { type:"image_url", image_url:{ url:"data:image/jpeg;base64,..." } }
 */
function convertImageBlock(block) {
  const src = block.source
  if (!src) return null

  if (src.type === 'base64') {
    return {
      type:      'image_url',
      image_url: { url: `data:${src.media_type};base64,${src.data}` },
    }
  }
  if (src.type === 'url') {
    return {
      type:      'image_url',
      image_url: { url: src.url },
    }
  }
  // src.type === 'file' 等其他类型暂不支持，降级为空
  log.warn(`Unsupported image source type: ${src.type}`)
  return null
}

/**
 * 把 Anthropic messages + system 转换成 OpenAI messages
 *
 * Fix #3: system block 数组过滤 cache_control，只取 type:"text" 的 block
 * Fix #2: image block 转换成 OpenAI image_url 格式（而非文本占位符）
 */
function convertMessages(messages, system, mappedModel) {
  const result = []

  if (system) {
    let systemText
    if (typeof system === 'string') {
      systemText = system
    } else {
      // Fix #3: 过滤掉非 text block（如 cache_control 附属 block）
      systemText = system
        .filter(b => b.type === 'text')
        .map(b => b.text)
        .join('\n')
    }
    if (systemText) {
      result.push({ role: 'system', content: systemText })
    }
  }

  for (const msg of messages) {
    const role = msg.role // user | assistant

    // content 是纯字符串，直接用
    if (typeof msg.content === 'string') {
      result.push({ role, content: msg.content })
      continue
    }

    // content block 数组 → 拆分处理
    // OpenAI user 消息的 content 可以是 part 数组（支持图片）
    // OpenAI assistant 消息的 content 只能是字符串或 null（tool_calls 另存）
    const textParts       = []   // 纯文本片段
    const imageParts      = []   // 图片 part（仅 user/tool 消息有效）
    const toolCalls       = []   // assistant 发起的 tool_calls
    const toolResults     = []   // user 侧的 tool_result → role:"tool" 消息
    const thinkingParts   = []   // thinking block → reasoning_content

    for (const block of msg.content) {
      if (block.type === 'text') {
        textParts.push({ type: 'text', text: block.text })

      } else if (block.type === 'thinking') {
        // Anthropic extended thinking block → 上游 reasoning_content
        // 必须原样回传，否则上游报 "reasoning_content must be passed back"
        thinkingParts.push(block.thinking ?? '')

      } else if (block.type === 'redacted_thinking') {
        // 上游加密的 thinking block，透传 data 字段
        thinkingParts.push(block.data ?? '')

      } else if (block.type === 'image') {
        // Fix #2: 真正转换图片格式
        const imgPart = convertImageBlock(block)
        if (imgPart) imageParts.push(imgPart)

      } else if (block.type === 'tool_use') {
        // Anthropic tool_use → OpenAI tool_calls
        toolCalls.push({
          id:       block.id,
          type:     'function',
          function: {
            name:      block.name,
            arguments: typeof block.input === 'string'
              ? block.input
              : JSON.stringify(block.input),
          },
        })

      } else if (block.type === 'tool_result') {
        // Anthropic tool_result → OpenAI role:"tool" 消息
        // tool_result 的 content 也可能包含图片，一并处理
        let toolContent
        if (typeof block.content === 'string') {
          toolContent = block.content
        } else if (Array.isArray(block.content)) {
          toolContent = block.content
            .map(b => {
              if (b.type === 'text') return b.text
              if (b.type === 'image') return `[Image omitted: ${b.source?.media_type ?? b.source?.type ?? 'unknown'}]`
              return ''
            })
            .filter(Boolean)
            .join('\n')
        } else {
          toolContent = ''
        }
        toolResults.push({
          role:         'tool',
          tool_call_id: block.tool_use_id,
          content:      toolContent,
        })

      } else {
        log.debug(`Skipping unknown block type: ${block.type}`)
      }
    }

    // 组合成 OpenAI 消息格式
    if (toolResults.length > 0) {
      // tool_result 必须单独成消息，role 固定为 "tool"
      result.push(...toolResults)

    } else if (toolCalls.length > 0) {
      // assistant 发起 tool_calls：content 只能是字符串
      const textContent = textParts.map(p => p.text).join('\n') || null
      const msg = { role, content: textContent, tool_calls: toolCalls }
      if (thinkingParts.length > 0) {
        msg.reasoning_content = thinkingParts.join('\n')
      } else if (role === 'assistant' && (mappedModel.includes('deepseek') || mappedModel.includes('reason'))) {
        msg.reasoning_content = ''
      }
      result.push(msg)

    } else {
      // 普通消息：如果有图片，content 用 part 数组；否则用字符串
      const allParts = [...textParts, ...imageParts]
      let msg
      if (imageParts.length > 0) {
        // OpenAI multimodal：content 是 part 数组
        msg = { role, content: allParts }
      } else {
        // 纯文本：content 是字符串（更兼容）
        msg = { role, content: textParts.map(p => p.text).join('\n') }
      }
      
      if (thinkingParts.length > 0) {
        msg.reasoning_content = thinkingParts.join('\n')
      } else if (role === 'assistant' && (mappedModel.includes('deepseek') || mappedModel.includes('reason'))) {
        msg.reasoning_content = ''
      }
      result.push(msg)
    }
  }

  return result
}

/**
 * 把 Anthropic tools 转换成 OpenAI tools
 * Anthropic: { name, description, input_schema }
 * OpenAI:    { type:"function", function: { name, description, parameters } }
 */
function convertTools(tools) {
  if (!tools?.length) return undefined
  return tools.map(t => ({
    type:     'function',
    function: {
      name:        t.name,
      description: t.description ?? '',
      parameters:  t.input_schema,
    },
  }))
}

/**
 * 把 Anthropic tool_choice 转换成 OpenAI tool_choice
 */
function convertToolChoice(toolChoice) {
  if (!toolChoice) return undefined
  if (toolChoice.type === 'auto')  return 'auto'
  if (toolChoice.type === 'any')   return 'required'
  if (toolChoice.type === 'none')  return 'none'
  if (toolChoice.type === 'tool')  return { type: 'function', function: { name: toolChoice.name } }
  return 'auto'
}

function estimateTokens(value) {
  if (value == null) return 0
  if (typeof value === 'string') return Math.ceil(value.length / 4)
  if (typeof value === 'number' || typeof value === 'boolean') return 1
  if (Array.isArray(value)) return value.reduce((sum, item) => sum + estimateTokens(item), 0)
  if (typeof value === 'object') return Object.values(value).reduce((sum, item) => sum + estimateTokens(item), 0)
  return 0
}

function safeTokenCount(value, fallback = 0) {
  const number = Number(value)
  if (!Number.isFinite(number) || number < 0) return fallback
  return Math.floor(number)
}

function convertUsage(usage) {
  const outputTokens = safeTokenCount(usage?.completion_tokens)
  const totalTokens = safeTokenCount(usage?.total_tokens)
  const inputTokens = usage?.prompt_tokens == null
    ? Math.max(totalTokens - outputTokens, 0)
    : safeTokenCount(usage.prompt_tokens)

  return {
    input_tokens:                inputTokens,
    output_tokens:               outputTokens,
    cache_read_input_tokens:     safeTokenCount(usage?.prompt_tokens_details?.cached_tokens),
    cache_creation_input_tokens: 0,
  }
}

function formatTokenUsage(usage) {
  const input = safeTokenCount(usage?.input_tokens)
  const output = safeTokenCount(usage?.output_tokens)
  const cacheRead = safeTokenCount(usage?.cache_read_input_tokens)
  const cacheCreation = safeTokenCount(usage?.cache_creation_input_tokens)
  return `${input}in/${output}out/${input + output}total | cache_read=${cacheRead} cache_creation=${cacheCreation}`
}

/**
 * 把 OpenAI 非流式响应转换成 Anthropic 格式
 *
 * Fix #5: content 为空时补一个空文本 block，防止客户端报错
 * Fix #6: usage 补全 cache_read_input_tokens / cache_creation_input_tokens
 */
function convertResponse(openaiResp, requestId) {
  const choice  = openaiResp.choices?.[0]
  const message = choice?.message ?? {}
  const content = []

  // reasoning_content（上游 thinking 模式）→ Anthropic thinking block
  if (message.reasoning_content) {
    content.push({ type: 'thinking', thinking: message.reasoning_content })
  }

  // 普通文本
  if (message.content) {
    content.push({ type: 'text', text: message.content })
  }

  // Tool calls → Anthropic tool_use blocks
  if (message.tool_calls?.length) {
    for (const tc of message.tool_calls) {
      let parsed
      try   { parsed = JSON.parse(tc.function.arguments) }
      catch { parsed = tc.function.arguments }
      content.push({
        type:  'tool_use',
        id:    tc.id,
        name:  tc.function.name,
        input: parsed,
      })
    }
  }

  // Fix #5: content 不能为空数组，至少补一个空文本 block
  if (content.length === 0) {
    content.push({ type: 'text', text: '' })
  }

  // finish_reason 映射
  const finishMap = {
    stop:           'end_turn',
    length:         'max_tokens',
    tool_calls:     'tool_use',
    content_filter: 'stop_sequence',
  }

  return {
    id:            requestId || openaiResp.id || `msg_${Date.now()}`,
    type:          'message',
    role:          'assistant',
    model:         openaiResp.model,
    content,
    stop_reason:   finishMap[choice?.finish_reason] ?? 'end_turn',
    stop_sequence: null,
    usage: convertUsage(openaiResp.usage),
  }
}

// ─── 流式转换工具 ────────────────────────────────────────────────────────────

/**
 * 把 OpenAI SSE 流转换成 Anthropic SSE 流
 * OpenAI:   data: {"choices":[{"delta":{"content":"..."}}]}
 * Anthropic: 一系列 event: content_block_delta 等事件
 */
async function streamConvert(openaiStream, res, requestId) {
  const msgId = requestId || `msg_${Date.now()}`

  // Anthropic 流开头固定事件
  const send = (event, data) => {
    res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
  }

  send('message_start', {
    type: 'message_start',
    message: {
      id:      msgId,
      type:    'message',
      role:    'assistant',
      content: [],
      model:   '',
      usage:   { 
        input_tokens: 0, 
        output_tokens: 0,
        cache_read_input_tokens: 0,
        cache_creation_input_tokens: 0
      },
    },
  })

  send('ping', { type: 'ping' })

  // ── 惰性 block 管理 ──────────────────────────────────────────────────────
  // 不在流开头预先声明 content_block_start，而是等看到第一个 delta 再决定类型。
  // 这样 thinking block 才能以正确的 type:"thinking" 发出，
  // SDK 才能在多轮对话中正确重建 assistant 消息（含 thinking block），
  // 下一轮才能正确回传 reasoning_content，避免上游 400。
  let nextBlockIndex    = 0
  let thinkingIndex     = -1   // thinking block 的 index，-1 = 尚未打开
  let textIndex         = -1   // text block 的 index，-1 = 尚未打开

  const openBlock = (type) => {
    const idx = nextBlockIndex++
    send('content_block_start', {
      type:          'content_block_start',
      index:         idx,
      content_block: type === 'thinking'
        ? { type: 'thinking', thinking: '' }
        : { type: 'text',    text: '' },
    })
    return idx
  }

  // 用于收集 tool_calls（OpenAI 流式 tool call 是分片发送的）
  const toolCallBuffers = {}
  let   inputTokens       = 0
  let   outputTokens      = 0
  let   cacheReadTokens   = 0
  let   finishReason      = 'end_turn'

  // 用 eventsource-parser 解析 OpenAI SSE
  await new Promise((resolve, reject) => {
    const parser = createParser((event) => {
      if (event.data === '[DONE]') { resolve(); return }
      try {
        const chunk  = JSON.parse(event.data)
        
        // 累计 token 用量（流式 usage 在最后一个 chunk 或无 choices 的附加 chunk）
        if (chunk.usage) {
          const usage = convertUsage(chunk.usage)
          outputTokens    = usage.output_tokens || outputTokens
          inputTokens     = usage.input_tokens || inputTokens
              cacheReadTokens = usage.cache_read_input_tokens || cacheReadTokens
        }

        const choice = chunk.choices?.[0]
        if (!choice) return

        if (choice.finish_reason) {
          const map = { stop:'end_turn', length:'max_tokens', tool_calls:'tool_use' }
          finishReason = map[choice.finish_reason] ?? 'end_turn'
        }

        const delta = choice.delta
        if (!delta) return

        // reasoning_content delta → Anthropic thinking_delta
        if (delta.reasoning_content) {
          if (thinkingIndex === -1) thinkingIndex = openBlock('thinking')
          send('content_block_delta', {
            type:  'content_block_delta',
            index: thinkingIndex,
            delta: { type: 'thinking_delta', thinking: delta.reasoning_content },
          })
        }

        // 文本 delta
        if (delta.content) {
          if (textIndex === -1) textIndex = openBlock('text')
          send('content_block_delta', {
            type:  'content_block_delta',
            index: textIndex,
            delta: { type: 'text_delta', text: delta.content },
          })
        }

        // Tool call delta（OpenAI 流式 tool calls 是分片的）
        if (delta.tool_calls) {
          for (const tc of delta.tool_calls) {
            const idx = tc.index ?? 0
            if (!toolCallBuffers[idx]) {
              toolCallBuffers[idx] = { id: '', name: '', arguments: '' }
            }
            const buf = toolCallBuffers[idx]
            if (tc.id)                     buf.id        += tc.id
            if (tc.function?.name)         buf.name      += tc.function.name
            if (tc.function?.arguments)    buf.arguments += tc.function.arguments
          }
        }

      } catch (e) {
        log.debug('SSE parse error:', e.message)
      }
    })

    openaiStream.on('data',  chunk  => parser.feed(chunk.toString()))
    openaiStream.on('end',   resolve)
    openaiStream.on('error', reject)
  })

  // 关闭已打开的 block（按 index 顺序）
  if (thinkingIndex !== -1) {
    send('content_block_stop', { type: 'content_block_stop', index: thinkingIndex })
  }
  if (textIndex !== -1) {
    send('content_block_stop', { type: 'content_block_stop', index: textIndex })
  }

  // 如果上游没有发出任何文本/thinking（纯 tool_call 场景），补一个空文本 block
  if (thinkingIndex === -1 && textIndex === -1 && Object.keys(toolCallBuffers).length === 0) {
    send('content_block_start', { type: 'content_block_start', index: nextBlockIndex, content_block: { type: 'text', text: '' } })
    send('content_block_stop',  { type: 'content_block_stop',  index: nextBlockIndex })
    nextBlockIndex++
  }

  // 如果有 tool calls，追加 tool_use content blocks
  const toolEntries = Object.entries(toolCallBuffers)
  if (toolEntries.length > 0) {
    let blockIndex = nextBlockIndex
    for (const [, buf] of toolEntries) {
      send('content_block_start', {
        type:          'content_block_start',
        index:         blockIndex,
        content_block: { type: 'tool_use', id: buf.id, name: buf.name, input: {} },
      })
      send('content_block_delta', {
        type:  'content_block_delta',
        index: blockIndex,
        delta: { type: 'input_json_delta', partial_json: buf.arguments },
      })
      send('content_block_stop', { type: 'content_block_stop', index: blockIndex })
      blockIndex++
    }
    finishReason = 'tool_use'
  }

  send('message_delta', {
    type:  'message_delta',
    delta: { stop_reason: finishReason, stop_sequence: null },
    usage: { 
      output_tokens: outputTokens,
      // 把最后的 inputTokens 在 message_delta 里发给 SDK
      input_tokens: inputTokens,
      cache_read_input_tokens: cacheReadTokens,
      cache_creation_input_tokens: 0
    },
  })

  send('message_stop', { type: 'message_stop' })
  res.end()
  return {
    input_tokens: inputTokens,
    output_tokens: outputTokens,
    cache_read_input_tokens: cacheReadTokens,
    cache_creation_input_tokens: 0,
  }
}

// ─── Express 应用 ────────────────────────────────────────────────────────────

const app = express()
app.use(express.json({ limit: '10mb', type: ['application/json', 'application/*+json'] }))

// 健康检查
app.get('/', (_req, res) => res.json({ status: 'ok' }))
app.head('/', (_req, res) => res.status(200).end())
app.get('/health', (_req, res) => res.json({ status: 'ok', uptime: process.uptime() }))

function extractIncomingApiKey(req) {
  const xApiKey = String(req.get('x-api-key') ?? '').trim()
  if (xApiKey) return xApiKey

  const auth = String(req.get('authorization') ?? '').trim()
  const match = auth.match(/^(Bearer|x-api-key)\s+(.+)$/i)
  return match ? match[2].trim() : ''
}

function maskKey(key) {
  if (!key) return ''
  if (key.length <= 8) return `${key.slice(0, 1)}***${key.slice(-1)}`
  return `${key.slice(0, 4)}***${key.slice(-4)}`
}

function requireAnthropicApiKey(req, res, next) {
  const incomingKey = extractIncomingApiKey(req)
  if (!incomingKey || (!API_KEY_DIRECTY && incomingKey !== CLIENT_API_KEY)) {
    log.warn(`Invalid API key: ${maskKey(incomingKey)}`)
    return res.status(401).json({
      type: 'error',
      error: {
        type: 'authentication_error',
        message: 'Invalid API key',
      },
    })
  }
  req.clientApiKey = incomingKey
  next()
}

function getUpstreamApiKey(req) {
  return API_KEY_DIRECTY ? req.clientApiKey : OPENAI_API_KEY
}

function buildOpenAIHeaders(req, body) {
  const headers = {
    'Authorization': `Bearer ${getUpstreamApiKey(req)}`,
  }

  for (const name of ['accept', 'content-type', 'openai-beta', 'openai-organization', 'openai-project']) {
    const value = req.get(name)
    if (value) headers[name] = value
  }

  if (typeof body === 'string' && !headers['content-type']) {
    headers['content-type'] = 'application/json'
  }
  return headers
}

function getOpenAIBody(req) {
  if (req.method === 'GET' || req.method === 'HEAD') return undefined
  if (req.is('application/json') && req.body != null) return JSON.stringify(req.body)
  return req
}

async function sendOpenAIRequest({ path, req, body, label = 'OpenAI upstream', method = req.method }) {
  const upstreamUrl = `${OPENAI_API_BASE}${path}`
  const maxRetries = parseInt(process.env.UPSTREAM_RETRIES ?? '2', 10)
  const baseDelay = parseInt(process.env.UPSTREAM_RETRY_DELAY_MS ?? '300', 10)
  let upstream, attempt = 0

  while (true) {
    try {
      upstream = await fetch(upstreamUrl, {
        method,
        headers: buildOpenAIHeaders(req, body),
        body,
        agent: upstreamAgent(upstreamUrl),
      })
    } catch (fetchErr) {
      if (body === req) throw fetchErr
      if (attempt < maxRetries) {
        const delay = baseDelay * 2 ** attempt
        log.warn(`${label} network error (attempt ${attempt + 1}/${maxRetries + 1}), retry in ${delay}ms:`, fetchErr.message)
        await new Promise(r => setTimeout(r, delay))
        attempt++
        continue
      }
      throw fetchErr
    }

    if (body === req) return upstream

    if (!upstream.ok && upstream.status >= 500 && attempt < maxRetries) {
      const errText = await upstream.text()
      const delay = baseDelay * 2 ** attempt
      log.warn(`${label} ${upstream.status} (attempt ${attempt + 1}/${maxRetries + 1}), retry in ${delay}ms: ${errText}`)
      await new Promise(r => setTimeout(r, delay))
      attempt++
      continue
    }
    return upstream
  }
}

function setResponseHeaders(upstream, res) {
  for (const [name, value] of upstream.headers.entries()) {
    if (!['connection', 'transfer-encoding', 'content-encoding', 'content-length'].includes(name.toLowerCase())) {
      res.setHeader(name, value)
    }
  }
}

async function pipeUpstreamResponse(upstream, req, res, startTime) {
  res.status(upstream.status)
  setResponseHeaders(upstream, res)

  const contentType = upstream.headers.get('content-type') || ''
  const isSse = contentType.includes('text/event-stream')
  if (isSse && upstream.ok) {
    upstream.body.on('end', () => log.info(`← OpenAI ${upstream.status} ${req.method} ${req.originalUrl} | stream | ${Date.now() - startTime}ms`))
    upstream.body.on('error', err => log.error('OpenAI stream error:', err))
    return upstream.body.pipe(res)
  }

  if (contentType.includes('application/json')) {
    const text = await upstream.text()
    if (!upstream.ok) {
      log.error(`OpenAI upstream ${upstream.status} ${req.method} ${req.originalUrl}:`, text)
      return res.send(text)
    }

    try {
      const data = JSON.parse(text)
      const usage = data.usage ? ` | ${formatTokenUsage(convertUsage(data.usage))}` : ''
      log.info(`← OpenAI ${upstream.status} ${req.method} ${req.originalUrl}${usage} | ${Date.now() - startTime}ms`)
    } catch {
      log.info(`← OpenAI ${upstream.status} ${req.method} ${req.originalUrl} | ${Date.now() - startTime}ms`)
    }
    return res.send(text)
  }

  upstream.body.on('end', () => log.info(`← OpenAI ${upstream.status} ${req.method} ${req.originalUrl} | ${Date.now() - startTime}ms`))
  upstream.body.on('error', err => log.error('OpenAI passthrough stream error:', err))
  return upstream.body.pipe(res)
}

// 主路由：接收 Anthropic /v1/messages
async function handleMessages(req, res) {
  const startTime = Date.now()

  try {
    // 1. 解析请求
    const {
      model,
      messages,
      system,
      max_tokens,
      temperature,
      top_p,
      // Fix #4: top_k 是 Anthropic 特有参数，OpenAI 不支持，直接忽略
      // eslint-disable-next-line no-unused-vars
      top_k,
      stop_sequences,
      stream = false,
      tools,
      tool_choice,
      metadata,
      // Fix #1: thinking / extended_thinking 是 Claude 特有参数，OpenAI 没有对应项
      // 直接解构并丢弃，不传给上游，否则 OpenAI 会报 unknown field 错误
      // eslint-disable-next-line no-unused-vars
      thinking,
      // eslint-disable-next-line no-unused-vars
      extended_thinking,
    } = req.body

    const mappedModel = mapModel(model)
    const modelLog = mappedModel !== model ? `${model} → ${mappedModel}` : (mappedModel ?? '(no model)')
    log.info(`→ Anthropic POST /v1/messages | model=${modelLog} | stream=${stream} | tools=${tools?.length ?? 0}`)
    if (thinking || extended_thinking) {
      log.debug('thinking / extended_thinking 参数已忽略（OpenAI 不支持）')
    }
    if (top_k != null) {
      log.warn('top_k 参数已忽略（OpenAI 不支持）')
    }
    log.debug('Request body:', JSON.stringify(req.body, null, 2))

    // 2. 转换成 OpenAI 格式
    const openaiBody = {
      model:       mappedModel,
      messages:    convertMessages(messages, system, mappedModel),
      max_tokens:  max_tokens  ?? 4096,
      stream,
    }

    if (temperature    != null) openaiBody.temperature = temperature
    if (top_p          != null) openaiBody.top_p       = top_p
    if (stop_sequences?.length) openaiBody.stop        = stop_sequences

    // Fix #7: metadata.user_id → OpenAI user 字段（用于滥用追踪）
    if (metadata?.user_id) openaiBody.user = String(metadata.user_id)

    const convertedTools = convertTools(tools)
    if (convertedTools) {
      openaiBody.tools       = convertedTools
      openaiBody.tool_choice = convertToolChoice(tool_choice) ?? 'auto'
    }

    // 开启流式时请求 usage
    if (stream) openaiBody.stream_options = { include_usage: true }

    log.debug('→ OpenAI body:', JSON.stringify(openaiBody, null, 2))

    // 3. 转发到上游
    const upstream = await sendOpenAIRequest({ path: '/chat/completions', req, body: JSON.stringify(openaiBody), label: 'OpenAI upstream', method: 'POST' })

    if (!upstream.ok) {
      const errText = await upstream.text()
      log.error(`Upstream ${upstream.status}:`, errText)
      return res.status(upstream.status).json({
        type: 'error',
        error: { type: 'api_error', message: `Upstream error ${upstream.status}: ${errText}` },
      })
    }

    const requestId = upstream.headers.get('x-request-id') || `msg_${Date.now()}`

    // 4. 非流式：直接转换响应
    if (!stream) {
      const data        = await upstream.json()
      const anthropicResp = convertResponse(data, requestId)
      log.info(`← Anthropic ${anthropicResp.stop_reason} | ${formatTokenUsage(anthropicResp.usage)} | ${Date.now() - startTime}ms`)
      log.debug('← Response:', JSON.stringify(anthropicResp, null, 2))
      return res.json(anthropicResp)
    }

    // 5. 流式：转换 SSE
    res.setHeader('Content-Type',  'text/event-stream')
    res.setHeader('Cache-Control', 'no-cache')
    res.setHeader('Connection',    'keep-alive')
    const streamUsage = await streamConvert(upstream.body, res, requestId)
    log.info(`← Anthropic stream done | ${formatTokenUsage(streamUsage)} | ${Date.now() - startTime}ms`)

  } catch (err) {
    log.error('Proxy error:', err)
    if (!res.headersSent) {
      res.status(500).json({
        type:  'error',
        error: { type: 'api_error', message: err.message },
      })
    } else {
      res.end()
    }
  }
}

async function handleOpenAIPassthrough(req, res) {
  const startTime = Date.now()
  const body = getOpenAIBody(req)
  const pathWithQuery = req.originalUrl.replace(/^\/v1/, '') || '/'
  const stream = Boolean(req.body?.stream)
  const model = req.body?.model ?? '(no model)'

  log.info(`→ OpenAI ${req.method} ${req.originalUrl} | model=${model} | stream=${stream}`)
  if (req.is('application/json')) {
    log.debug('→ OpenAI passthrough body:', JSON.stringify(req.body ?? {}, null, 2))
  }

  try {
    const upstream = await sendOpenAIRequest({ path: pathWithQuery, req, body, label: 'OpenAI upstream' })
    return pipeUpstreamResponse(upstream, req, res, startTime)
  } catch (err) {
    log.error('OpenAI passthrough proxy error:', err)
    if (!res.headersSent) {
      res.status(500).json({ error: { message: err.message, type: 'api_error' } })
    } else {
      res.end()
    }
  }
}

function anthropicNotSupported(message) {
  return (_req, res) => {
    res.status(400).json({
      type: 'error',
      error: { type: 'not_supported', message },
    })
  }
}

// Anthropic 专属路由
app.post('/v1/messages', requireAnthropicApiKey, handleMessages)
app.post('/messages', requireAnthropicApiKey, handleMessages)

app.post(['/v1/messages/count_tokens', '/messages/count_tokens'], requireAnthropicApiKey, (req, res) => {
  const { messages, system, tools, tool_choice } = req.body ?? {}
  res.json({
    input_tokens: estimateTokens({ messages, system, tools, tool_choice }),
  })
})

const messageBatchesNotSupported = anthropicNotSupported('Anthropic Message Batches are not supported by this OpenAI-backed proxy.')
app.all('/v1/messages/batches', requireAnthropicApiKey, messageBatchesNotSupported)
app.all('/v1/messages/batches/:batch_id', requireAnthropicApiKey, messageBatchesNotSupported)
app.all('/v1/messages/batches/:batch_id/cancel', requireAnthropicApiKey, messageBatchesNotSupported)
app.all('/v1/messages/batches/:batch_id/results', requireAnthropicApiKey, messageBatchesNotSupported)

// 兼容 /v1/complete（旧版 Anthropic API，直接 400 提示）
app.post('/v1/complete', requireAnthropicApiKey, anthropicNotSupported('Legacy /v1/complete is not supported. Use /v1/messages.'))

// OpenAI 原生协议透传
app.all('/v1/*', requireAnthropicApiKey, handleOpenAIPassthrough)
app.all(['/chat/completions', '/models'], requireAnthropicApiKey, handleOpenAIPassthrough)

// 404 fallback
app.use((req, res) => {
  log.warn(`404: ${req.method} ${req.path}`)
  res.status(404).json({ error: 'Not found' })
})

// ─── 启动 ────────────────────────────────────────────────────────────────────

if (!API_KEY_DIRECTY && !OPENAI_API_KEY) {
  log.error('请设置环境变量 OPENAI_API_KEY，或设置 API_KEY_DIRECTY=true 直接透传客户端 API key')
  process.exit(1)
}
if (!API_KEY_DIRECTY && !CLIENT_API_KEY) {
  log.error('请设置环境变量 CLIENT_API_KEY（入站鉴权），或设置 API_KEY_DIRECTY=true 直接透传客户端 API key')
  process.exit(1)
}

app.listen(PORT, HOST, () => {
  console.log(`
══════════════════════════════════════════════════════
                 llm-proxy-lite ✅
══════════════════════════════════════════════════════
`)
})
