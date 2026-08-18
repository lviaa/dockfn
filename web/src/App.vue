<script setup lang="ts">
import { Icon } from '@iconify/vue'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import dockfnLogo from './assets/dockfn-logo.png'
import {
  APIError,
  request,
  type AppInput,
  type AppOrigin,
  type AppView,
  type Diagnostics,
  type DockFNSettings,
  type DiscoveryCandidate,
  type IdentitySuggestion,
  type OperationResult,
} from './api/client'

type CreatorStep = 'discover' | 'review' | 'complete'
type DiagnosticItem = Diagnostics['logs'][number]
type DiscoverySourceGroup = 'docker' | 'docker-host' | 'host' | 'ignored'
type IconDiscoveryStatus = '' | 'loading' | 'found' | 'missing'

const ignoredCandidatesStorageKey = 'dockfn.discovery.ignored.v1'
const hideInstalledCandidatesStorageKey = 'dockfn.discovery.hide-installed.v1'
const maxPersistedIgnoredCandidates = 500

const commonCandidateIconURIs = [
  '/favicon.ico',
  '/favicon.png',
  '/apple-touch-icon.png',
  '/apple-touch-icon-precomposed.png',
  '/icon.png',
  '/public/favicon.png',
]

const apps = ref<AppView[]>([])
const candidates = ref<DiscoveryCandidate[]>([])
const diagnostics = ref<Diagnostics | null>(null)
const loading = ref(true)
const diagnosticsLoading = ref(false)
const diagnosticsError = ref('')
const scanning = ref(false)
const scanCompleted = ref(false)
const busy = ref('')
const error = ref('')
const search = ref('')
const creatorOpen = ref(false)
const diagnosticsOpen = ref(false)
const settingsOpen = ref(false)
const settingsSaving = ref(false)
const settingsError = ref('')
const settingsSavedOpen = ref(false)
const selectedDiagnostic = ref<DiagnosticItem | null>(null)
const diagnosticNotice = ref('')
const pendingDiagnosticsClear = ref(false)
const creatorStep = ref<CreatorStep>('discover')
const collapsedDiscoveryGroups = ref<Set<string>>(new Set(['ignored']))
const hideInstalledCandidates = ref(readHideInstalledCandidates())
const ignoredCandidateKeys = ref<Set<string>>(readIgnoredCandidateKeys())
const editing = ref<AppView | null>(null)
const completedApp = ref<AppView | null>(null)
const selectedCandidate = ref<DiscoveryCandidate | null>(null)
const pendingRemoval = ref<AppView | null>(null)
const pendingIconSync = ref<AppView | null>(null)
const iconSyncCompleted = ref<AppView | null>(null)
const iconPreview = ref('')
const iconChanged = ref(false)
const iconManuallyEdited = ref(false)
const iconDiscoveryStatus = ref<IconDiscoveryStatus>('')
const entryIDManuallyEdited = ref(false)
const defaultSettings: DockFNSettings = {
  entryPrefixTemplate: 'dkfn.{id}',
  defaultOpenType: 'url',
  defaultAllUsers: false,
  autoScanOnCreate: true,
  showDockFNBadge: true,
}
const settings = reactive<DockFNSettings>({ ...defaultSettings })
const settingsDraft = reactive<DockFNSettings>({ ...defaultSettings })
let iconPreviewSequence = 0
let discoverySequence = 0
let diagnosticsRequestSequence = 0
let diagnosticsAbortController: AbortController | undefined
let pathIconTimer: ReturnType<typeof setTimeout> | undefined
let entryIDSuggestionTimer: ReturnType<typeof setTimeout> | undefined
let entryIDSuggestionSequence = 0

function readIgnoredCandidateKeys() {
  if (typeof window === 'undefined') return new Set<string>()
  try {
    const raw = window.localStorage.getItem(ignoredCandidatesStorageKey)
    if (!raw) return new Set<string>()
    const parsed: unknown = JSON.parse(raw)
    if (!Array.isArray(parsed)) return new Set<string>()
    return new Set(
      parsed.filter(
        (value): value is string =>
          typeof value === 'string' && value.length > 0 && value.length <= 512,
      ),
    )
  } catch {
    return new Set<string>()
  }
}

function readHideInstalledCandidates() {
  if (typeof window === 'undefined') return false
  try {
    return window.localStorage.getItem(hideInstalledCandidatesStorageKey) === 'true'
  } catch {
    return false
  }
}

function persistHideInstalledCandidates(value: boolean) {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(hideInstalledCandidatesStorageKey, String(value))
  } catch {
    // Private browsing or a full storage quota should not disable discovery.
  }
}

function persistIgnoredCandidateKeys(keys: Set<string>) {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(
      ignoredCandidatesStorageKey,
      JSON.stringify(Array.from(keys).slice(-maxPersistedIgnoredCandidates)),
    )
  } catch {
    // Private browsing or a full storage quota should not disable discovery.
  }
}

function syncIgnoredCandidateKeys(keys: Set<string>) {
  return request('/discovery/ignored', {
    method: 'PUT',
    body: JSON.stringify({ keys: Array.from(keys) }),
  })
}

function setIgnoredCandidateKeys(keys: Set<string>) {
  ignoredCandidateKeys.value = keys
  persistIgnoredCandidateKeys(keys)
  void syncIgnoredCandidateKeys(keys).catch(() => {
    // Keep the browser copy as a compatibility fallback when an older shell
    // is still serving the page during an upgrade.
  })
}

watch(hideInstalledCandidates, persistHideInstalledCandidates)

const form = reactive<AppInput>({
  displayName: '',
  description: '',
  entryPrefix: '',
  openType: 'url',
  protocol: 'http',
  port: 8080,
  path: '/',
  allUsers: false,
  origin: { source: 'manual' },
})

const filteredApps = computed(() => {
  const query = search.value.trim().toLocaleLowerCase('zh-CN')
  if (!query) return apps.value
  return apps.value.filter((item) =>
    [
      item.displayName,
      item.appName,
      `${item.protocol}:${item.port}${item.path}`,
      item.origin?.sourceDetail || '',
      item.origin?.description || '',
      item.origin?.networkMode || '',
      item.origin?.pid ? `PID ${item.origin.pid}` : '',
    ].some((value) => value.toLocaleLowerCase('zh-CN').includes(query)),
  )
})

const installedCandidateCount = computed(
  () =>
    candidates.value.filter((candidate) => candidate.registrationSuggestion !== 'available').length,
)

const visibleCandidates = computed(() =>
  candidates.value.filter(
    (candidate) =>
      !ignoredCandidateKeys.value.has(candidate.key) &&
      !(hideInstalledCandidates.value && candidate.registrationSuggestion !== 'available'),
  ),
)

const groupedCandidates = computed(() => {
  const sourceOrder: DiscoverySourceGroup[] = ['docker', 'docker-host', 'host', 'ignored']
  const sourceLabels: Record<DiscoverySourceGroup, string> = {
    docker: 'Docker',
    'docker-host': 'Docker Host',
    host: '宿主机',
    ignored: '已忽略',
  }
  return sourceOrder.flatMap((source) => {
    const sourceItems =
      source === 'ignored'
        ? candidates.value.filter((candidate) => ignoredCandidateKeys.value.has(candidate.key))
        : visibleCandidates.value.filter((candidate) => candidateSourceGroup(candidate) === source)
    if (!sourceItems.length) return []
    const groups = new Map<string, DiscoveryCandidate[]>()
    for (const candidate of sourceItems) {
      const key =
        candidate.groupKey || `${candidate.source}:${candidate.sourceDetail || candidate.port}`
      const group = groups.get(key) || []
      group.push(candidate)
      groups.set(key, group)
    }
    return [
      {
        key: source,
        label: sourceLabels[source],
        portCount: sourceItems.length,
        collapsed: collapsedDiscoveryGroups.value.has(source),
        groups: Array.from(groups, ([key, items]) => ({
          key,
          tags: sourceTags(items[0]),
          items: [...items].sort(
            (left, right) =>
              Number(right.preferred) - Number(left.preferred) || left.port - right.port,
          ),
        })),
      },
    ]
  })
})

const diagnosticItems = computed(() =>
  diagnostics.value
    ? [...(diagnostics.value.reports || []), ...(diagnostics.value.logs || [])]
    : [],
)

function diagnosticTitle(name: string) {
  const titles: Record<string, string> = {
    'last-discovery.json': '最近扫描',
    'last-install-failure.json': '最近安装失败',
    'runtime.log': '运行日志',
  }
  return titles[name] || name
}

function diagnosticDescription(name: string) {
  const descriptions: Record<string, string> = {
    'last-discovery.json': '最近一次服务扫描结果',
    'last-install-failure.json': '最近一次安装失败诊断',
    'runtime.log': '管理服务与权限助手的最近输出',
  }
  return descriptions[name] || 'DockFN 诊断记录'
}

