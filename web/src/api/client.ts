import type { APIErrorBody } from './types'

const apiPrefix = '/api/v1'

// APIError 携带后端统一错误结构中的 code 与面向用户的 message。
export class APIError extends Error {
  status: number
  code: string

  constructor(status: number, body?: APIErrorBody) {
    super(body?.message ?? `请求失败（HTTP ${status}）`)
    this.name = 'APIError'
    this.status = status
    this.code = body?.code ?? 'http_error'
  }
}

function request(path: string, init?: RequestInit): Promise<Response> {
  return fetch(apiPrefix + path, {
    ...init,
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json', ...init?.headers },
  })
}

async function bootstrap(): Promise<void> {
  await fetch(`${apiPrefix}/bootstrap`, { credentials: 'same-origin' })
}

async function errorFromResponse(response: Response): Promise<APIError> {
  let body: APIErrorBody | undefined
  try {
    body = (await response.json()) as APIErrorBody
  } catch {
    body = undefined
  }
  return new APIError(response.status, body)
}

async function parse<T>(response: Response): Promise<T> {
  if (!response.ok) {
    throw await errorFromResponse(response)
  }
  if (response.status === 204) {
    return undefined as T
  }
  return (await response.json()) as T
}

// apiFetch 调用 /api/v1 下的接口；首次 401 时通过 bootstrap 重新种 Cookie，
// 并且只重试一次。
export async function apiFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await request(path, init)
  if (response.status === 401) {
    await bootstrap()
    return parse<T>(await request(path, init))
  }
  return parse<T>(response)
}

// apiRawFetch 返回原始 Response（供文件预览读取 Content-Type 与文本），
// 与 apiFetch 相同的 401 bootstrap 重试；错误仍抛出统一 APIError。
export async function apiRawFetch(path: string): Promise<Response> {
  let response = await request(path)
  if (response.status === 401) {
    await bootstrap()
    response = await request(path)
  }
  if (!response.ok) {
    throw await errorFromResponse(response)
  }
  return response
}
