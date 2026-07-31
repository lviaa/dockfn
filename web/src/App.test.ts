import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { createApp, nextTick, type App as VueApp } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import App from './App.vue'

const styles = readFileSync(resolve(process.cwd(), 'src/styles.css'), 'utf8')

const application = {
  id: '012345abcdef',
  appName: 'photos.dkfn',
  displayName: '家庭相册',
  openType: 'iframe' as const,
  protocol: 'http',
  port: 8080,
  path: '/photos',
  allUsers: true,
  revision: 1,
  origin: {
    source: 'docker' as const,
    sourceDetail: 'photos',
    description: 'ghcr.io/example/photos:latest',
    networkMode: '1panel-network',
    watchCow: true,
  },
  status: { registration: 'installed', target: 'available' },
}

const candidate = {
  key: 'docker:photo:8080',
  displayName: 'Docker 相册',
  description: '来自 WatchCow 标签',
  protocol: 'https',
  port: 8443,
  path: '/photos',
  source: 'docker' as const,
  sourceDetail: 'photos:latest',
  groupKey: 'docker:photo',
  networkMode: '1panel-network',
  preferred: true,
  watchCow: true,
  iconUri: '/favicon.png',
  registrationSuggestion: 'available' as const,
}

let mounted: VueApp<Element> | undefined

beforeEach(() => {
  window.localStorage.clear()
})

