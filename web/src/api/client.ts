export type AppSpec = {
  id: string
  appName: string
  displayName: string
  description?: string
  iconPath?: string
  origin?: AppOrigin
  openType: 'iframe' | 'url'
  protocol: 'http' | 'https'
  port: number
  path: string
  allUsers: boolean
  revision: number
}

export type AppOrigin = {
  source: 'manual' | 'docker' | 'host'
  sourceDetail?: string
  description?: string
  networkMode?: string
  pid?: number
  watchCow?: boolean
}

export type AppStatus = {
  registration: 'installed' | 'missing' | 'unknown'
  target: 'available' | 'unavailable'
  lastError?: string
}

export type AppView = AppSpec & {
  status: AppStatus
  iconDataUrl?: string
}

export type AppInput = {
  displayName: string
  description: string
  entryPrefix?: string
  iconBase64?: string
  iconUri?: string
  origin?: AppOrigin
  openType: 'iframe' | 'url'
  protocol: 'http' | 'https'
  port: number
  path: string
  allUsers: boolean
}

export type DiscoveryCandidate = {
  key: string
  displayName: string
  description?: string
  protocol: 'http' | 'https'
  port: number
  path: string
  iconUri?: string
  source: 'docker' | 'host'
  sourceDetail?: string
  address?: string
  groupKey?: string
  containerId?: string
  networkMode?: string
  ownerConfidence?: 'high' | 'medium'
  pid?: number
  preferred: boolean
  watchCow: boolean
  existingApplication?: string
  registrationSuggestion: 'available' | 'already-registered' | 'existing-fnos-application'
}

export type Diagnostics = {
  logs: Array<{ name: string; text: string; present: boolean }>
  reports: Array<{ name: string; text: string; present: boolean }>
}

export type OperationResult = {
  app: AppView
  code: 'CREATED' | 'UPDATED' | 'REPAIRED' | 'ROLLED_BACK'
}

type Problem = {
  code?: string
  message?: string
  suggestion?: string
  requestId?: string
  fields?: Array<{ field: string; message: string }>
}

export class APIError extends Error {
  constructor(
    message: string,
    public status: number,
    public code: string,
    public suggestion = '',
    public requestID = '',
  ) {
    super(message)
  }
}

export function newRequestID(source: Pick<Crypto, 'randomUUID'> | undefined = globalThis.crypto) {
  return source?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`
}

export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(`./api${path}`, {
    credentials: 'same-origin',
    ...init,
    headers: {
      ...(init.body ? { 'Content-Type': 'application/json' } : {}),
      'X-Request-ID': newRequestID(),
      ...init.headers,
    },
  })
  if (!response.ok) {
    const problem = (await response.json().catch(() => ({}))) as Problem
    const fieldMessage = problem.fields
      ?.map((field) => `${field.field}: ${field.message}`)
      .join('；')
    throw new APIError(
      fieldMessage || problem.message || response.statusText || '请求失败',
      response.status,
      problem.code || 'INTERNAL_ERROR',
      problem.suggestion,
      problem.requestId,
    )
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}