const entryPrefixValid = computed(() => {
  const prefix = normalizedEntryPrefix(form.entryPrefix)
  return !prefix || /^[a-z](?:[a-z0-9-]{0,25}[a-z0-9])?$/.test(prefix)
})

const entryNamePreview = computed(() => {
  const prefix = normalizedEntryPrefix(form.entryPrefix)
  if (!prefix) return settings.entryPrefixTemplate.replace('{id}', '<应用 ID>')
  return entryPrefixValid.value ? settings.entryPrefixTemplate.replace('{id}', prefix) : '<无效 ID>'
})

const entryRulePreview = computed(() =>
  entryIDManuallyEdited.value ? '当前值由你手动指定' : '当前值根据应用名称自动生成',
)

const settingsTemplateError = computed(() =>
  validateEntryTemplate(settingsDraft.entryPrefixTemplate),
)
const settingsTemplatePreview = computed(() => {
  if (settingsTemplateError.value) return '—'
  return settingsDraft.entryPrefixTemplate.trim().replace('{id}', 'entry-7f3a2c')
})

function validateEntryTemplate(value: string) {
  const template = value.trim()
  if (!template) return '请输入 fnID 模板'
  if ((template.match(/\{id\}/g) || []).length !== 1) return '模板必须且只能包含一个 {id}'
  const marker = template.replace('{id}', '')
  if (!/[a-z0-9]/.test(marker)) return '模板不能仅包含 {id}，请增加固定标识'
  if (!/^[a-z0-9.-]*$/.test(marker)) return '模板只能包含小写字母、数字、点、连字符和 {id}'
  for (const id of ['app', 'a'.repeat(27)]) {
    const result = template.replace('{id}', id)
    if (
      result.length > 63 ||
      !/^[a-z](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$/.test(result)
    ) {
      return '模板生成的完整 fnOS ID 必须以字母开头、使用安全的小写分段且不超过 63 位'
    }
  }
  return ''
}

function normalizedEntryPrefix(value?: string) {
  return value?.trim() || ''
}

function clearEntryIDSuggestionTimer() {
  if (entryIDSuggestionTimer) clearTimeout(entryIDSuggestionTimer)
  entryIDSuggestionTimer = undefined
}

function scheduleEntryIDSuggestion() {
  clearEntryIDSuggestionTimer()
  if (editing.value || entryIDManuallyEdited.value) return
  const displayName = form.displayName.trim()
  if (!displayName) {
    form.entryPrefix = ''
    return
  }
  entryIDSuggestionTimer = setTimeout(() => void suggestEntryID(displayName), 300)
}

async function suggestEntryID(displayName: string) {
  if (editing.value || entryIDManuallyEdited.value || !displayName.trim()) return
  const sequence = ++entryIDSuggestionSequence
  try {
    const suggestion = await request<IdentitySuggestion>('/entry-ids/suggest', {
      method: 'POST',
      body: JSON.stringify({ displayName }),
    })
    if (
      sequence === entryIDSuggestionSequence &&
      !entryIDManuallyEdited.value &&
      form.displayName.trim() === displayName.trim()
    ) {
      form.entryPrefix = suggestion.entryId
    }
  } catch {
    // Suggestions are advisory. Creation can still use the stable internal-ID
    // fallback when a display name cannot be transliterated.
  }
}

function onEntryIDInput() {
  clearEntryIDSuggestionTimer()
  entryIDSuggestionSequence += 1
  entryIDManuallyEdited.value = true
}

function entryPrefix(item: AppView) {
  if (item.entryId) return item.entryId
  if (item.appName.endsWith('.dkfn')) return item.appName.slice(0, -'.dkfn'.length)
  return ''
}

function appShowsBadge(item?: Pick<AppView, 'showDockFNBadge'> | null) {
  return item?.showDockFNBadge !== false
}

const formShowsBadge = computed(() => settings.showDockFNBadge)

async function loadApps() {
  loading.value = true
  error.value = ''
  try {
    const response = await request<{ items: AppView[]; settings?: DockFNSettings }>('/apps')
    apps.value = response.items
    if (response.settings) {
      Object.assign(settings, response.settings)
      Object.assign(settingsDraft, response.settings)
    }
  } catch (reason) {
    showError(reason)
  } finally {
    loading.value = false
  }
}

async function openSettings() {
  settingsOpen.value = true
  settingsSavedOpen.value = false
  settingsError.value = ''
  Object.assign(settingsDraft, settings)
  try {
    const response = await request<DockFNSettings>('/settings')
    Object.assign(settings, response)
    Object.assign(settingsDraft, response)
  } catch (reason) {
    settingsError.value = reason instanceof Error ? reason.message : '无法读取全局配置'
  }
}

async function saveSettings() {
  if (settingsTemplateError.value) return
  settingsSaving.value = true
  settingsError.value = ''
  try {
    const response = await request<DockFNSettings>('/settings', {
      method: 'PUT',
      body: JSON.stringify(settingsDraft),
    })
    Object.assign(settings, response)
    Object.assign(settingsDraft, response)
    settingsOpen.value = false
    settingsSavedOpen.value = true
  } catch (reason) {
    settingsError.value = reason instanceof Error ? reason.message : '保存全局配置失败'
  } finally {
    settingsSaving.value = false
  }
}

function resetForm() {
  clearPathIconTimer()
  clearEntryIDSuggestionTimer()
  iconPreviewSequence += 1
  entryIDSuggestionSequence += 1
  Object.assign(form, {
    displayName: '',
    description: '',
    entryPrefix: '',
    openType: settings.defaultOpenType,
    protocol: 'http',
    port: 8080,
    path: '/',
    allUsers: settings.defaultAllUsers,
    origin: { source: 'manual' },
    iconBase64: undefined,
    iconUri: undefined,
  })
  iconPreview.value = ''
  iconChanged.value = false
  iconManuallyEdited.value = false
  iconDiscoveryStatus.value = ''
  entryIDManuallyEdited.value = false
}

function beginCreate() {
  editing.value = null
  completedApp.value = null
  selectedCandidate.value = null
  candidates.value = []
  ignoredCandidateKeys.value = readIgnoredCandidateKeys()
  collapsedDiscoveryGroups.value = new Set(['ignored'])
  scanCompleted.value = false
  creatorStep.value = 'discover'
  resetForm()
  error.value = ''
  creatorOpen.value = true
  if (settings.autoScanOnCreate) void scanServices()
}

function beginManualCreate() {
  selectedCandidate.value = null
  resetForm()
  creatorStep.value = 'review'
}

function beginEdit(item: AppView) {
  editing.value = item
  completedApp.value = null
  selectedCandidate.value = null
  creatorStep.value = 'review'
  clearPathIconTimer()
  iconManuallyEdited.value = false
  iconDiscoveryStatus.value = ''
  Object.assign(form, {
    displayName: item.displayName,
    description: item.description || '',
    entryPrefix: entryPrefix(item),
    openType: item.openType || 'url',
    protocol: item.protocol,
    port: item.port,
    path: item.path,
    allUsers: item.allUsers,
    origin: item.origin,
    iconBase64: undefined,
    iconUri: undefined,
  })
  iconPreview.value = item.iconDataUrl || ''
  iconChanged.value = false
  entryIDManuallyEdited.value = true
  error.value = ''
  creatorOpen.value = true
}

async function scanServices() {
  const sequence = ++discoverySequence
  scanning.value = true
  error.value = ''
  try {
    const response = await request<{
      items: DiscoveryCandidate[]
      ignoredKeys?: string[]
    }>('/discovery/scan', {
      method: 'POST',
    })
    if (sequence !== discoverySequence) return
    candidates.value = response.items
    if (Array.isArray(response.ignoredKeys)) {
      const serverKeys = new Set(response.ignoredKeys)
      const localKeys = readIgnoredCandidateKeys()
      ignoredCandidateKeys.value = serverKeys.size ? serverKeys : localKeys
      persistIgnoredCandidateKeys(ignoredCandidateKeys.value)
      if (!serverKeys.size && localKeys.size) {
        void syncIgnoredCandidateKeys(localKeys).catch(() => undefined)
      }
    }
  } catch (reason) {
    if (sequence === discoverySequence) showError(reason)
  } finally {
    if (sequence === discoverySequence) {
      scanning.value = false
      scanCompleted.value = true
    }
  }
}

function selectCandidate(candidate: DiscoveryCandidate) {
  if (candidate.registrationSuggestion !== 'available') {
    error.value = `${suggestionLabel(candidate)}，为避免重复安装，不能再次创建 DockFN 入口。`
    return
  }
  selectedCandidate.value = candidate
  Object.assign(form, {
    displayName: candidate.displayName,
    description: '',
    entryPrefix: '',
    openType: settings.defaultOpenType,
    protocol: candidate.protocol,
    port: candidate.port,
    path: candidate.path,
    allUsers: settings.defaultAllUsers,
    origin: {
      source: candidate.source,
      sourceDetail: candidate.sourceDetail,
      description: candidate.description,
      networkMode: candidate.networkMode,
      pid: candidate.pid,
      watchCow: candidate.watchCow,
    },
    iconBase64: undefined,
    iconUri: undefined,
  })
  iconPreview.value = ''
  iconChanged.value = false
  iconManuallyEdited.value = false
  iconDiscoveryStatus.value = ''
  entryIDManuallyEdited.value = false
  creatorStep.value = 'review'
  void suggestEntryID(candidate.displayName)
  void discoverCandidateIcon(candidate)
}

async function discoverCandidateIcon(candidate: DiscoveryCandidate) {
  if (iconManuallyEdited.value) return
  const sequence = ++iconPreviewSequence
  const iconURIs = [candidate.iconUri?.trim(), ...commonCandidateIconURIs].filter(
    (value, index, values): value is string => !!value && values.indexOf(value) === index,
  )
  for (const iconUri of iconURIs) {
    try {
      const response = await request<{ dataUrl: string }>('/icons/preview', {
        method: 'POST',
        body: JSON.stringify({
          iconUri,
          protocol: candidate.protocol,
          port: candidate.port,
        }),
      })
      if (
        sequence !== iconPreviewSequence ||
        selectedCandidate.value?.key !== candidate.key ||
        iconManuallyEdited.value
      ) {
        return
      }
      form.iconUri = iconUri
      iconPreview.value = response.dataUrl
      return
    } catch {
      // Candidate icon discovery is advisory and continues through the small
      // local favicon allowlist before falling back to the DockFN icon.
    }
  }
}

function clearPathIconTimer() {
  if (pathIconTimer !== undefined) {
    clearTimeout(pathIconTimer)
    pathIconTimer = undefined
  }
}

function schedulePathIconDiscovery() {
  clearPathIconTimer()
  iconPreviewSequence += 1
  iconDiscoveryStatus.value = ''
  if (iconManuallyEdited.value || creatorStep.value !== 'review') return
  pathIconTimer = setTimeout(() => {
    pathIconTimer = undefined
    void discoverPathIcon()
  }, 400)
}

async function discoverPathIcon() {
  if (iconManuallyEdited.value || creatorStep.value !== 'review' || !creatorOpen.value) return
  const protocol = form.protocol
  const port = Number(form.port)
  const path = form.path.trim()
  if (
    (protocol !== 'http' && protocol !== 'https') ||
    !Number.isInteger(port) ||
    port < 1 ||
    port > 65535 ||
    !path.startsWith('/')
  ) {
    return
  }
  const sequence = ++iconPreviewSequence
  const fingerprint = `${protocol}:${port}:${path}`
  iconDiscoveryStatus.value = 'loading'
  try {
    const response = await request<{ iconUri: string; dataUrl: string }>('/icons/discover', {
      method: 'POST',
      body: JSON.stringify({ protocol, port, path }),
    })
    if (
      sequence !== iconPreviewSequence ||
      iconManuallyEdited.value ||
      fingerprint !== `${form.protocol}:${form.port}:${form.path.trim()}`
    ) {
      return
    }
    form.iconUri = response.iconUri
    iconPreview.value = response.dataUrl
    iconChanged.value = false
    iconDiscoveryStatus.value = 'found'
  } catch {
    if (sequence === iconPreviewSequence && !iconManuallyEdited.value) {
      iconDiscoveryStatus.value = 'missing'
    }
  }
}

function rediscoverPathIcon() {
  clearPathIconTimer()
  iconManuallyEdited.value = false
  void discoverPathIcon()
}

function backToDiscovery() {
  if (editing.value) return
  creatorStep.value = 'discover'
}

async function selectIcon(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  const supportedType = [
    'image/png',
    'image/jpeg',
    'image/x-icon',
    'image/vnd.microsoft.icon',
  ].includes(file.type)
  if (
    (!supportedType && !file.name.toLocaleLowerCase('en-US').endsWith('.ico')) ||
    file.size > 2 * 1024 * 1024
  ) {
    error.value = '图标必须是 2 MiB 以内的 PNG、JPEG 或 ICO。'
    input.value = ''
    return
  }
  clearPathIconTimer()
  iconManuallyEdited.value = true
  iconDiscoveryStatus.value = ''
  iconPreviewSequence += 1
  iconPreview.value = await readDataURL(file)
  form.iconUri = undefined
  iconChanged.value = true
}

function removeIcon() {
  clearPathIconTimer()
  iconManuallyEdited.value = true
  iconDiscoveryStatus.value = ''
  iconPreviewSequence += 1
  iconPreview.value = ''
  form.iconUri = undefined
  iconChanged.value = true
}

async function refreshIconURI() {
  clearPathIconTimer()
  iconManuallyEdited.value = true
  iconDiscoveryStatus.value = ''
  const value = form.iconUri?.trim()
  form.iconUri = value || undefined
  iconChanged.value = false
  await previewIconURI(value)
}

function onIconURIInput() {
  clearPathIconTimer()
  iconManuallyEdited.value = true
  iconDiscoveryStatus.value = ''
  iconPreviewSequence += 1
  iconPreview.value = ''
  iconChanged.value = false
}

async function previewIconURI(value?: string) {
  const uri = value?.trim()
  const sequence = ++iconPreviewSequence
  iconPreview.value = ''
  if (!uri) return
  try {
    const response = await request<{ dataUrl: string }>('/icons/preview', {
      method: 'POST',
      body: JSON.stringify({ iconUri: uri, protocol: form.protocol, port: form.port }),
    })
    if (sequence === iconPreviewSequence && form.iconUri?.trim() === uri) {
      iconPreview.value = response.dataUrl
    }
  } catch {
    // Preview is advisory. Creation performs the authoritative icon validation
    // and reports a field error without discarding the URI the user entered.
  }
}

async function submit() {
  const creating = !editing.value
  clearEntryIDSuggestionTimer()
  entryIDSuggestionSequence += 1
  busy.value = 'submit'
  error.value = ''
  try {
    const payload: AppInput = { ...form }
    if (iconChanged.value) {
      payload.iconBase64 = iconPreview.value
      payload.iconUri = undefined
    }
    const path = editing.value ? `/apps/${editing.value.id}` : '/apps'
    const method = editing.value ? 'PUT' : 'POST'
    if (creating) {
      completedApp.value = null
      creatorStep.value = 'complete'
    }
    const response = await request<OperationResult>(path, {
      method,
      body: JSON.stringify(payload),
    })
    replaceApp(response.app)
    if (editing.value) {
      creatorOpen.value = false
    } else {
      completedApp.value = response.app
      creatorStep.value = 'complete'
    }
  } catch (reason) {
    if (creating) creatorStep.value = 'review'
    showError(reason)
  } finally {
    busy.value = ''
  }
}

function beginIconSync(item: AppView) {
  error.value = ''
  iconSyncCompleted.value = null
  pendingIconSync.value = item
}

async function syncDesktopIconConfirmed() {
  const item = pendingIconSync.value
  if (!item) return
  busy.value = `${item.id}:refresh-icon`
  error.value = ''
  try {
    const response = await request<OperationResult>(`/apps/${item.id}/refresh-icon`, {
      method: 'POST',
    })
    replaceApp(response.app)
    iconSyncCompleted.value = response.app
  } catch (reason) {
    showError(reason)
  } finally {
    busy.value = ''
  }
}

function closeIconSync() {
  if (busy.value) return
  pendingIconSync.value = null
  iconSyncCompleted.value = null
}

async function runAction(item: AppView, action: 'repair' | 'rollback') {
  busy.value = `${item.id}:${action}`
  error.value = ''
  try {
    const response = await request<OperationResult>(`/apps/${item.id}/${action}`, {
      method: 'POST',
    })
    replaceApp(response.app)
  } catch (reason) {
    showError(reason)
  } finally {
    busy.value = ''
  }
}

async function removeConfirmed() {
  const item = pendingRemoval.value
  if (!item) return
  busy.value = `${item.id}:remove`
  error.value = ''
  try {
    await request<void>(`/apps/${item.id}`, { method: 'DELETE' })
    apps.value = apps.value.filter((candidate) => candidate.id !== item.id)
    pendingRemoval.value = null
  } catch (reason) {
    showError(reason)
  } finally {
    busy.value = ''
  }
}

async function openDiagnostics() {
  diagnosticsRequestSequence += 1
  const sequence = diagnosticsRequestSequence
  diagnosticsAbortController?.abort()
  const controller = new AbortController()
  diagnosticsAbortController = controller
  const timeout = setTimeout(() => controller.abort(), 15_000)
  diagnosticsOpen.value = true
  selectedDiagnostic.value = null
  diagnosticNotice.value = ''
  diagnosticsError.value = ''
  diagnostics.value = null
  diagnosticsLoading.value = true
  try {
    const result = await request<Diagnostics>('/system/diagnostics', { signal: controller.signal })
    if (sequence === diagnosticsRequestSequence) diagnostics.value = result
  } catch (reason) {
    if (sequence !== diagnosticsRequestSequence) return
    diagnosticsError.value =
      reason instanceof DOMException && reason.name === 'AbortError'
        ? '诊断信息读取超时，请确认 DockFN 服务正在运行后重试。'
        : errorMessage(reason)
  } finally {
    clearTimeout(timeout)
    if (sequence === diagnosticsRequestSequence) {
      diagnosticsLoading.value = false
      diagnosticsAbortController = undefined
    }
  }
}

function closeDiagnostics() {
  diagnosticsRequestSequence += 1
  diagnosticsAbortController?.abort()
  diagnosticsAbortController = undefined
  diagnosticsLoading.value = false
  diagnosticsError.value = ''
  selectedDiagnostic.value = null
  pendingDiagnosticsClear.value = false
  diagnosticsOpen.value = false
}

async function clearDiagnosticsConfirmed() {
  busy.value = 'clear-diagnostics'
  diagnosticNotice.value = ''
  error.value = ''
  try {
    await request<void>('/system/diagnostics', { method: 'DELETE' })
    selectedDiagnostic.value = null
    pendingDiagnosticsClear.value = false
    diagnostics.value = await request<Diagnostics>('/system/diagnostics')
    diagnosticNotice.value = '历史诊断记录已清空；当前内容从本次清理后开始。'
  } catch (reason) {
    showError(reason)
  } finally {
    busy.value = ''
  }
}

function openDiagnostic(item: DiagnosticItem) {
  if (!item.present) return
  diagnosticNotice.value = ''
  selectedDiagnostic.value = item
}

async function copyDiagnostic(item: DiagnosticItem) {
  if (!item.present) return
  const text = item.text || ''
  try {
    if (!navigator.clipboard?.writeText) throw new Error('Clipboard API unavailable')
    await navigator.clipboard.writeText(text)
  } catch {
    const input = document.createElement('textarea')
    input.value = text
    input.style.position = 'fixed'
    input.style.opacity = '0'
    document.body.append(input)
    input.select()
    const copied = document.execCommand('copy')
    input.remove()
    if (!copied) {
      diagnosticNotice.value = `${item.name} 复制失败，请在独立窗口中手动选择。`
      return
    }
  }
  diagnosticNotice.value = `${diagnosticTitle(item.name)} 已复制。`
}

function replaceApp(item: AppView) {
  const index = apps.value.findIndex((candidate) => candidate.id === item.id)
  if (index < 0) apps.value = [item, ...apps.value]
  else apps.value[index] = item
}

function sourceTags(candidate: DiscoveryCandidate) {
  if (candidate.source === 'docker') {
    const tags = [candidate.sourceDetail || '未命名容器']
    if (candidate.networkMode && candidate.networkMode !== 'host') tags.push(candidate.networkMode)
    return tags
  }
  const tags = [candidate.sourceDetail || '未知进程']
  if (candidate.pid) tags.push(`PID ${candidate.pid}`)
  return tags
}

function candidateSourceGroup(
  candidate: Pick<DiscoveryCandidate, 'source' | 'networkMode'>,
): Exclude<DiscoverySourceGroup, 'ignored'> {
  if (candidate.source === 'host') return 'host'
  return candidate.networkMode === 'host' ? 'docker-host' : 'docker'
}

function showAllDiscoveryCandidates() {
  collapsedDiscoveryGroups.value = new Set()
  hideInstalledCandidates.value = false
}

function toggleDiscoveryGroup(key: string) {
  const next = new Set(collapsedDiscoveryGroups.value)
  if (next.has(key)) next.delete(key)
  else next.add(key)
  collapsedDiscoveryGroups.value = next
}

function ignoreCandidate(candidate: DiscoveryCandidate) {
  const next = new Set(ignoredCandidateKeys.value)
  next.add(candidate.key)
  setIgnoredCandidateKeys(next)
  collapsedDiscoveryGroups.value = new Set(collapsedDiscoveryGroups.value).add('ignored')
}

function restoreCandidate(candidate: DiscoveryCandidate) {
  const next = new Set(ignoredCandidateKeys.value)
  next.delete(candidate.key)
  setIgnoredCandidateKeys(next)
}

function originSourceTag(origin: Pick<AppOrigin, 'source' | 'networkMode'>) {
  if (origin.source === 'manual') return '手动配置'
  if (origin.source !== 'docker') return '宿主机'
  return origin.networkMode === 'host' ? 'Docker Host' : 'Docker'
}

function appOriginTags(item: AppView) {
  const origin = item.origin
  if (!origin) return ['来源未记录']
  const tags = [originSourceTag(origin)]
  if (origin.sourceDetail) tags.push(origin.sourceDetail)
  if (origin.networkMode && origin.networkMode !== 'host') tags.push(origin.networkMode)
  if (origin.pid) tags.push(`PID ${origin.pid}`)
  if (origin.watchCow) tags.push('WatchCow')
  if (origin.description && origin.description !== origin.sourceDetail)
    tags.push(origin.description)
  return tags
}

function endpointAddress(candidate: DiscoveryCandidate) {
  const address = candidate.address || '127.0.0.1'
  if (address === '0.0.0.0' || address === '*') return '127.0.0.1'
  if (address === '::') return '[::1]'
  return address.includes(':') ? `[${address}]` : address
}

function suggestionLabel(candidate: DiscoveryCandidate) {
  if (candidate.registrationSuggestion === 'already-registered') return '已由 DockFN 登记'
  if (candidate.registrationSuggestion === 'existing-fnos-application')
    return `已存在 fnOS 应用：${candidate.existingApplication}`
  return '可创建'
}

function errorMessage(reason: unknown) {
  if (reason instanceof APIError) {
    if (reason.code === 'FNOS_OPERATION_FAILED') {
      return 'fnOS 应用登记未完成。DockFN 已保留安装诊断；请打开“诊断”查看详情后重试。'
    }
    return `${reason.message}${reason.suggestion ? ` ${reason.suggestion}` : ''}`
  }
  return reason instanceof Error ? reason.message : '操作失败，请稍后重试。'
}

function showError(reason: unknown) {
  error.value = errorMessage(reason)
}

function readDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(String(reader.result))
    reader.onerror = () => reject(reader.error)
    reader.readAsDataURL(file)
  })
}