afterEach(() => {
  vi.useRealTimers()
  mounted?.unmount()
  mounted = undefined
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

async function mountPage() {
  const container = document.createElement('div')
  document.body.append(container)
  mounted = createApp(App)
  mounted.mount(container)
  await Promise.resolve()
  await nextTick()
  await Promise.resolve()
  await nextTick()
  return container
}

function response(value: unknown) {
  const body = Array.isArray(value) ? { items: value } : value
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('DockFN single page', () => {
  it('renders registered applications as the primary view', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response([application])))
    const page = await mountPage()
    await vi.waitFor(() => expect(page.textContent).toContain('家庭相册'))
    expect(page.textContent).toContain('DockFN')
    expect(page.textContent).toContain('把已有 Web 服务接入 fnOS 桌面')
    expect(page.textContent).toContain('已注册应用 1')
    expect(
      [...page.querySelectorAll('.app-origin-tag')].map((item) => item.textContent?.trim()),
    ).toEqual(['Docker', 'photos', '1panel-network', 'WatchCow', 'ghcr.io/example/photos:latest'])
    const logo = page.querySelector<HTMLImageElement>('img.brand-mark')
    expect(logo?.alt).toBe('DockFN')
    expect(logo?.getAttribute('src')).toContain('dockfn-logo')
    expect(page.textContent).toContain('8080')
    expect(page.querySelector('main')).not.toBeNull()
  })

  it('uses discovery first, allows manual entry, then pre-fills the review form from a candidate', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([candidate]))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ dataUrl: 'data:image/png;base64,cHJldmlldw==' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await nextTick()
    const dialog = page.querySelector('[role="dialog"]')
    expect(dialog?.textContent).toContain('从本机发现 Web 服务')
    expect(dialog?.querySelector('input[placeholder="例如：家庭相册"]')).toBeNull()

    await vi.waitFor(() => expect(page.textContent).toContain('Docker 相册'))
    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('/discovery/scan')
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(page.querySelector('.discovery-copy .primary-button')?.textContent).toContain('重新扫描')
    expect(
      [...page.querySelectorAll('.discovery-copy-actions button')].map((item) =>
        item.textContent?.trim(),
      ),
    ).toEqual(['手动填写', '重新扫描'])
    expect(page.querySelector('.discovery-step > .dialog-actions')).toBeNull()
    expect(page.querySelector('.candidate-source')?.textContent).toContain('Docker')
    expect(page.querySelector('.discovery-filters')).toBeNull()
    expect(page.querySelector('.discovery-result-count')?.textContent).toContain('共 1 个 Web 端口')
    expect(page.querySelector('.candidate-source-heading')?.getAttribute('aria-expanded')).toBe(
      'true',
    )
    expect(
      [...page.querySelectorAll('.candidate-group-tag')].map((item) => item.textContent?.trim()),
    ).toEqual(['photos:latest', '1panel-network'])
    expect(page.querySelector('.candidate-group > header')?.textContent).not.toContain('Docker ·')
    expect(page.querySelector('.candidate-card img')).toBeNull()
    page.querySelector<HTMLButtonElement>('.candidate-card')?.click()
    await nextTick()
    expect(page.querySelector<HTMLInputElement>('input[placeholder="例如：家庭相册"]')?.value).toBe(
      'Docker 相册',
    )
    expect(page.querySelector<HTMLInputElement>('input[type="number"]')?.value).toBe('8443')
    expect(
      page.querySelector<HTMLInputElement>('input[placeholder*="留空则使用显示名称"]')?.value,
    ).toBe('')
    await vi.waitFor(() =>
      expect(page.querySelector<HTMLImageElement>('img[alt="应用图标预览"]')?.src).toContain(
        'data:image/png;base64,cHJldmlldw==',
      ),
    )
    expect(page.querySelector<HTMLInputElement>('input[placeholder*="/favicon.ico"]')?.value).toBe(
      '/favicon.png',
    )
    expect(page.textContent).not.toContain('已采用“Docker 相册”的候选值')
    const urlMode = page.querySelector<HTMLButtonElement>(
      '.segmented-control [role="radio"][data-value="url"]',
    )
    const iframeMode = page.querySelector<HTMLButtonElement>(
      '.segmented-control [role="radio"][data-value="iframe"]',
    )
    expect(urlMode?.getAttribute('aria-checked')).toBe('true')
    iframeMode?.click()
    await nextTick()
    expect(iframeMode?.getAttribute('aria-checked')).toBe('true')
    urlMode?.click()
    await nextTick()
    expect(urlMode?.getAttribute('aria-checked')).toBe('true')

    page.querySelector<HTMLButtonElement>('.secondary-button')?.click()
    await nextTick()
    page.querySelector<HTMLButtonElement>('.discovery-copy-actions .secondary-button')?.click()
    await nextTick()
    expect(page.querySelector<HTMLInputElement>('input[placeholder="例如：家庭相册"]')?.value).toBe(
      '',
    )
  })

  it('persists ignored discovery services across scans', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([candidate]))
      .mockResolvedValueOnce(response({ keys: [candidate.key] }))
      .mockResolvedValueOnce(response([candidate]))
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await vi.waitFor(() => expect(page.textContent).toContain('Docker 相册'))

    page.querySelector<HTMLButtonElement>('.candidate-ignore')?.click()
    expect(window.localStorage.getItem('dockfn.discovery.ignored.v1')).toContain(candidate.key)
    expect(String(fetchMock.mock.calls[2]?.[0])).toContain('/discovery/ignored')
    expect(fetchMock.mock.calls[2]?.[1]).toMatchObject({
      method: 'PUT',
      body: JSON.stringify({ keys: [candidate.key] }),
    })

    page.querySelector<HTMLButtonElement>('.discovery-copy .primary-button')?.click()
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(4))
    const ignoredHeading = [
      ...page.querySelectorAll<HTMLButtonElement>('.candidate-source-heading'),
    ].find((item) => item.textContent?.includes('已忽略'))
    ignoredHeading?.click()
    await vi.waitFor(() =>
      expect(page.querySelector<HTMLButtonElement>('.candidate-card')?.disabled).toBe(true),
    )
  })

  it('discovers a common favicon only after a candidate is selected', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([{ ...candidate, iconUri: undefined }]))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ message: 'icon not found' }), {
          status: 422,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ dataUrl: 'data:image/png;base64,ZGlzY292ZXJlZA==' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await vi.waitFor(() => expect(page.querySelector('.candidate-card')).not.toBeNull())
    expect(fetchMock).toHaveBeenCalledTimes(2)

    page.querySelector<HTMLButtonElement>('.candidate-card')?.click()
    await vi.waitFor(() =>
      expect(page.querySelector<HTMLImageElement>('img[alt="应用图标预览"]')?.src).toContain(
        'data:image/png;base64,ZGlzY292ZXJlZA==',
      ),
    )
    const firstIconRequest = JSON.parse(String(fetchMock.mock.calls[2]?.[1]?.body))
    const secondIconRequest = JSON.parse(String(fetchMock.mock.calls[3]?.[1]?.body))
    expect(firstIconRequest.iconUri).toBe('/favicon.ico')
    expect(secondIconRequest.iconUri).toBe('/favicon.png')
    expect(page.querySelector<HTMLInputElement>('input[placeholder*="/favicon.ico"]')?.value).toBe(
      '/favicon.png',
    )
  })

  it('discovers a page icon after the access path changes', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            iconUri: '/panel/icon.ico',
            dataUrl: 'data:image/png;base64,cGFuZWw=',
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await Promise.resolve()
    await nextTick()
    page.querySelector<HTMLButtonElement>('.discovery-copy-actions .secondary-button')?.click()
    await nextTick()

    const path = page.querySelector<HTMLInputElement>('input[placeholder="/"]')
    if (!path) throw new Error('access path input was not rendered')
    path.value = '/panel/'
    path.dispatchEvent(new Event('input'))
    await vi.advanceTimersByTimeAsync(500)
    await nextTick()

    expect(String(fetchMock.mock.calls[2]?.[0])).toContain('/icons/discover')
    expect(JSON.parse(String(fetchMock.mock.calls[2]?.[1]?.body))).toEqual({
      protocol: 'http',
      port: 8080,
      path: '/panel/',
    })
    expect(page.querySelector<HTMLInputElement>('input[placeholder*="/favicon.ico"]')?.value).toBe(
      '/panel/icon.ico',
    )
    expect(page.querySelector<HTMLImageElement>('img[alt="应用图标预览"]')?.src).toContain(
      'data:image/png;base64,cGFuZWw=',
    )
  })

  it('does not replace a manually edited icon when the access path changes', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([]))
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await Promise.resolve()
    await nextTick()
    page.querySelector<HTMLButtonElement>('.discovery-copy-actions .secondary-button')?.click()
    await nextTick()

    const icon = page.querySelector<HTMLInputElement>('input[placeholder*="/favicon.ico"]')
    const path = page.querySelector<HTMLInputElement>('input[placeholder="/"]')
    if (!icon || !path) throw new Error('review inputs were not rendered')
    icon.value = '/manual-icon.png'
    icon.dispatchEvent(new Event('input'))
    path.value = '/panel/'
    path.dispatchEvent(new Event('input'))
    await vi.advanceTimersByTimeAsync(500)
    await nextTick()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(icon.value).toBe('/manual-icon.png')
  })

  it('rediscovers the icon when an existing application path is edited', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        response([
          {
            ...application,
            iconDataUrl: 'data:image/png;base64,b2xk',
          },
        ]),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            iconUri: '/lvia/icon.png',
            dataUrl: 'data:image/png;base64,bmV3',
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()
    await Promise.resolve()
    await nextTick()
    page.querySelector<HTMLButtonElement>('button[aria-label="编辑应用"]')?.click()
    await nextTick()

    const path = page.querySelector<HTMLInputElement>('input[placeholder="/"]')
    if (!path) throw new Error('access path input was not rendered')
    path.value = '/lvia'
    path.dispatchEvent(new Event('input'))
    await vi.advanceTimersByTimeAsync(500)
    await nextTick()

    expect(String(fetchMock.mock.calls[1]?.[0])).toContain('/icons/discover')
    expect(page.querySelector<HTMLInputElement>('input[placeholder*="/favicon.ico"]')?.value).toBe(
      '/lvia/icon.png',
    )
    expect(page.textContent).toContain('已按当前路径识别')
  })

  it('reports a missing page icon without discarding the existing icon', async () => {
    vi.useFakeTimers()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(
        response([
          {
            ...application,
            iconDataUrl: 'data:image/png;base64,b2xk',
          },
        ]),
      )
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            code: 'VALIDATION_FAILED',
            message: 'no page or common service icon was found',
          }),
          { status: 422, headers: { 'Content-Type': 'application/json' } },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()
    await Promise.resolve()
    await nextTick()
    page.querySelector<HTMLButtonElement>('button[aria-label="编辑应用"]')?.click()
    await nextTick()

    const path = page.querySelector<HTMLInputElement>('input[placeholder="/"]')
    if (!path) throw new Error('access path input was not rendered')
    path.value = '/lvia'
    path.dispatchEvent(new Event('input'))
    await vi.advanceTimersByTimeAsync(500)
    await nextTick()

    expect(page.textContent).toContain('当前页面未声明可用图标')
    expect(page.querySelector<HTMLImageElement>('img[alt="应用图标预览"]')?.src).toContain(
      'data:image/png;base64,b2xk',
    )
  })

  it('rejects a custom fnOS ID that starts with a number', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValueOnce(response([])).mockResolvedValueOnce(response([])),
    )
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await vi.waitFor(() => expect(page.textContent).toContain('暂无可选服务'))
    page.querySelector<HTMLButtonElement>('.discovery-copy-actions .secondary-button')?.click()
    await nextTick()

    const prefix = page.querySelector<HTMLInputElement>('input[placeholder="留空则自动生成"]')
    if (!prefix) throw new Error('entry prefix input was not rendered')
    prefix.value = '1panel2'
    prefix.dispatchEvent(new Event('input'))
    await nextTick()

    expect(prefix.pattern).toBe('[a-z](?:[a-z0-9-]{0,25}[a-z0-9])?')
    expect(prefix.getAttribute('aria-invalid')).toBe('true')
    expect(page.querySelector<HTMLButtonElement>('button[type="submit"]')?.disabled).toBe(true)
    expect(page.textContent).not.toContain('d1panel2.dkfn')
    expect(page.textContent).not.toContain('数字开头时自动补 d')
  })

  it('retains the selected discovery source as registered application tags', async () => {
    const created = {
      ...application,
      displayName: candidate.displayName,
      origin: {
        source: candidate.source,
        sourceDetail: candidate.sourceDetail,
        description: candidate.description,
        networkMode: candidate.networkMode,
        watchCow: candidate.watchCow,
      },
    }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([candidate]))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ dataUrl: 'data:image/png;base64,cHJldmlldw==' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ app: created, code: 'CREATED' }), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()

    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await vi.waitFor(() => expect(page.querySelector('.candidate-card')).not.toBeNull())
    page.querySelector<HTMLButtonElement>('.candidate-card')?.click()
    await vi.waitFor(() =>
      expect(page.querySelector<HTMLImageElement>('img[alt="应用图标预览"]')?.src).toContain(
        'data:image/png;base64,cHJldmlldw==',
      ),
    )
    page.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()

    await vi.waitFor(() => expect(page.querySelector('.completion-step')).not.toBeNull())
    expect(page.querySelector('[data-step="complete"]')?.classList.contains('active')).toBe(true)
    expect(page.textContent).toContain('应用创建完成')
    const payload = JSON.parse(String(fetchMock.mock.calls[3]?.[1]?.body))
    expect(payload.origin).toEqual({
      source: 'docker',
      sourceDetail: 'photos:latest',
      description: '来自 WatchCow 标签',
      networkMode: '1panel-network',
      watchCow: true,
    })
    expect(
      [...page.querySelectorAll('.app-origin-tag')].map((item) => item.textContent?.trim()),
    ).toEqual(['Docker', 'photos:latest', '1panel-network', 'WatchCow', '来自 WatchCow 标签'])
    page.querySelector<HTMLButtonElement>('[data-completion-action="close"]')?.click()
    await vi.waitFor(() => expect(page.querySelector('.creator-dialog')).toBeNull())
  })

  it('does not let a matched fnOS or DockFN candidate create a duplicate shell', async () => {
    const duplicate = { ...candidate, registrationSuggestion: 'already-registered' as const }
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response([]))
        .mockResolvedValueOnce(response([duplicate])),
    )
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await vi.waitFor(() => expect(page.querySelector('.installed-filter input')).not.toBeNull())
    const hideInstalled = page.querySelector<HTMLInputElement>('.installed-filter input')
    if (!hideInstalled) throw new Error('installed filter was not rendered')
    hideInstalled.checked = false
    hideInstalled.dispatchEvent(new Event('change'))
    await nextTick()
    expect(page.querySelector<HTMLButtonElement>('.candidate-card')?.disabled).toBe(true)
    expect(page.textContent).toContain('已由 DockFN 登记')
    expect(page.querySelector<HTMLButtonElement>('.candidate-card')?.title).toContain(
      '已由 DockFN 登记',
    )
  })

  it('groups discovery results by source and supports collapsing and ignoring services', async () => {
    const dockerHost = {
      ...candidate,
      key: 'docker:metrics:9090',
      groupKey: 'docker:metrics',
      displayName: '容器主机网络',
      sourceDetail: 'metrics',
      networkMode: 'host',
      port: 9090,
      preferred: false,
      watchCow: false,
    }
    const host = {
      ...candidate,
      key: 'host:caddy:2019',
      groupKey: 'host:caddy',
      displayName: '宿主机 Caddy',
      source: 'host' as const,
      sourceDetail: 'caddy',
      networkMode: undefined,
      port: 2019,
      pid: 4231,
      preferred: false,
      watchCow: false,
    }
    const installed = {
      ...candidate,
      key: 'docker:installed:7575',
      groupKey: 'docker:installed',
      displayName: '已安装服务',
      sourceDetail: 'installed',
      networkMode: 'bridge',
      port: 7575,
      preferred: false,
      watchCow: false,
      registrationSuggestion: 'existing-fnos-application' as const,
      existingApplication: 'watchcow.installed',
    }
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response([]))
        .mockResolvedValueOnce(response([host, installed, dockerHost, candidate])),
    )
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await vi.waitFor(() => expect(page.textContent).toContain('宿主机 Caddy'))

    const hideInstalled = page.querySelector<HTMLInputElement>('.installed-filter input')
    if (!hideInstalled) throw new Error('installed filter was not rendered')
    hideInstalled.checked = false
    hideInstalled.dispatchEvent(new Event('change'))
    await nextTick()

    expect(
      [...page.querySelectorAll('.candidate-source-heading .candidate-source')].map((item) =>
        item.textContent?.trim(),
      ),
    ).toEqual(['Docker', 'Docker Host', '宿主机'])
    expect(page.textContent).toContain('已安装服务')
    expect(page.querySelector('.discovery-result-count')?.textContent).toContain('共 4 个 Web 端口')
    expect(page.querySelector('[data-filter-tag]')).toBeNull()

    const hostHeading = [
      ...page.querySelectorAll<HTMLButtonElement>('.candidate-source-heading'),
    ].find((button) => button.textContent?.includes('宿主机'))
    hostHeading?.click()
    await nextTick()
    expect(hostHeading?.getAttribute('aria-expanded')).toBe('false')
    expect(page.textContent).not.toContain('宿主机 Caddy')

    const ignoreButton = page.querySelector<HTMLButtonElement>('.candidate-ignore')
    ignoreButton?.click()
    await nextTick()
    expect(page.textContent).toContain('已忽略')
    const ignoredHeading = [
      ...page.querySelectorAll<HTMLButtonElement>('.candidate-source-heading'),
    ].find((button) => button.textContent?.includes('已忽略'))
    expect(ignoredHeading?.getAttribute('aria-expanded')).toBe('false')
    ignoredHeading?.click()
    await nextTick()
    expect(page.textContent).toContain('恢复')
  })

  it('shows progress while fnOS is installing and keeps a failure inside the creator', async () => {
    let finishInstall: ((response: Response) => void) | undefined
    const install = new Promise<Response>((resolve) => {
      finishInstall = resolve
    })
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ dataUrl: 'data:image/png;base64,cHJldmlldw==' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockReturnValueOnce(install)
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await nextTick()
    page.querySelector<HTMLButtonElement>('.discovery-step .secondary-button')?.click()
    await nextTick()
    const name = page.querySelector<HTMLInputElement>('input[placeholder="例如：家庭相册"]')
    if (!name) throw new Error('display name input was not rendered')
    name.value = '测试应用'
    name.dispatchEvent(new Event('input'))
    const entryPrefix = page.querySelector<HTMLInputElement>('input[placeholder="留空则自动生成"]')
    if (!entryPrefix) throw new Error('entry prefix input was not rendered')
    entryPrefix.value = 'blinko'
    entryPrefix.dispatchEvent(new Event('input'))
    const iconURI = page.querySelector<HTMLInputElement>('input[placeholder*="/favicon.ico"]')
    if (!iconURI) throw new Error('relative icon URI input was not rendered')
    expect(iconURI.type).toBe('text')
    iconURI.value = '/favicon.ico'
    iconURI.dispatchEvent(new Event('input'))
    iconURI.dispatchEvent(new Event('change'))
    const openType = page.querySelector<HTMLButtonElement>(
      '.segmented-control [role="radio"][data-value="url"]',
    )
    if (!openType) throw new Error('URL open type option was not rendered')
    expect(openType.getAttribute('aria-checked')).toBe('true')
    page.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()
    await nextTick()

    const payload = JSON.parse(String(fetchMock.mock.calls[3]?.[1]?.body))
    expect(payload.openType).toBe('url')
    expect(payload.entryPrefix).toBe('blinko')
    expect(payload.iconUri).toBe('/favicon.ico')
    expect(page.querySelector('[data-step="complete"]')?.classList.contains('active')).toBe(true)
    expect(page.querySelector('[role="status"].completion-progress')).not.toBeNull()
    expect(page.textContent).toContain('正在创建应用入口')
    expect(page.textContent).toContain('提交 fnOS 应用中心并校验入口')

    finishInstall?.(
      new Response(
        JSON.stringify({
          code: 'FNOS_OPERATION_FAILED',
          message: 'fnOS registration operation failed',
          suggestion: '查看安装诊断后重试。',
        }),
        { status: 502, headers: { 'Content-Type': 'application/json' } },
      ),
    )
    await vi.waitFor(() =>
      expect(page.querySelector('.review-error')?.textContent).toContain('DockFN 已保留安装诊断'),
    )
    expect(page.querySelector('.creator-dialog')).not.toBeNull()
    expect(page.querySelector('.completion-progress')).toBeNull()
    expect(page.querySelector('[data-step="review"]')?.classList.contains('active')).toBe(true)
  })

  it('keeps a newly created single application row content-sized while showing success', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ app: application, code: 'CREATED' }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()

    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await nextTick()
    page.querySelector<HTMLButtonElement>('.discovery-step .secondary-button')?.click()
    await nextTick()
    const name = page.querySelector<HTMLInputElement>('input[placeholder*="家庭相册"]')
    if (!name) throw new Error('display name input was not rendered')
    name.value = '测试应用'
    name.dispatchEvent(new Event('input'))
    page.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()

    await vi.waitFor(() => expect(page.querySelector('.completion-step')).not.toBeNull())
    expect(page.querySelectorAll('.app-card')).toHaveLength(1)
    expect(styles).toMatch(/\.app-list\s*\{[^}]*grid-auto-rows:\s*max-content/)
    expect(styles).toMatch(/\.app-list\s*\{[^}]*align-content:\s*start/)
  })

  it('can continue from the completion step into a fresh automatic discovery', async () => {
    const nextCandidate = { ...candidate, key: 'docker:next:8081', displayName: '下一个服务' }
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(
        new Response(JSON.stringify({ app: application, code: 'CREATED' }), {
          status: 201,
          headers: { 'Content-Type': 'application/json' },
        }),
      )
      .mockResolvedValueOnce(response([nextCandidate]))
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()

    page.querySelector<HTMLButtonElement>('.topbar .primary-button')?.click()
    await vi.waitFor(() => expect(page.textContent).toContain('暂无可选服务'))
    page.querySelector<HTMLButtonElement>('.discovery-copy-actions .secondary-button')?.click()
    await nextTick()
    const name = page.querySelector<HTMLInputElement>('input[placeholder*="家庭相册"]')
    if (!name) throw new Error('display name input was not rendered')
    name.value = '测试应用'
    name.dispatchEvent(new Event('input'))
    page.querySelector<HTMLButtonElement>('button[type="submit"]')?.click()
    await vi.waitFor(() => expect(page.querySelector('.completion-step')).not.toBeNull())

    page.querySelector<HTMLButtonElement>('[data-completion-action="continue"]')?.click()
    await vi.waitFor(() => expect(page.textContent).toContain('下一个服务'))
    expect(page.querySelector('[data-step="discover"]')?.classList.contains('active')).toBe(true)
    expect(page.querySelector('.completion-step')).toBeNull()
  })

  it('shows the explicit safe-removal warning', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response([application])))
    const page = await mountPage()
    await vi.waitFor(() =>
      expect(page.querySelector('button[aria-label="移除应用入口"]')).not.toBeNull(),
    )
    page.querySelector<HTMLButtonElement>('button[aria-label="移除应用入口"]')?.click()
    await nextTick()
    const dialog = page.querySelector('[role="alertdialog"]')
    expect(dialog?.textContent).toContain('不会停止或删除目标服务')
    expect(dialog?.textContent).toContain('存储卷及业务数据')
  })

  it('does not show an open action that DockFN cannot resolve safely', async () => {
    const open = vi.fn()
    vi.stubGlobal('open', open)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response([application])))
    const page = await mountPage()
    await vi.waitFor(() => expect(page.textContent).toContain('家庭相册'))
    expect(page.querySelector<HTMLButtonElement>('.desktop-launch-button')).toBeNull()
    expect(page.textContent).not.toContain('从桌面打开')
    expect(open).not.toHaveBeenCalled()
  })

  it('opens a complete diagnostic in a separate viewer and copies its full text', async () => {
    const diagnosticText = Array.from({ length: 80 }, (_, index) => `line-${index + 1}`).join('\n')
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response([]))
        .mockResolvedValueOnce(
          new Response(
            JSON.stringify({
              logs: [{ name: 'helper.log', text: diagnosticText, present: true }],
              reports: [],
            }),
            { status: 200, headers: { 'Content-Type': 'application/json' } },
          ),
        ),
    )
    const writeText = vi.fn().mockResolvedValue(undefined)
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const page = await mountPage()
    page.querySelector<HTMLButtonElement>('button[aria-label="打开诊断"]')?.click()
    await vi.waitFor(() =>
      expect(
        page.querySelector<HTMLButtonElement>('button[aria-label="查看 helper.log"]'),
      ).not.toBeNull(),
    )
    expect(page.querySelector('.diagnostic-logs')?.textContent).toContain(
      '权限助手与 fnOS 操作日志',
    )
    expect(page.querySelector('.diagnostic-logs')?.textContent).not.toContain('line-1')
    page.querySelector<HTMLButtonElement>('button[aria-label="查看 helper.log"]')?.click()
    await nextTick()
    expect(page.querySelector('.log-viewer-dialog pre')?.textContent).toBe(diagnosticText)

    page.querySelector<HTMLButtonElement>('button[aria-label="复制完整 helper.log"]')?.click()
    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith(diagnosticText))
    await vi.waitFor(() => expect(page.textContent).toContain('helper.log 已复制'))
  })

  it('clears only DockFN diagnostic history after explicit confirmation', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([]))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            logs: [{ name: 'helper.log', text: 'old history', present: true }],
            reports: [{ name: 'last-discovery.json', text: '{}', present: true }],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(
        new Response(
          JSON.stringify({
            logs: [{ name: 'helper.log', text: 'diagnostic history cleared', present: true }],
            reports: [{ name: 'last-discovery.json', text: '', present: false }],
          }),
          { status: 200, headers: { 'Content-Type': 'application/json' } },
        ),
      )
    vi.stubGlobal('fetch', fetchMock)
    const page = await mountPage()

    page.querySelector<HTMLButtonElement>('button[aria-label="打开诊断"]')?.click()
    await vi.waitFor(() =>
      expect(
        page.querySelector<HTMLButtonElement>('button[aria-label="清空诊断历史"]'),
      ).not.toBeNull(),
    )
    page.querySelector<HTMLButtonElement>('button[aria-label="清空诊断历史"]')?.click()
    await nextTick()
    const confirmation = page.querySelector('[aria-labelledby="clear-diagnostics-title"]')
    expect(confirmation?.textContent).toContain('不会清理 fnOS、应用中心、Docker 或目标服务日志')
    Array.from(confirmation?.querySelectorAll<HTMLButtonElement>('button') || [])
      .find((button) => button.textContent?.includes('确认清空记录'))
      ?.click()

    await vi.waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        './api/system/diagnostics',
        expect.objectContaining({ method: 'DELETE' }),
      ),
    )
    await vi.waitFor(() => expect(page.textContent).toContain('历史诊断记录已清空'))
    expect(page.textContent).not.toContain('old history')
  })
})
