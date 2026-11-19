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

// 携带 tool role 的第二轮请求：分块返回最终文本并正常结束。
function streamFinalText(res) {
  res.write(sseChunk({ role: 'assistant', content: '已创建 ' }))
  res.write(sseChunk({ content: '`note.txt`' }))
  res.write(sseChunk({ content: '，可以在右侧预览。' }))
  res.write(sseChunk({}, 'stop'))
  res.end('data: [DONE]\n\n')
}

// 首轮请求：返回 file_write ToolCall，等待人工审批。
function streamFileWriteToolCall(res) {
  res.write(sseChunk({ role: 'assistant' }))
  res.write(
    sseChunk({
      tool_calls: [
        {
          index: 0,
          id: 'call_e2e_1',
          type: 'function',
          function: {
            name: 'file_write',
            arguments: JSON.stringify({
              path: 'note.txt',
              content: '# Euler 验收\n\n产物预览成功。',
            }),
          },
        },
      ],
    }),
  )
  res.write(sseChunk({}, 'tool_calls'))
  res.end('data: [DONE]\n\n')
}

function handleCompletions(req, res, raw) {
  res.writeHead(200, {
    'Content-Type': 'text/event-stream',
    'Cache-Control': 'no-cache',
    Connection: 'keep-alive',
  })
  const body = JSON.parse(raw)
  if (lastUserContent(body).includes('持续输出')) {
    streamSlow(res)
    return
  }
  if (raw.includes('"role":"tool"')) {
    streamFinalText(res)
    return
  }
  streamFileWriteToolCall(res)
}

const server = http.createServer((req, res) => {
  if (req.method === 'GET' && req.url === '/healthz') {
    res.writeHead(200)
    res.end('ok')
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
