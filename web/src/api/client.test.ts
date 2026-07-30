import { afterEach, describe, expect, it, vi } from 'vitest'
import { newRequestID, request } from './client'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('API client', () => {
  it('uses a relative fnOS gateway URL and request ID', async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(JSON.stringify({ items: [] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    vi.stubGlobal('fetch', fetchMock)
    await request('/apps')
    expect(fetchMock).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0]
    expect(url).toBe('./api/apps')
    expect(init.credentials).toBe('same-origin')
    expect(init.headers['X-Request-ID']).toBeTruthy()
  })

  it('preserves stable error details and suggestions', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            code: 'TARGET_UNAVAILABLE',
            message: 'Target is unavailable.',
            suggestion: 'Publish a stable port.',
            requestId: 'request-1',
          }),
          { status: 422, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    )
    await expect(request('/apps', { method: 'POST', body: '{}' })).rejects.toMatchObject({
      code: 'TARGET_UNAVAILABLE',
      suggestion: 'Publish a stable port.',
      requestID: 'request-1',
    })
  })

  it('surfaces field validation details', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        new Response(
          JSON.stringify({
            code: 'VALIDATION_FAILED',
            message: 'One or more fields are invalid.',
            fields: [{ field: 'iconUri', message: 'icon URI returned HTTP 404' }],
          }),
          { status: 422, headers: { 'Content-Type': 'application/json' } },
        ),
      ),
    )
    await expect(request('/apps', { method: 'POST', body: '{}' })).rejects.toMatchObject({
      message: 'iconUri: icon URI returned HTTP 404',
      code: 'VALIDATION_FAILED',
    })
  })

  it('uses provided crypto for deterministic request IDs', () => {
    const fixed = '00000000-0000-4000-8000-000000000000'
    expect(newRequestID({ randomUUID: () => fixed })).toBe(fixed)
  })
})
