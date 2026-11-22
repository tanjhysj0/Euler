// 模拟 OpenAI 兼容供应商，仅供 E2E 使用，不访问真实付费模型。
// 响应协议与真实 OpenAI Chat Completions SSE 一致（chat.completion.chunk + [DONE]）。
import http from 'node:http'

const host = '127.0.0.1'
const port = 18081
const slowIntervalMs = 50

function sseChunk(delta, finishReason = null) {
  const chunk = {
    id: 'chatcmpl-e2e',
    object: 'chat.completion.chunk',
    created: 1700000000,
    model: 'euler-e2e',
    choices: [{ index: 0, delta, finish_reason: finishReason }],
  }
  return `data: ${JSON.stringify(chunk)}\n\n`
}

function lastUserContent(body) {
  const messages = Array.isArray(body.messages) ? body.messages : []
  for (let i = messages.length - 1; i >= 0; i -= 1) {
    if (messages[i].role === 'user') {
      return String(messages[i].content ?? '')
    }
  }
  return ''
}

function writeSseHead(res) {
  res.writeHead(200, {
    'Content-Type': 'text/event-stream',
    'Cache-Control': 'no-cache',
    Connection: 'keep-alive',
  })
}

// 用户消息为「持续输出」时按固定间隔无限发送文本 chunk，直到客户端断流。
// 序号 4 位补零且带闭括号，避免前缀碰撞干扰去重断言。
function streamSlow(res) {
  res.write(sseChunk({ role: 'assistant' }))
  let index = 0
  const timer = setInterval(() => {
    // 请求体读完后 req 即 destroyed，只能依据响应/连接状态判断断流。
    if (res.destroyed || res.writableEnded) {
      clearInterval(timer)
      return
    }
    index += 1
    res.write(sseChunk({ content: `片段[${String(index).padStart(4, '0')}] ` }))
  }, slowIntervalMs)
  // 注意：IncomingMessage 的 close 在请求体读完时就会触发，
  // 必须监听响应/连接的 close 才能感知客户端断流。
  res.on('close', () => clearInterval(timer))
  res.on('error', () => clearInterval(timer))
}

// 直接返回一段完整文本并正常结束。
function streamText(res, text) {
  res.write(sseChunk({ role: 'assistant' }))
  res.write(sseChunk({ content: text }))
  res.write(sseChunk({}, 'stop'))
  res.end('data: [DONE]\n\n')
}

// 先流式返回思考过程（reasoning_content），再返回正文；用于验证思考 UI。
function streamReasoning(res, reasoning, text) {
  res.write(sseChunk({ role: 'assistant' }))
  res.write(sseChunk({ reasoning_content: reasoning }))
  res.write(sseChunk({ content: text }))
  res.write(sseChunk({}, 'stop'))
  res.end('data: [DONE]\n\n')
}

// 首轮请求：返回一个或多个工具调用，交给 registry 执行。
function streamToolCalls(res, calls) {
  res.write(sseChunk({ role: 'assistant' }))
  res.write(
    sseChunk({
      tool_calls: calls.map((call, index) => ({
        index,
        id: call.id,
        type: 'function',
        function: {
          name: call.name,
          arguments: JSON.stringify(call.arguments),
        },
      })),
    }),
  )
  res.write(sseChunk({}, 'tool_calls'))
  res.end('data: [DONE]\n\n')
}

function fileWrite(path, content, id) {
  return { id, name: 'file_write', arguments: { path, content } }
}

// 工具结果回传后的第二轮请求：按用户关键词返回收尾文本。
function finalTextFor(user) {
  if (user.includes('读取缺失文件')) {
    return '读取失败，任务结束。'
  }
  if (user.includes('创建多个文件')) {
    return '已创建两个文件。'
  }
  if (user.includes('创建嵌套文件')) {
    return '已创建 `src/main.go`。'
  }
  return '已创建 `note.txt`，可以在右侧预览。'
}

function sendServerError(res) {
  res.writeHead(500, { 'Content-Type': 'application/json' })
  res.end(
    JSON.stringify({
      error: { message: '模拟上游错误', type: 'server_error' },
    }),
  )
}