onMounted(loadApps)
onBeforeUnmount(() => {
  diagnosticsAbortController?.abort()
  clearPathIconTimer()
  clearEntryIDSuggestionTimer()
})
</script>

<template>
  <div class="page-shell">
    <section class="app-frame" aria-label="DockFN 管理界面">
      <header class="topbar">
        <div class="brand-block">
          <img class="brand-mark" :src="dockfnLogo" alt="DockFN" />
          <div>
            <div class="brand-line"><strong>DockFN</strong></div>
            <p>把已有 Web 服务接入 fnOS 桌面</p>
          </div>
        </div>
        <div class="topbar-actions">
          <button
            class="quiet-button"
            type="button"
            aria-label="打开全局配置"
            title="全局配置"
            @click="openSettings"
          >
            <Icon icon="solar:settings-linear" aria-hidden="true" />
            <span>配置</span>
          </button>
          <button class="quiet-button" type="button" aria-label="打开诊断" @click="openDiagnostics">
            <Icon icon="solar:health-linear" aria-hidden="true" />
            <span>诊断</span>
          </button>
          <button class="primary-button" type="button" @click="beginCreate">
            <Icon icon="solar:add-circle-linear" aria-hidden="true" />
            <span>新增应用</span>
          </button>
        </div>
      </header>

      <main class="workspace" aria-labelledby="application-heading">
        <div class="workspace-head">
          <div>
            <h1 id="application-heading">
              已注册应用 <span>{{ apps.length }}</span>
            </h1>
          </div>
          <label class="search">
            <Icon icon="solar:magnifer-linear" aria-hidden="true" />
            <input v-model="search" type="search" placeholder="搜索应用或端口" />
          </label>
        </div>

        <div v-if="error" class="message error" role="alert">
          <Icon icon="solar:danger-triangle-linear" aria-hidden="true" />
          <span>{{ error }}</span>
          <button type="button" aria-label="关闭错误" title="关闭错误" @click="error = ''">
            <Icon icon="solar:close-circle-linear" />
          </button>
        </div>

        <div v-if="loading" class="empty-state">
          <Icon class="loader" icon="solar:refresh-linear" />正在读取应用…
        </div>
        <div v-else-if="filteredApps.length === 0" class="empty-state">
          <img class="empty-logo" :src="dockfnLogo" alt="" />
          <h2>{{ apps.length ? '没有匹配的应用' : '还没有 DockFN 应用' }}</h2>
          <p>{{ apps.length ? '换个关键词试试。' : '扫描本地 Web 服务，或直接手动填写入口。' }}</p>
          <button v-if="!apps.length" class="primary-button" type="button" @click="beginCreate">
            <Icon icon="solar:add-circle-linear" aria-hidden="true" />新增第一个应用
          </button>
        </div>

        <div v-else class="app-list" aria-live="polite">
          <article v-for="item in filteredApps" :key="item.id" class="app-card">
            <div class="app-icon-wrap">
              <img
                class="app-icon"
                :src="item.iconDataUrl || dockfnLogo"
                :alt="`${item.displayName} 图标`"
              />
              <img
                v-if="appShowsBadge(item)"
                class="dockfn-badge"
                :src="dockfnLogo"
                alt="由 DockFN 创建"
                title="由 DockFN 创建"
              />
            </div>
            <div class="app-identity">
              <div class="name-row">
                <h2 :title="`${item.displayName} · ${item.appName}`">{{ item.displayName }}</h2>
                <span class="revision">r{{ item.revision }}</span>
              </div>
              <div
                class="app-origin-tags"
                :title="[item.appName, ...appOriginTags(item)].join(' · ')"
                :aria-label="`来源：${appOriginTags(item).join('，')}`"
              >
                <span
                  v-for="(tag, index) in appOriginTags(item)"
                  :key="`${index}:${tag}`"
                  class="app-origin-tag"
                  :class="{ primary: index === 0 }"
                  >{{ tag }}</span
                >
              </div>
            </div>
            <div class="endpoint">
              <span class="endpoint-type"
                >{{ item.protocol.toUpperCase() }} · {{ item.openType.toUpperCase() }}</span
              >
              <strong class="endpoint-port">{{ item.port }}</strong>
              <small class="endpoint-path">{{ item.path }}</small>
            </div>
            <div class="statuses">
              <Icon
                :icon="
                  item.status.registration === 'installed'
                    ? 'solar:verified-check-linear'
                    : 'solar:danger-triangle-linear'
                "
                aria-hidden="true"
              />
              <div>
                <strong>{{
                  item.status.registration === 'installed'
                    ? '入口已登记'
                    : item.status.registration === 'missing'
                      ? '登记壳缺失'
                      : '待诊断'
                }}</strong>
                <small :class="item.status.target">{{
                  item.status.target === 'available' ? '目标端口可用' : '目标端口不可用'
                }}</small>
              </div>
            </div>
            <div class="actions">
              <button
                class="icon-action"
                type="button"
                :disabled="!!busy"
                aria-label="同步桌面图标"
                title="同步桌面图标"
                @click="beginIconSync(item)"
              >
                <Icon icon="solar:refresh-linear" />
              </button>
              <button
                class="icon-action"
                type="button"
                :disabled="!!busy"
                aria-label="编辑应用"
                title="编辑应用"
                @click="beginEdit(item)"
              >
                <Icon icon="solar:pen-linear" />
              </button>
              <button
                v-if="item.status.registration !== 'installed'"
                class="icon-action"
                type="button"
                :disabled="!!busy"
                aria-label="修复登记壳"
                title="修复登记壳"
                @click="runAction(item, 'repair')"
              >
                <Icon icon="solar:restart-linear" />
              </button>
              <button
                class="icon-action danger"
                type="button"
                :disabled="!!busy"
                aria-label="移除应用入口"
                title="移除应用入口"
                @click="pendingRemoval = item"
              >
                <Icon icon="solar:trash-bin-trash-linear" />
              </button>
            </div>
            <p v-if="item.status.lastError" class="last-error">{{ item.status.lastError }}</p>
          </article>
        </div>
      </main>
    </section>

    <Transition name="fade">
      <div v-if="settingsOpen" class="overlay modal-overlay" @click.self="settingsOpen = false">
        <section
          class="settings-dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby="settings-title"
        >
          <header class="dialog-head">
            <div>
              <h2 id="settings-title">全局配置</h2>
            </div>
            <button
              class="icon-action"
              type="button"
              aria-label="关闭全局配置"
              title="关闭"
              :disabled="settingsSaving"
              @click="settingsOpen = false"
            >
              <Icon icon="solar:close-circle-linear" />
            </button>
          </header>

          <form class="settings-form" @submit.prevent="saveSettings">
            <div v-if="settingsError" class="review-error" role="alert">
              <Icon icon="solar:danger-triangle-linear" aria-hidden="true" />
              <span>{{ settingsError }}</span>
            </div>
            <section class="settings-section" aria-labelledby="identity-settings-title">
              <div class="settings-section-title">
                <h3 id="identity-settings-title">入口标识</h3>
                <span class="info-tip" tabindex="0" aria-label="fnID 模板说明">
                  <Icon icon="solar:info-circle-linear" aria-hidden="true" />
                  <span role="tooltip"
                    >{id} 表示根据应用名称自动生成、且可在创建前修改的应用 ID。模板描述完整 fnOS
                    ID，不会再自动追加后缀；修改模板不会重命名现有应用。</span
                  >
                </span>
              </div>
              <label class="settings-field">
                <span>fnID 生成模板</span>
                <input
                  v-model="settingsDraft.entryPrefixTemplate"
                  required
                  maxlength="63"
                  :aria-invalid="!!settingsTemplateError"
                  placeholder="dkfn.{id}"
                />
                <small>填写完整模板，例如 <code>dkfn.{id}</code> 或 <code>{id}.dkfn</code></small>
                <small v-if="settingsTemplateError" class="field-error">{{
                  settingsTemplateError
                }}</small>
                <small v-else class="field-preview"
                  >生成结果：<code>{{ settingsTemplatePreview }}</code></small
                >
              </label>
            </section>

            <section class="settings-section" aria-labelledby="create-defaults-title">
              <div class="settings-section-title">
                <h3 id="create-defaults-title">新增应用默认值</h3>
                <span class="info-tip" tabindex="0" aria-label="新增应用默认值说明">
                  <Icon icon="solar:info-circle-linear" aria-hidden="true" />
                  <span role="tooltip">这些选项只作为新增表单的默认值，仍可在创建前单独修改。</span>
                </span>
              </div>
              <fieldset class="settings-open-type">
                <legend>默认打开方式</legend>
                <div class="segmented-control" role="radiogroup" aria-label="默认打开方式">
                  <button
                    type="button"
                    role="radio"
                    :class="{ active: settingsDraft.defaultOpenType === 'url' }"
                    :aria-checked="settingsDraft.defaultOpenType === 'url'"
                    @click="settingsDraft.defaultOpenType = 'url'"
                  >
                    URL
                  </button>
                  <button
                    type="button"
                    role="radio"
                    :class="{ active: settingsDraft.defaultOpenType === 'iframe' }"
                    :aria-checked="settingsDraft.defaultOpenType === 'iframe'"
                    @click="settingsDraft.defaultOpenType = 'iframe'"
                  >
                    iframe
                  </button>
                </div>
              </fieldset>
              <label class="toggle-line compact-toggle">
                <input v-model="settingsDraft.defaultAllUsers" type="checkbox" />
                <span
                  ><strong>默认所有用户可见</strong><small>关闭时默认仅管理员可见。</small></span
                >
              </label>
              <label class="toggle-line compact-toggle">
                <input v-model="settingsDraft.autoScanOnCreate" type="checkbox" />
                <span
                  ><strong>进入新增流程后自动扫描</strong
                  ><small>关闭后可手动点击扫描。</small></span
                >
              </label>
              <label class="toggle-line compact-toggle">
                <input v-model="settingsDraft.showDockFNBadge" type="checkbox" />
                <span
                  ><strong>应用入口显示 DockFN 角标</strong
                  ><small
                    >新建或重新保存时采用此配置；桌面仍显示旧图标时，请清除浏览器图片缓存后刷新。</small
                  ></span
                >
              </label>
            </section>

            <footer class="dialog-actions settings-actions">
              <button
                class="secondary-button"
                type="button"
                :disabled="settingsSaving"
                @click="settingsOpen = false"
              >
                取消
              </button>
              <button
                class="primary-button"
                type="submit"
                :disabled="settingsSaving || !!settingsTemplateError"
              >
                <Icon
                  :class="{ loader: settingsSaving }"
                  :icon="settingsSaving ? 'solar:refresh-linear' : 'solar:diskette-linear'"
                  aria-hidden="true"
                />{{ settingsSaving ? '保存中…' : '保存配置' }}
              </button>
            </footer>
          </form>
        </section>
      </div>
    </Transition>

    <Transition name="fade">
      <div
        v-if="settingsSavedOpen"
        class="overlay modal-overlay"
        @click.self="settingsSavedOpen = false"
      >
        <section
          class="confirm-dialog settings-saved-dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby="settings-saved-title"
        >
          <Icon class="success-icon" icon="solar:check-circle-linear" aria-hidden="true" />
          <h2 id="settings-saved-title">配置已保存</h2>
          <p>新配置将在以后创建应用时生效，已注册应用不会被自动重命名。</p>
          <footer class="dialog-actions">
            <button class="primary-button" type="button" @click="settingsSavedOpen = false">
              确定
            </button>
          </footer>
        </section>
      </div>
    </Transition>

    <Transition name="fade">
      <div v-if="creatorOpen" class="overlay creator-overlay">
        <section
          class="creator-dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby="creator-title"
        >
          <header class="dialog-head">
            <div class="brand-block compact">
              <img class="brand-mark" :src="dockfnLogo" alt="" />
              <div>
                <p class="eyebrow">
                  {{
                    editing
                      ? 'EDIT APPLICATION'
                      : creatorStep === 'complete'
                        ? busy === 'submit'
                          ? 'CREATING APPLICATION'
                          : 'APPLICATION READY'
                        : 'CREATE APPLICATION'
                  }}
                </p>
                <h2 id="creator-title">
                  {{
                    editing
                      ? '编辑 DockFN 应用'
                      : creatorStep === 'complete'
                        ? busy === 'submit'
                          ? '正在创建 DockFN 应用'
                          : '应用创建完成'
                        : '新增 DockFN 应用'
                  }}
                </h2>
              </div>
            </div>
            <button
              class="icon-action"
              type="button"
              :disabled="!!busy"
              aria-label="关闭"
              title="关闭"
              @click="creatorOpen = false"
            >
              <Icon icon="solar:close-circle-linear" />
            </button>
          </header>

          <ol v-if="!editing" class="stepper" aria-label="创建步骤">
            <li
              data-step="discover"
              :class="{ active: creatorStep === 'discover', done: creatorStep !== 'discover' }"
            >
              <span>1</span>发现服务
            </li>
            <li
              data-step="review"
              :class="{ active: creatorStep === 'review', done: creatorStep === 'complete' }"
            >
              <span>2</span>核对信息
            </li>
            <li data-step="complete" :class="{ active: creatorStep === 'complete' }">
              <span>3</span>创建应用
            </li>
          </ol>

          <section v-if="creatorStep === 'discover'" class="discovery-step">
            <div class="discovery-copy">
              <div>
                <h3>从本机发现 Web 服务</h3>
                <p>扫描本机 Web 服务；选择候选并确认后创建应用。</p>
              </div>
              <div class="discovery-copy-actions">
                <button class="secondary-button" type="button" @click="beginManualCreate">
                  <Icon icon="solar:pen-new-square-linear" />手动填写
                </button>
                <button
                  class="primary-button"
                  type="button"
                  :disabled="scanning"
                  @click="scanServices"
                >
                  <span v-if="scanning" class="discovery-spinner" aria-hidden="true"></span>
                  <Icon v-else class="discovery-scan-icon" icon="solar:radar-2-linear" />{{
                    scanning ? '扫描中…' : scanCompleted ? '重新扫描' : '扫描本地服务'
                  }}
                </button>
              </div>
            </div>
            <div v-if="candidates.length" class="discovery-results">
              <div class="discovery-toolbar">
                <div class="discovery-result-count">
                  共 {{ candidates.length }} 个 Web 端口，按服务来源分组
                </div>
                <label class="installed-filter" :class="{ active: hideInstalledCandidates }">
                  <input v-model="hideInstalledCandidates" type="checkbox" />
                  <span>隐藏已注册</span><small>{{ installedCandidateCount }}</small>
                </label>
              </div>
              <div v-if="groupedCandidates.length" class="candidate-list">
                <section
                  v-for="sourceGroup in groupedCandidates"
                  :key="sourceGroup.key"
                  class="candidate-source-group"
                  :class="{
                    collapsed: sourceGroup.collapsed,
                    ignored: sourceGroup.key === 'ignored',
                  }"
                >
                  <button
                    class="candidate-source-heading"
                    type="button"
                    :aria-expanded="!sourceGroup.collapsed"
                    @click="toggleDiscoveryGroup(sourceGroup.key)"
                  >
                    <span class="candidate-source" :class="sourceGroup.key">{{
                      sourceGroup.label
                    }}</span>
                    <span class="candidate-source-summary"
                      >{{ sourceGroup.groups.length }} 个服务 ·
                      {{ sourceGroup.portCount }} 个端口</span
                    >
                    <Icon
                      :icon="
                        sourceGroup.collapsed
                          ? 'solar:alt-arrow-down-linear'
                          : 'solar:alt-arrow-up-linear'
                      "
                      aria-hidden="true"
                    />
                  </button>
                  <div v-if="!sourceGroup.collapsed" class="candidate-source-body">
                    <section
                      v-for="group in sourceGroup.groups"
                      :key="group.key"
                      class="candidate-group"
                    >
                      <header>
                        <div class="candidate-group-tags">
                          <span
                            v-for="(tag, index) in group.tags"
                            :key="tag"
                            class="candidate-group-tag"
                            :class="{ primary: index === 0 }"
                            >{{ tag }}</span
                          >
                        </div>
                        <span class="candidate-group-count"
                          >{{ group.items.length }} 个 Web 端口</span
                        >
                      </header>
                      <div
                        v-for="candidate in group.items"
                        :key="candidate.key"
                        class="candidate-row"
                      >
                        <button
                          class="candidate-card"
                          type="button"
                          :disabled="
                            sourceGroup.key === 'ignored' ||
                            candidate.registrationSuggestion !== 'available'
                          "
                          :title="
                            sourceGroup.key === 'ignored'
                              ? '该服务已忽略'
                              : candidate.registrationSuggestion === 'available'
                                ? '选择此 Web 服务'
                                : suggestionLabel(candidate)
                          "
                          @click="selectCandidate(candidate)"
                        >
                          <span class="candidate-main"
                            ><strong
                              >{{ candidate.displayName
                              }}<em v-if="candidate.preferred">WatchCow 主入口</em></strong
                            ><small
                              >{{ candidate.protocol.toUpperCase() }}://{{
                                endpointAddress(candidate)
                              }}:{{ candidate.port }}{{ candidate.path }}</small
                            ></span
                          >
                          <span class="candidate-meta"
                            ><small>{{
                              candidate.ownerConfidence ? `归属：${candidate.ownerConfidence}` : ''
                            }}</small
                            ><em :class="candidate.registrationSuggestion">{{
                              suggestionLabel(candidate)
                            }}</em></span
                          >
                          <Icon
                            :icon="
                              sourceGroup.key !== 'ignored' &&
                              candidate.registrationSuggestion === 'available'
                                ? 'solar:alt-arrow-right-linear'
                                : 'solar:forbidden-circle-linear'
                            "
                            aria-hidden="true"
                          />
                        </button>
                        <button
                          class="candidate-ignore"
                          type="button"
                          :aria-label="
                            sourceGroup.key === 'ignored'
                              ? `恢复 ${candidate.displayName}`
                              : `忽略 ${candidate.displayName}`
                          "
                          @click="
                            sourceGroup.key === 'ignored'
                              ? restoreCandidate(candidate)
                              : ignoreCandidate(candidate)
                          "
                        >
                          <Icon
                            :icon="
                              sourceGroup.key === 'ignored'
                                ? 'solar:undo-left-round-linear'
                                : 'solar:eye-closed-linear'
                            "
                            aria-hidden="true"
                          />{{ sourceGroup.key === 'ignored' ? '恢复' : '忽略' }}
                        </button>
                      </div>
                    </section>
                  </div>
                </section>
              </div>
              <div v-else class="filtered-empty">
                <Icon icon="solar:eye-closed-linear" aria-hidden="true" />
                <span
                  ><strong>当前没有显示中的服务</strong
                  ><small>已忽略的服务仍保留在“已忽略”分组中。</small></span
                >
                <button class="secondary-button" type="button" @click="showAllDiscoveryCandidates">
                  展开全部
                </button>
              </div>
            </div>
            <div v-else class="discovery-empty">
              <Icon icon="solar:radar-2-linear" /><span>{{
                scanning ? '正在识别可访问的 Web 服务…' : '暂无可选服务，可重新扫描或手动填写。'
              }}</span>
            </div>
          </section>

          <form v-else-if="creatorStep === 'review'" class="review-step" @submit.prevent="submit">
            <div v-if="busy === 'submit'" class="submit-progress" role="status" aria-live="polite">
              <Icon class="loader" icon="solar:refresh-linear" aria-hidden="true" />
              <span
                ><strong>正在提交 fnOS 应用中心</strong
                ><small>正在生成、安装并校验桌面入口，请勿关闭窗口。</small></span
              >
            </div>
            <div v-if="error" class="review-error" role="alert">
              <Icon icon="solar:danger-triangle-linear" aria-hidden="true" />
              <span>{{ error }}</span>
            </div>
            <div class="form-columns">
              <label
                ><span>显示名称</span
                ><input
                  v-model="form.displayName"
                  required
                  maxlength="80"
                  placeholder="例如：本地服务"
                  @input="scheduleEntryIDSuggestion"
              /></label>
              <label
                ><span>访问路径</span
                ><input
                  v-model="form.path"
                  required
                  maxlength="512"
                  placeholder="/"
                  @input="schedulePathIconDiscovery"
              /></label>
              <label
                ><span>说明</span
                ><input v-model="form.description" maxlength="500" placeholder="留空则使用显示名称"
              /></label>
              <label
                ><span class="field-label-with-info"
                  >fnOS 入口 ID
                  <span class="info-tip" tabindex="0" aria-label="fnID 创建规则说明">
                    <Icon icon="solar:info-circle-linear" aria-hidden="true" />
                    <span role="tooltip"
                      >{{ entryRulePreview }}；支持中文转无声调拼音、英文小写化和特殊符号过滤。完整
                      fnOS ID 按全局模板 {{ settings.entryPrefixTemplate }} 生成，最终访问域名仍由
                      fnOS 管理。</span
                    >
                  </span></span
                ><input
                  v-model="form.entryPrefix"
                  maxlength="27"
                  pattern="[a-z](?:[a-z0-9-]{0,25}[a-z0-9])?"
                  :aria-invalid="!entryPrefixValid"
                  placeholder="留空则自动生成"
                  @input="onEntryIDInput"
                /><small class="field-help"
                  >应用标识：<code>{{ entryNamePreview }}</code></small
                ></label
              >
              <fieldset class="open-type-field">
                <legend>打开方式</legend>
                <div class="segmented-control" role="radiogroup" aria-label="打开方式">
                  <button
                    :class="{ active: form.openType === 'url' }"
                    type="button"
                    role="radio"
                    data-value="url"
                    :aria-checked="form.openType === 'url'"
                    @click="form.openType = 'url'"
                  >
                    URL 独立打开
                  </button>
                  <button
                    :class="{ active: form.openType === 'iframe' }"
                    type="button"
                    role="radio"
                    data-value="iframe"
                    :aria-checked="form.openType === 'iframe'"
                    @click="form.openType = 'iframe'"
                  >
                    嵌入窗口
                  </button>
                </div>
              </fieldset>
              <div class="form-grid">
                <label
                  ><span>协议</span
                  ><select v-model="form.protocol">
                    <option value="http">HTTP</option>
                    <option value="https">HTTPS</option>
                  </select></label
                ><label
                  ><span>端口</span
                  ><input v-model.number="form.port" required type="number" min="1" max="65535"
                /></label>
              </div>
            </div>
            <div class="icon-source">
              <div class="icon-preview-wrap">
                <img :src="iconPreview || dockfnLogo" alt="应用图标预览" /><img
                  v-if="formShowsBadge"
                  class="dockfn-badge"
                  :src="dockfnLogo"
                  alt="DockFN 角标"
                />
              </div>
              <div class="icon-source-controls">
                <span class="field-title">应用图标</span>
                <small class="icon-cache-help">如果桌面图标未更新，请清除缓存后重新刷新。</small>
                <button
                  class="icon-discover-button"
                  type="button"
                  :disabled="iconDiscoveryStatus === 'loading' || !!busy"
                  @click="rediscoverPathIcon"
                >
                  <Icon
                    :class="{ loader: iconDiscoveryStatus === 'loading' }"
                    :icon="
                      iconDiscoveryStatus === 'loading'
                        ? 'solar:refresh-linear'
                        : 'solar:radar-2-linear'
                    "
                    aria-hidden="true"
                  />{{ iconDiscoveryStatus === 'loading' ? '识别中…' : '从路径识别' }}
                </button>
                <label class="file-button"
                  ><Icon icon="solar:upload-minimalistic-linear" />选择图片<input
                    type="file"
                    accept="image/png,image/jpeg,image/x-icon,image/vnd.microsoft.icon,.ico"
                    @change="selectIcon"
                /></label>
                <label class="uri-input"
                  ><Icon icon="solar:link-linear" /><input
                    v-model="form.iconUri"
                    type="text"
                    placeholder="/favicon.ico、127.0.0.1:8080/favicon.ico 或完整 URI"
                    @input="onIconURIInput"
                    @change="refreshIconURI"
                /></label>
                <button v-if="iconPreview" class="text-button" type="button" @click="removeIcon">
                  清除图标
                </button>
                <small
                  v-if="iconDiscoveryStatus === 'found'"
                  class="icon-discovery-status found"
                  role="status"
                  >已按当前路径识别</small
                >
                <small
                  v-else-if="iconDiscoveryStatus === 'missing'"
                  class="icon-discovery-status missing"
                  role="status"
                  >当前页面未声明可用图标，已保留现有或默认图标</small
                >
              </div>
            </div>
            <label class="toggle-line"
              ><input v-model="form.allUsers" type="checkbox" /><span
                ><strong>所有 fnOS 用户可见</strong><small>关闭时仅管理员可见。</small></span
              ></label
            >
            <footer class="dialog-actions">
              <button
                v-if="!editing"
                class="secondary-button"
                type="button"
                :disabled="!!busy"
                @click="backToDiscovery"
              >
                <Icon icon="solar:arrow-left-linear" />返回发现
              </button>
              <button
                class="secondary-button"
                type="button"
                :disabled="!!busy"
                @click="creatorOpen = false"
              >
                取消
              </button>
              <button class="primary-button" type="submit" :disabled="!!busy || !entryPrefixValid">
                <Icon
                  :class="{ loader: busy === 'submit' }"
                  :icon="busy === 'submit' ? 'solar:refresh-linear' : 'solar:check-circle-linear'"
                />{{
                  busy === 'submit' ? '正在创建…' : editing ? '保存更新' : '确认创建 DockFN 应用'
                }}
              </button>
            </footer>
          </form>
          <section v-else class="completion-step" aria-live="polite">
            <div v-if="busy === 'submit'" class="completion-progress" role="status">
              <div class="completion-loader">
                <span class="completion-spinner" aria-hidden="true"></span>
                <img :src="iconPreview || dockfnLogo" alt="" />
              </div>
              <p class="eyebrow">INSTALLING FNOS ENTRY</p>
              <h3>正在创建应用入口</h3>
              <p>正在生成应用壳、提交 fnOS 应用中心并校验入口，请勿关闭窗口。</p>
              <div class="completion-progress-line" aria-hidden="true"><span></span></div>
            </div>
            <div v-else-if="completedApp" class="completion-card">
              <div class="completion-visual">
                <div class="completion-icon-wrap">
                  <img :src="completedApp?.iconDataUrl || dockfnLogo" alt="" />
                  <img
                    v-if="appShowsBadge(completedApp)"
                    class="dockfn-badge"
                    :src="dockfnLogo"
                    alt="DockFN 角标"
                  />
                </div>
                <Icon class="completion-check" icon="solar:check-circle-bold" aria-hidden="true" />
              </div>
              <p class="eyebrow">FNOS REGISTRATION COMPLETE</p>
              <h3>应用创建完成</h3>
              <p>
                <strong>{{ completedApp?.displayName }}</strong>
                已生成应用壳并通过 fnOS 入口校验。
              </p>
              <dl v-if="completedApp" class="completion-details">
                <div>
                  <dt>入口 ID</dt>
                  <dd>{{ completedApp.appName }}</dd>
                </div>
                <div>
                  <dt>目标服务</dt>
                  <dd>
                    {{ completedApp.protocol.toUpperCase() }} · {{ completedApp.port
                    }}{{ completedApp.path }}
                  </dd>
                </div>
                <div>
                  <dt>最终域名</dt>
                  <dd>由 fnOS 生成，请以 fnOS 应用中心显示为准</dd>
                </div>
              </dl>
              <footer class="completion-actions">
                <button
                  class="secondary-button"
                  type="button"
                  data-completion-action="close"
                  @click="creatorOpen = false"
                >
                  关闭
                </button>
                <button
                  class="primary-button"
                  type="button"
                  data-completion-action="continue"
                  @click="beginCreate"
                >
                  <Icon icon="solar:add-circle-linear" aria-hidden="true" />继续创建
                </button>
              </footer>
            </div>
          </section>
        </section>
      </div>
    </Transition>

    <Transition name="fade">
      <div v-if="pendingIconSync" class="overlay modal-overlay">
        <section
          class="confirm-dialog icon-sync-dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby="icon-sync-title"
        >
          <template v-if="busy === `${pendingIconSync.id}:refresh-icon`">
            <Icon class="loader" icon="solar:refresh-linear" aria-hidden="true" />
            <h2 id="icon-sync-title">正在同步桌面图标</h2>
            <p>
              正在重新生成并安装“{{ pendingIconSync.displayName }}”的 fnOS 应用入口，请勿关闭窗口。
            </p>
          </template>
          <template v-else-if="iconSyncCompleted">
            <Icon class="success-icon" icon="solar:check-circle-linear" aria-hidden="true" />
            <h2 id="icon-sync-title">桌面入口已同步</h2>
            <p>
              fnOS
              桌面图标可能会短暂刷新。若仍显示旧图标或暂未出现，请关闭并重新打开桌面；必要时刷新页面或清除图片缓存。
            </p>
          </template>
          <template v-else>
            <Icon class="warning-icon" icon="solar:refresh-circle-linear" aria-hidden="true" />
            <h2 id="icon-sync-title">同步“{{ pendingIconSync.displayName }}”的桌面图标？</h2>
            <p>
              将按当前全局角标配置重新生成并安装 fnOS
              应用入口。同步期间，桌面图标可能会短暂消失；不会操作目标服务、Docker
              容器、存储卷或业务数据。
            </p>
          </template>
          <div v-if="error" class="review-error icon-sync-error" role="alert">
            <Icon icon="solar:danger-triangle-linear" aria-hidden="true" />
            <span>{{ error }}</span>
          </div>
          <footer class="dialog-actions">
            <button
              v-if="!iconSyncCompleted"
              class="secondary-button"
              type="button"
              :disabled="!!busy"
              @click="closeIconSync"
            >
              取消
            </button>
            <button
              v-if="!iconSyncCompleted && busy !== `${pendingIconSync.id}:refresh-icon`"
              class="primary-button"
              type="button"
              @click="syncDesktopIconConfirmed"
            >
              <Icon icon="solar:refresh-linear" aria-hidden="true" />开始同步
            </button>
            <button
              v-if="iconSyncCompleted"
              class="primary-button"
              type="button"
              @click="closeIconSync"
            >
              完成
            </button>
          </footer>
        </section>
      </div>
    </Transition>

    <Transition name="fade">
      <div v-if="diagnosticsOpen" class="overlay modal-overlay" @click.self="closeDiagnostics">
        <section
          class="diagnostics-dialog"
          role="dialog"
          aria-modal="true"
          aria-labelledby="diagnostics-title"
        >
          <header class="dialog-head">
            <div>
              <h2 id="diagnostics-title">DockFN 诊断</h2>
            </div>
            <div class="diagnostics-head-actions">
              <button
                class="secondary-button diagnostics-clear-button"
                type="button"
                aria-label="清空诊断历史"
                :disabled="!!busy || diagnosticItems.length === 0"
                @click="pendingDiagnosticsClear = true"
              >
                <Icon icon="solar:trash-bin-minimalistic-linear" />清空记录
              </button>
              <button
                class="icon-action"
                type="button"
                aria-label="关闭"
                title="关闭"
                @click="closeDiagnostics"
              >
                <Icon icon="solar:close-circle-linear" />
              </button>
            </div>
          </header>
          <div class="diagnostics-summary">
            <p>显示最近扫描、最近安装失败及有内容的运行日志；敏感字段已脱敏。</p>
            <div v-if="diagnosticNotice" class="diagnostic-notice" role="status">
              {{ diagnosticNotice }}
            </div>
          </div>
          <div v-if="diagnosticsLoading" class="diagnostics-loading">
            <Icon class="loader" icon="solar:refresh-linear" />正在读取诊断信息…
          </div>
          <div
            v-else-if="diagnosticsError"
            class="diagnostics-empty diagnostics-error"
            role="alert"
          >
            <Icon icon="solar:danger-triangle-linear" aria-hidden="true" />
            <span
              ><strong>无法读取诊断信息</strong><small>{{ diagnosticsError }}</small></span
            >
            <button
              class="secondary-button"
              type="button"
              data-diagnostics-action="retry"
              @click="openDiagnostics"
            >
              <Icon icon="solar:restart-linear" aria-hidden="true" />重试
            </button>
          </div>
          <div v-else-if="diagnosticItems.length === 0" class="diagnostics-empty">
            <Icon icon="solar:document-text-linear" aria-hidden="true" />
            <span
              ><strong>暂无诊断记录</strong
              ><small>扫描或出现安装异常后，相关记录会显示在这里。</small></span
            >
          </div>
          <div v-else class="diagnostic-logs">
            <article v-for="log in diagnosticItems" :key="log.name" class="diagnostic-row">
              <div class="diagnostic-file">
                <Icon icon="solar:document-text-linear" aria-hidden="true" />
                <div>
                  <strong>{{ diagnosticTitle(log.name) }}</strong>
                  <small>{{ diagnosticDescription(log.name) }}</small>
                </div>
              </div>
              <button
                class="log-action"
                type="button"
                :aria-label="`查看 ${diagnosticTitle(log.name)}`"
                @click="openDiagnostic(log)"
              >
                <Icon icon="solar:maximize-square-3-linear" />查看
              </button>
            </article>
          </div>
        </section>
      </div>
    </Transition>

    <Transition name="fade">
      <div
        v-if="pendingDiagnosticsClear"
        class="overlay diagnostics-clear-overlay"
        @click.self="pendingDiagnosticsClear = false"
      >
        <section
          class="confirm-dialog"
          role="alertdialog"
          aria-modal="true"
          aria-labelledby="clear-diagnostics-title"
        >
          <Icon class="warning-icon" icon="solar:danger-triangle-linear" />
          <h2 id="clear-diagnostics-title">清空 DockFN 诊断历史？</h2>
          <p>
            只会清理 DockFN 保存的运行日志、最近扫描和安装失败记录。<strong
              >不会清理 fnOS、应用中心、Docker 或目标服务日志。</strong
            >
            历史内容无法恢复。
          </p>
          <footer class="dialog-actions">
            <button
              class="secondary-button"
              type="button"
              :disabled="!!busy"
              @click="pendingDiagnosticsClear = false"
            >
              取消</button
            ><button
              class="danger-button"
              type="button"
              :disabled="!!busy"
              @click="clearDiagnosticsConfirmed"
            >
              <Icon
                :class="{ loader: busy === 'clear-diagnostics' }"
                :icon="
                  busy === 'clear-diagnostics'
                    ? 'solar:refresh-linear'
                    : 'solar:trash-bin-trash-linear'
                "
              />{{ busy === 'clear-diagnostics' ? '正在清空…' : '确认清空记录' }}
            </button>
          </footer>
        </section>
      </div>
    </Transition>

    <Transition name="fade">
      <div
        v-if="selectedDiagnostic"
        class="overlay log-viewer-overlay"
        @click.self="selectedDiagnostic = null"
      >
        <section
          class="log-viewer-dialog"
          role="dialog"
          aria-modal="true"
          :aria-label="`${selectedDiagnostic.name} 完整内容`"
        >
          <header class="dialog-head">
            <div>
              <h2>{{ diagnosticTitle(selectedDiagnostic.name) }}</h2>
            </div>
            <div class="log-viewer-actions">
              <span v-if="diagnosticNotice" class="log-copy-status" role="status">{{
                diagnosticNotice
              }}</span>
              <button
                class="secondary-button"
                type="button"
                :aria-label="`复制完整 ${diagnosticTitle(selectedDiagnostic.name)}`"
                @click="copyDiagnostic(selectedDiagnostic)"
              >
                <Icon icon="solar:copy-linear" />复制全文
              </button>
              <button
                class="icon-action"
                type="button"
                aria-label="关闭独立日志窗口"
                title="关闭日志查看"
                @click="selectedDiagnostic = null"
              >
                <Icon icon="solar:close-circle-linear" />
              </button>
            </div>
          </header>
          <pre>{{ selectedDiagnostic.text || '日志文件为空。' }}</pre>
        </section>
      </div>
    </Transition>

    <Transition name="fade">
      <div v-if="pendingRemoval" class="overlay modal-overlay" @click.self="pendingRemoval = null">
        <section
          class="confirm-dialog"
          role="alertdialog"
          aria-modal="true"
          aria-labelledby="remove-title"
        >
          <Icon class="warning-icon" icon="solar:danger-triangle-linear" />
          <h2 id="remove-title">移除“{{ pendingRemoval.displayName }}”？</h2>
          <p>
            该操作只会卸载 DockFN 创建的 fnOS 应用入口，<strong
              >不会停止或删除目标服务、Docker 容器、存储卷及业务数据。</strong
            >
          </p>
          <footer class="dialog-actions">
            <button
              class="secondary-button"
              type="button"
              :disabled="!!busy"
              @click="pendingRemoval = null"
            >
              取消</button
            ><button
              class="danger-button"
              type="button"
              :disabled="!!busy"
              @click="removeConfirmed"
            >
              <Icon icon="solar:trash-bin-trash-linear" />移除应用入口
            </button>
          </footer>
        </section>
      </div>
    </Transition>
  </div>
</template>