// 模拟 DeepSeek 对 assistant 消息的校验：content、tool_calls、reasoning_content
// 至少有一个非空，否则返回 400。用于防止空助手消息再次进入历史。
function hasEmptyAssistant(body) {
  const messages = Array.isArray(body.messages) ? body.messages : []
  return messages.some((message) => {
    if (message.role !== 'assistant') {
      return false
    }
    const content = typeof message.content === 'string' ? message.content.trim() : ''
    const toolCalls = Array.isArray(message.tool_calls) ? message.tool_calls : []
    const reasoning =
      typeof message.reasoning_content === 'string' ? message.reasoning_content.trim() : ''
    return content === '' && toolCalls.length === 0 && reasoning === ''
  })
}

function sendInvalidAssistant(res) {
  res.writeHead(400, { 'Content-Type': 'application/json' })
  res.end(
    JSON.stringify({
      error: {
        message: 'Invalid assistant message: content, tool_calls or reasoning_content must be set',
        type: 'invalid_request_error',
      },
    }),
  )
}

function handleCompletions(req, res, raw) {
  const body = JSON.parse(raw)
  const user = lastUserContent(body)
  const hasToolResult = raw.includes('"role":"tool"')

  // 上游协议校验必须在写 SSE 头之前返回；带空助手消息的请求直接 400。
  if (hasEmptyAssistant(body)) {
    sendInvalidAssistant(res)
    return
  }

  // 错误路径必须在写 SSE 头之前返回，否则客户端会把 500 当成流读取。
  if (user.includes('触发模型错误') && !hasToolResult) {
    sendServerError(res)
    return
  }

  writeSseHead(res)
  if (user.includes('持续输出')) {
    streamSlow(res)
    return
  }
  if (hasToolResult) {
    streamText(res, finalTextFor(user))
    return
  }
  if (user.includes('提交空总结')) {
    // 只调用 submit，不输出正文；用于复现“正文为空的助手消息”。
    streamToolCalls(res, [
      { id: 'call_e2e_submit', name: 'submit', arguments: { summary: '空总结收尾' } },
    ])
    return
  }
  if (user.includes('读取缺失文件')) {
    streamToolCalls(res, [
      { id: 'call_e2e_missing', name: 'file_read', arguments: { path: 'missing.txt' } },
    ])
    return
  }
  if (user.includes('创建多个文件')) {
    streamToolCalls(res, [
      fileWrite('a.txt', '# Alpha\n\nalpha-content', 'call_e2e_a'),
      fileWrite('b.txt', '# Beta\n\nbeta-content', 'call_e2e_b'),
    ])
    return
  }
  if (user.includes('创建嵌套文件')) {
    streamToolCalls(res, [
      fileWrite('src/main.go', 'package main\n\nfunc main() {}\n', 'call_e2e_nested'),
    ])
    return
  }
  if (user.includes('快速完成')) {
    streamText(res, '快速完成。')
    return
  }
  if (user.includes('展示思考')) {
    streamReasoning(res, '先分析任务，再给出结论。', '思考完成，结论如下。')
    return
  }
  // 默认场景：写入 note.txt 并等待工具结果回传。
  streamToolCalls(res, [
    fileWrite('note.txt', '# Euler 验收\n\n产物预览成功。', 'call_e2e_1'),
  ])
}

const server = http.createServer((req, res) => {
  if (req.method === 'GET' && req.url === '/healthz') {
    res.writeHead(200)
    res.end('ok')
    return
  }
  if (req.method === 'GET' && req.url.endsWith('/models')) {
    res.writeHead(200, { 'Content-Type': 'application/json' })
    res.end(
      JSON.stringify({
        object: 'list',
        data: [{ id: 'euler-e2e', object: 'model', owned_by: 'e2e' }],
      }),
    )
    return
  }
  if (req.method === 'POST' && req.url.endsWith('/chat/completions')) {
    let raw = ''
    req.on('data', (part) => {
      raw += part
    })
    req.on('end', () => handleCompletions(req, res, raw))
    return
  }
  res.writeHead(404)
  res.end()
})

server.listen(port, host, () => {
  console.log(`mock openai listening on http://${host}:${port}`)
})
