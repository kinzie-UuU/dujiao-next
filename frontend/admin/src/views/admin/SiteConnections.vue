<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'
import type { AdminSiteConnection, AdminReadOnlySupplierBinding } from '@/api/types'
import IdCell from '@/components/IdCell.vue'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Dialog, DialogScrollContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table'
import TableSkeleton from '@/components/TableSkeleton.vue'
import ListPagination from '@/components/ListPagination.vue'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { confirmAction } from '@/utils/confirm'
import { notifyError, notifySuccess } from '@/utils/notify'
import { useFormValidation, rules } from '@/composables/useFormValidation'

import { fangfangProtocol, fangfangOrigin, fangfangKeyMarker, fangfangTargets, protectFangfangPayload, parseFangfangKey } from '@/utils/fangfangSupplier'

const { t, locale } = useI18n()

const fangfangCopy = [
  {
    "readOnly": "只读 · 采购关闭",
    "bindings": "只读商品关联",
    "apiKey": "Fang Fang Global API 密钥",
    "keyFile": "从本地密钥文件读取（可选）",
    "keyHint": "文件只在浏览器本地读取；保存时密钥仅发送到本站后台加密保存。编辑时留空保留现有密钥。",
    "keyLoaded": "已读取密钥，尚未保存",
    "keyInvalid": "请选择仅含 API 密钥的小型文本文件，或检查输入。",
    "formHint": "只查询 USD 余额、商品和库存。保存后连接保持禁用，不启用采购，不同步或重算本店售价。",
    "bindingHint": "查看供货价和库存，保留现有商品售价、库存、交付方式及其他货源。Fang Fang 自动采购保持关闭。",
    "account": "成品号",
    "bind": "建立只读关联",
    "refresh": "刷新报价与库存",
    "quote": "供货价",
    "stock": "上游库存",
    "checkedAt": "查询时间",
    "notBound": "尚未关联",
    "loadFailed": "读取关联失败，请重试。",
    "saveFailed": "操作未完成，请检查连接后重试；不会下单。",
    "reload": "重试",
    "plusName": "Plus 30天全质保成品号"
  },
  {
    "readOnly": "唯讀 · 採購關閉",
    "bindings": "唯讀商品關聯",
    "apiKey": "Fang Fang Global API 金鑰",
    "keyFile": "從本機金鑰檔案讀取（選填）",
    "keyHint": "檔案只在瀏覽器本機讀取；儲存時金鑰僅傳送至本站後台加密儲存。編輯時留空保留現有金鑰。",
    "keyLoaded": "已讀取金鑰，尚未儲存",
    "keyInvalid": "請選擇僅含 API 金鑰的小型文字檔，或檢查輸入。",
    "formHint": "只查詢 USD 餘額、商品和庫存。儲存後連線保持停用，不啟用採購，不同步或重算本店售價。",
    "bindingHint": "查看供貨價和庫存，保留現有商品售價、庫存、交付方式及其他貨源。Fang Fang 自動採購保持關閉。",
    "account": "成品帳號",
    "bind": "建立唯讀關聯",
    "refresh": "更新報價與庫存",
    "quote": "供貨價",
    "stock": "上游庫存",
    "checkedAt": "查詢時間",
    "notBound": "尚未關聯",
    "loadFailed": "讀取關聯失敗，請重試。",
    "saveFailed": "操作未完成，請檢查連線後重試；不會下單。",
    "reload": "重試",
    "plusName": "Plus 30天全保固成品帳號"
  },
  {
    "readOnly": "Read only · Purchasing off",
    "bindings": "Read-only product links",
    "apiKey": "Fang Fang Global API key",
    "keyFile": "Read a local key file (optional)",
    "keyHint": "The file is read locally. Saving sends the key only to this store for encrypted storage. Leave blank when editing to retain the current key.",
    "keyLoaded": "Key loaded, not saved yet",
    "keyInvalid": "Choose a small text file containing only the API key, or check the key entered.",
    "formHint": "Queries USD balance, products and stock only. Saving keeps the connection disabled. Purchasing and store price updates stay off.",
    "bindingHint": "View supplier prices and stock. Existing store prices, inventory, fulfillment and other suppliers remain unchanged. Fang Fang purchasing stays off.",
    "account": "Ready-made account",
    "bind": "Link for read-only queries",
    "refresh": "Refresh price and stock",
    "quote": "Supplier price",
    "stock": "Supplier stock",
    "checkedAt": "Checked at",
    "notBound": "Not linked yet",
    "loadFailed": "Could not load links. Try again.",
    "saveFailed": "The operation did not finish. Check the connection and retry. No order will be placed.",
    "reload": "Retry",
    "plusName": "Plus 30-day full-warranty account"
  }
] as const
const ff = (key: keyof typeof fangfangCopy[0]) => fangfangCopy[locale.value === "en-US" ? 2 : locale.value === "zh-TW" ? 1 : 0][key]

const loading = ref(true)
const connections = ref<AdminSiteConnection[]>([])
const pagination = reactive({
  page: 1,
  page_size: 20,
  total: 0,
  total_page: 1,
})
const showModal = ref(false)
const isEditing = ref(false)
const editingId = ref<number | null>(null)
const pingingId = ref<number | null>(null)

const form = reactive({
  name: '',
  base_url: '',
  api_key: '',
  api_secret: '',
  protocol: 'dujiao-next',
  callback_url: '',
  retry_max: 3,
  retry_intervals: '30,60,120',
  exchange_rate: 1,
  price_markup_percent: 0,
  price_rounding_mode: 'none',
  auto_sync_price: 'false',
})
const reapplyingId = ref<number | null>(null)
const submitting = ref(false)
const isFangfang = computed(() => form.protocol === fangfangProtocol)
const isFangfangConnection = (conn: AdminSiteConnection) => conn.protocol === fangfangProtocol
const bindingConnection = ref<AdminSiteConnection | null>(null)
const showBindings = ref(false)
const bindings = ref<AdminReadOnlySupplierBinding[]>([])
const bindingsLoading = ref(false)
const bindingBusy = ref<number | null>(null)
const bindingError = ref('')
const keyFileBusy = ref(false)

const readKeyFile = async (event: Event) => {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  if (!file) return
  keyFileBusy.value = true
  try {
    if (file.size > 4096) throw new Error('Key file too large')
    const text = await file.text()
    if (!showModal.value || form.protocol !== fangfangProtocol) return
    form.api_secret = parseFangfangKey(text)
    notifySuccess(ff('keyLoaded'))
  } catch {
    form.api_secret = ''
    notifyError(ff('keyInvalid'))
  } finally {
    input.value = ''
    keyFileBusy.value = false
  }
}

const loadBindings = async () => {
  if (!bindingConnection.value) return
  bindingsLoading.value = true
  bindingError.value = ''
  try {
    const response = await adminAPI.getReadOnlySupplierBindings(bindingConnection.value.id)
    bindings.value = response.data.data || []
  } catch {
    bindings.value = []
    bindingError.value = ff('loadFailed')
  } finally {
    bindingsLoading.value = false
  }
}
const openBindings = async (conn: AdminSiteConnection) => {
  if (!isFangfangConnection(conn)) return
  bindingConnection.value = conn
  bindings.value = []
  showBindings.value = true
  await loadBindings()
}
const bindingFor = (upstreamId: number) => bindings.value.find((row) => row.upstream_product_id === upstreamId)
const saveBinding = async (target: typeof fangfangTargets[number]) => {
  if (!bindingConnection.value || bindingBusy.value !== null || bindingError.value) return
  bindingBusy.value = target.upstream_product_id
  try {
    const existing = bindingFor(target.upstream_product_id)
    if (existing) await adminAPI.refreshReadOnlySupplierBinding(existing.id)
    else await adminAPI.bindReadOnlySupplier({ connection_id: bindingConnection.value.id, local_product_id: target.local_product_id, local_sku_id: target.local_sku_id, upstream_product_id: target.upstream_product_id })
    await loadBindings()
  } catch {
    notifyError(ff('saveFailed'))
  } finally {
    bindingBusy.value = null
  }
}

const siteConnectionSchema = {
  name: [rules.required()],
  base_url: [rules.required(), rules.url()],
  api_key: [rules.required()],
}
const { errors, validate, clearErrors } = useFormValidation(siteConnectionSchema)

const fetchConnections = async (page = 1) => {
  loading.value = true
  try {
    const res = await adminAPI.getSiteConnections({
      page,
      page_size: pagination.page_size,
    })
    connections.value = res.data.data || []
    const p = res.data.pagination
    if (p) {
      pagination.page = p.page
      pagination.page_size = p.page_size
      pagination.total = p.total
      pagination.total_page = p.total_page
    }
  } catch {
    connections.value = []
  } finally {
    loading.value = false
  }
}

const changePage = (page: number) => {
  if (page < 1 || page > pagination.total_page) return
  fetchConnections(page)
}

const pageSizeOptions = [10, 20, 50, 100]

const changePageSize = (size: number) => {
  if (size === pagination.page_size) return
  pagination.page_size = size
  fetchConnections(1)
}

const resetForm = () => {
  Object.assign(form, {
    name: '',
    base_url: '',
    api_key: '',
    api_secret: '',
    protocol: 'dujiao-next',
    callback_url: '',
    retry_max: 3,
    retry_intervals: '30,60,120',
    exchange_rate: 1,
    price_markup_percent: 0,
    price_rounding_mode: 'none',
    auto_sync_price: 'false',
  })
}

const openCreateModal = () => {
  isEditing.value = false
  editingId.value = null
  clearErrors()
  resetForm()
  showModal.value = true
}

const openEditModal = (conn: AdminSiteConnection) => {
  isEditing.value = true
  editingId.value = conn.id
  Object.assign(form, {
    name: conn.name || '',
    base_url: conn.base_url || '',
    api_key: conn.api_key || '',
    api_secret: conn.api_secret || '',
    protocol: conn.protocol || 'dujiao-next',
    callback_url: conn.callback_url || '',
    retry_max: conn.retry_max ?? 3,
    retry_intervals: (() => {
      const ri = conn.retry_intervals
      if (Array.isArray(ri)) return ri.join(',')
      if (typeof ri === 'string') {
        try {
          const parsed = JSON.parse(ri)
          if (Array.isArray(parsed)) return parsed.join(',')
        } catch { /* ignore */ }
        return ri
      }
      return '30,60,120'
    })(),
    exchange_rate: conn.exchange_rate ?? 1,
    price_markup_percent: conn.price_markup_percent ?? 0,
    price_rounding_mode: conn.price_rounding_mode || 'none',
    auto_sync_price: conn.auto_sync_price ? 'true' : 'false',
  })
  showModal.value = true
}

const closeModal = () => {
  showModal.value = false
  clearErrors()
  form.api_secret = ''
}

const newzoeOrigin = 'https://shop.newzoe.cloud'
const applyProtocolDefaults = (protocol: unknown) => {
  form.api_secret = ''
  if (protocol === fangfangProtocol) {
    form.base_url = fangfangOrigin
    form.api_key = fangfangKeyMarker
    form.callback_url = ''
    form.exchange_rate = 1
    form.price_markup_percent = 0
    form.price_rounding_mode = 'none'
    form.auto_sync_price = 'false'
    if (!form.name) form.name = 'Fang Fang Global'
  } else if (protocol === 'newzoe') {
    form.base_url = newzoeOrigin
    form.callback_url = ''
    form.price_markup_percent = 60
    form.price_rounding_mode = 'ceil_int'
    form.auto_sync_price = 'true'
  } else if (form.base_url === newzoeOrigin || form.base_url === fangfangOrigin) {
    form.base_url = ''
    form.api_key = ''
    form.price_markup_percent = 0
    form.price_rounding_mode = 'none'
    form.auto_sync_price = 'false'
  }
}

const buildPayload = () => {
  const intervals = form.retry_intervals
    .split(',')
    .map((s: string) => Number(s.trim()))
    .filter((n: number) => !Number.isNaN(n) && n > 0)
  return protectFangfangPayload({
    name: form.name,
    base_url: form.base_url,
    api_key: form.api_key,
    api_secret: form.api_secret,
    protocol: form.protocol,
    callback_url: form.callback_url,
    retry_max: Number(form.retry_max) || 3,
    retry_intervals: JSON.stringify(intervals),
    exchange_rate: Number(form.exchange_rate) || 1,
    price_markup_percent: Number(form.price_markup_percent) || 0,
    price_rounding_mode: form.price_rounding_mode,
    auto_sync_price: form.auto_sync_price === 'true',
  })
}

const handleSubmit = async () => {
  if (submitting.value || keyFileBusy.value) return
  if (isFangfang.value) {
    form.base_url = fangfangOrigin
    form.api_key = fangfangKeyMarker
    try {
      if (!isEditing.value || form.api_secret) form.api_secret = parseFangfangKey(form.api_secret)
    } catch { notifyError(ff('keyInvalid')); return }
  }
  if (!validate({ ...form } as Record<string, unknown>)) return
  submitting.value = true
  try {
    const payload = buildPayload()
    if (isEditing.value && editingId.value) {
      await adminAPI.updateSiteConnection(editingId.value, payload)
    } else {
      const created = await adminAPI.createSiteConnection(payload)
      if (isFangfang.value) {
        const id = Number(created.data.data?.id)
        if (!Number.isSafeInteger(id) || id <= 0) throw new Error('Missing connection ID')
        // Keep the created ID even if disabling fails, so retry never duplicates it.
        editingId.value = id
        isEditing.value = true
      }
    }
    if (isFangfang.value && editingId.value) {
      await adminAPI.updateSiteConnectionStatus(editingId.value, { status: 'disabled' })
    }
    closeModal()
    fetchConnections(isEditing.value ? pagination.page : 1)
    notifySuccess()
  } catch (err: any) {
    notifyError(isFangfang.value ? ff('saveFailed') : err?.response?.data?.message || err?.message)
  } finally {
    submitting.value = false
  }
}

const handlePing = async (conn: AdminSiteConnection) => {
  pingingId.value = conn.id
  try {
    if (isFangfangConnection(conn)) await adminAPI.updateSiteConnectionStatus(conn.id, { status: 'disabled' })
    await adminAPI.pingSiteConnection(conn.id)
    notifySuccess(t('siteConnections.ping.success'))
    fetchConnections(pagination.page)
  } catch (err: any) {
    notifyError(t('siteConnections.ping.failed') + ': ' + (err?.response?.data?.message || err?.message || ''))
  } finally {
    pingingId.value = null
  }
}

const handleToggleStatus = async (conn: AdminSiteConnection) => {
  if (isFangfangConnection(conn)) return
  const nextStatus = conn.status === 'active' ? 'disabled' : 'active'
  try {
    await adminAPI.updateSiteConnectionStatus(conn.id, { status: nextStatus })
    fetchConnections(pagination.page)
    notifySuccess()
  } catch (err: any) {
    notifyError(err?.response?.data?.message || err?.message)
  }
}

const handleDelete = async (conn: AdminSiteConnection) => {
  const confirmed = await confirmAction({
    description: t('siteConnections.delete.confirm', { name: conn.name || '#' + conn.id }),
    confirmText: t('admin.common.delete'),
    variant: 'destructive',
  })
  if (!confirmed) return
  try {
    await adminAPI.deleteSiteConnection(conn.id)
    fetchConnections(pagination.page)
    notifySuccess()
  } catch (err: any) {
    notifyError(err?.response?.data?.message || err?.message)
  }
}

const handleReapplyMarkup = async (conn: AdminSiteConnection) => {
  if (isFangfangConnection(conn)) return
  const confirmed = await confirmAction({
    description: t('siteConnections.reapplyMarkup.confirm', { name: conn.name || '#' + conn.id }),
    confirmText: t('siteConnections.actions.reapplyMarkup'),
  })
  if (!confirmed) return
  reapplyingId.value = conn.id
  try {
    const res = await adminAPI.reapplyConnectionMarkup(conn.id)
    const count = (res.data?.data as Record<string, number>)?.updated_products ?? 0
    notifySuccess(t('siteConnections.reapplyMarkup.success', { count }))
  } catch (err: any) {
    notifyError(err?.response?.data?.message || err?.message)
  } finally {
    reapplyingId.value = null
  }
}

const statusBadgeClass = (status?: string) => {
  switch (status) {
    case 'active':
      return 'text-emerald-700 border-emerald-200 bg-emerald-50'
    case 'pending':
      return 'text-yellow-700 border-yellow-200 bg-yellow-50'
    default:
      return 'text-muted-foreground border-border bg-muted/30'
  }
}

const statusLabel = (status?: string) => {
  const key = `siteConnections.status.${status}`
  const translated = t(key)
  return translated !== key ? translated : status
}

const formatTime = (raw?: string) => {
  if (!raw) return '-'
  const d = new Date(raw)
  if (Number.isNaN(d.getTime())) return '-'
  return d.toLocaleString()
}

onMounted(() => {
  fetchConnections()
})
</script>

<template>
  <div class="space-y-6">
    <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
      <h1 class="text-2xl font-semibold">{{ t('siteConnections.title') }}</h1>
      <Button class="w-full sm:w-auto" @click="openCreateModal">{{ t('siteConnections.createButton') }}</Button>
    </div>

    <div class="rounded-xl border border-border bg-card overflow-x-auto">
      <Table class="min-w-[900px]">
        <TableHeader class="border-b border-border bg-muted/40 text-xs uppercase text-muted-foreground">
          <TableRow>
            <TableHead class="px-6 py-3">{{ t('siteConnections.columns.id') }}</TableHead>
            <TableHead class="min-w-[160px] px-6 py-3">{{ t('siteConnections.columns.name') }}</TableHead>
            <TableHead class="min-w-[200px] px-6 py-3">{{ t('siteConnections.columns.baseUrl') }}</TableHead>
            <TableHead class="min-w-[80px] px-6 py-3">{{ t('siteConnections.columns.protocol') }}</TableHead>
            <TableHead class="min-w-[80px] px-6 py-3">{{ t('siteConnections.columns.markup') }}</TableHead>
            <TableHead class="min-w-[90px] px-6 py-3">{{ t('siteConnections.columns.status') }}</TableHead>
            <TableHead class="min-w-[80px] px-6 py-3">{{ t('siteConnections.columns.lastPing') }}</TableHead>
            <TableHead class="min-w-[200px] px-6 py-3 text-right">{{ t('siteConnections.columns.actions') }}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody class="divide-y divide-border">
          <TableRow v-if="loading">
            <TableCell :colspan="8" class="p-0">
              <TableSkeleton :columns="8" :rows="5" />
            </TableCell>
          </TableRow>
          <TableRow v-else-if="connections.length === 0">
            <TableCell colspan="8" class="px-6 py-8 text-center text-muted-foreground">{{ t('siteConnections.empty') }}</TableCell>
          </TableRow>
          <TableRow v-for="conn in connections" :key="conn.id" class="hover:bg-muted/30">
            <TableCell class="px-6 py-4">
              <IdCell :value="conn.id" />
            </TableCell>
            <TableCell class="min-w-[160px] px-6 py-4 font-medium text-foreground break-words">{{ conn.name }}</TableCell>
            <TableCell class="min-w-[200px] px-6 py-4 text-xs text-muted-foreground font-mono break-all">{{ conn.base_url }}</TableCell>
            <TableCell class="min-w-[80px] px-6 py-4 text-xs text-muted-foreground break-words">{{ isFangfangConnection(conn) ? 'Fang Fang Global' : conn.protocol }}<p v-if="isFangfangConnection(conn)" class="mt-1 text-xs">{{ ff('readOnly') }}</p></TableCell>
            <TableCell class="min-w-[80px] px-6 py-4 text-xs text-muted-foreground">
              <span v-if="conn.price_markup_percent && Number(conn.price_markup_percent) !== 0" class="inline-flex rounded-full border border-blue-200 bg-blue-50 px-2 py-0.5 text-xs text-blue-700">
                +{{ conn.price_markup_percent }}%
              </span>
              <span v-else class="text-muted-foreground">-</span>
            </TableCell>
            <TableCell class="min-w-[90px] px-6 py-4">
              <span
                class="inline-flex rounded-full border px-2.5 py-1 text-xs"
                :class="statusBadgeClass(conn.status)"
              >
                {{ statusLabel(conn.status) }}
              </span>
            </TableCell>
            <TableCell class="min-w-[80px] px-6 py-4 text-xs text-muted-foreground">{{ formatTime(conn.last_ping_at) }}</TableCell>
            <TableCell class="min-w-[160px] px-6 py-4 text-right">
              <div class="flex flex-wrap items-center justify-end gap-2">
                <Button size="sm" variant="outline" @click="openEditModal(conn)">{{ t('admin.common.edit') }}</Button>
                <Button
                  size="sm"
                  variant="outline"
                  :disabled="pingingId === conn.id"
                  @click="handlePing(conn)"
                >
                  {{ pingingId === conn.id ? t('siteConnections.ping.loading') : 'Ping' }}
                </Button>
                <Button
                  v-if="!isFangfangConnection(conn) && ((conn.price_markup_percent && Number(conn.price_markup_percent) !== 0) || (conn.exchange_rate != null && Number(conn.exchange_rate) !== 1))"
                  size="sm"
                  variant="outline"
                  :disabled="reapplyingId === conn.id"
                  @click="handleReapplyMarkup(conn)"
                >
                  {{ reapplyingId === conn.id ? '...' : t('siteConnections.actions.reapplyMarkup') }}
                </Button>
                <Button v-if="isFangfangConnection(conn)" size="sm" variant="outline" @click="openBindings(conn)">{{ ff('bindings') }}</Button>
                <Button v-else size="sm" variant="outline" @click="handleToggleStatus(conn)">
                  {{ conn.status === 'active' ? t('siteConnections.actions.disable') : t('siteConnections.actions.enable') }}
                </Button>
                <Button size="sm" variant="destructive" @click="handleDelete(conn)">{{ t('admin.common.delete') }}</Button>
              </div>
            </TableCell>
          </TableRow>
        </TableBody>
      </Table>

      <ListPagination
        :page="pagination.page"
        :total-page="pagination.total_page"
        :total="pagination.total"
        :page-size="pagination.page_size"
        :page-size-options="pageSizeOptions"
        @change-page="changePage"
        @change-page-size="changePageSize"
      />
    </div>

    <Dialog v-model:open="showBindings">
      <DialogScrollContent class="w-[calc(100vw-1rem)] max-w-2xl p-4 sm:p-6">
        <DialogHeader><DialogTitle>{{ ff('bindings') }} · {{ bindingConnection?.name }}</DialogTitle></DialogHeader>
        <p class="text-sm text-muted-foreground">{{ ff('bindingHint') }}</p>
        <p v-if="bindingError" role="alert" class="text-sm text-destructive">{{ bindingError }} <Button size="sm" variant="outline" @click="loadBindings">{{ ff('reload') }}</Button></p>
        <div v-for="target in fangfangTargets" :key="target.upstream_product_id" class="space-y-3 border-t border-border pt-4">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-medium">{{ target.upstream_product_id === 2 ? ff('plusName') : target.label + ' · ' + ff('account') }}</h3>
            <Button size="sm" variant="outline" :disabled="bindingsLoading || bindingBusy !== null || !!bindingError" @click="saveBinding(target)">{{ bindingBusy === target.upstream_product_id ? '…' : bindingFor(target.upstream_product_id) ? ff('refresh') : ff('bind') }}</Button>
          </div>
          <template v-if="bindingFor(target.upstream_product_id)">
            <p class="text-sm">{{ ff('quote') }}: {{ bindingFor(target.upstream_product_id)?.upstream_price }} {{ bindingFor(target.upstream_product_id)?.currency }} · {{ ff('stock') }}: {{ bindingFor(target.upstream_product_id)?.upstream_stock }}</p>
            <p class="text-xs text-muted-foreground">{{ ff('checkedAt') }}: {{ formatTime(bindingFor(target.upstream_product_id)?.quoted_at) }}</p>
          </template>
          <p v-else class="text-sm text-muted-foreground">{{ bindingsLoading ? '…' : ff('notBound') }}</p>
        </div>
      </DialogScrollContent>
    </Dialog>

    <Dialog v-model:open="showModal" @update:open="(value: boolean) => { if (!value) closeModal() }">
      <DialogScrollContent class="w-[calc(100vw-1rem)] max-w-2xl p-4 sm:p-6" @interact-outside="(e: Event) => e.preventDefault()">
        <DialogHeader>
          <DialogTitle>{{ isEditing ? t('siteConnections.editTitle') : t('siteConnections.createTitle') }}</DialogTitle>
        </DialogHeader>

        <form class="space-y-6" @submit.prevent="handleSubmit">
          <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
            <div class="md:col-span-2">
              <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.name') }}</label>
              <Input v-model="form.name" required :placeholder="t('siteConnections.form.namePlaceholder')" />
              <p v-if="errors.name" class="text-xs text-destructive mt-1">{{ errors.name }}</p>
            </div>
            <div>
              <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.protocol') }}</label>
              <Select v-model="form.protocol" :disabled="isEditing || keyFileBusy || submitting" @update:model-value="applyProtocolDefaults">
                <SelectTrigger class="h-9 w-full">
                  <SelectValue :placeholder="t('siteConnections.form.protocolPlaceholder')" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="dujiao-next">dujiao-next</SelectItem>
                  <SelectItem value="newzoe">NewZoe Shop API v1</SelectItem>
                  <SelectItem :value="fangfangProtocol">Fang Fang Global · {{ ff('readOnly') }}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div class="md:col-span-2">
              <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.baseUrl') }}</label>
              <Input v-model="form.base_url" :readonly="form.protocol === 'newzoe' || isFangfang" required :placeholder="t('siteConnections.form.baseUrlPlaceholder')" />
              <p v-if="errors.base_url" class="text-xs text-destructive mt-1">{{ errors.base_url }}</p>
            </div>
            <div v-if="!isFangfang">
              <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.apiKey') }}</label>
              <Input v-model="form.api_key" :placeholder="t('siteConnections.form.apiKeyPlaceholder')" />
              <p v-if="errors.api_key" class="text-xs text-destructive mt-1">{{ errors.api_key }}</p>
            </div>
            <div>
              <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ isFangfang ? ff('apiKey') : t('siteConnections.form.apiSecret') }}</label>
              <Input v-model="form.api_secret" type="password" autocomplete="new-password" :placeholder="t('siteConnections.form.apiSecretPlaceholder')" />
              <div v-if="isFangfang" class="mt-2 space-y-2">
                <label for="fangfang-key-file" class="block text-xs text-muted-foreground">{{ ff('keyFile') }}</label>
                <input id="fangfang-key-file" type="file" class="block max-w-full text-xs" :disabled="keyFileBusy || submitting" @change="readKeyFile" />
                <p class="text-xs text-muted-foreground">{{ ff('keyHint') }}</p>
              </div>
            </div>
            <div v-if="form.protocol !== 'newzoe' && !isFangfang">
              <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.callbackUrl') }}</label>
              <Input v-model="form.callback_url" :placeholder="t('siteConnections.form.callbackUrlPlaceholder')" />
            </div>
            <div>
              <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.retryMax') }}</label>
              <Input v-model.number="form.retry_max" type="number" min="0" placeholder="3" />
            </div>
            <div>
              <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.retryIntervals') }}</label>
              <Input v-model="form.retry_intervals" placeholder="30,60,120" />
            </div>
          </div>

          <p v-if="isFangfang" class="text-sm text-muted-foreground">{{ ff('formHint') }}</p>
          <div v-else class="border-t border-border pt-4">
            <h3 class="mb-3 text-sm font-medium text-foreground">{{ t('siteConnections.form.markupSection') }}</h3>
            <div class="grid grid-cols-1 gap-4 md:grid-cols-2">
              <div>
                <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.exchangeRate') }}</label>
                <Input v-model.number="form.exchange_rate" type="number" step="0.000001" min="0" placeholder="1" />
                <p class="mt-1 text-xs text-muted-foreground">{{ t('siteConnections.form.exchangeRateHint') }}</p>
              </div>
              <div>
                <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.priceMarkupPercent') }}</label>
                <div class="relative">
                  <Input v-model.number="form.price_markup_percent" type="number" step="0.01" min="0" placeholder="0" />
                  <span class="absolute right-3 top-1/2 -translate-y-1/2 text-xs text-muted-foreground">%</span>
                </div>
                <p class="mt-1 text-xs text-muted-foreground">{{ t('siteConnections.form.priceMarkupPercentHint') }}</p>
              </div>
              <div>
                <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.priceRoundingMode') }}</label>
                <Select v-model="form.price_rounding_mode">
                  <SelectTrigger class="h-9 w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="none">{{ t('siteConnections.form.roundingNone') }}</SelectItem>
                    <SelectItem value="ceil_int">{{ t('siteConnections.form.roundingCeilInt') }}</SelectItem>
                    <SelectItem value="ceil_tenth">{{ t('siteConnections.form.roundingCeilTenth') }}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div>
                <label class="mb-1.5 block text-xs font-medium text-muted-foreground">{{ t('siteConnections.form.autoSyncPrice') }}</label>
                <Select v-model="form.auto_sync_price">
                  <SelectTrigger class="h-9 w-full">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="false">{{ t('admin.common.no') }}</SelectItem>
                    <SelectItem value="true">{{ t('admin.common.yes') }}</SelectItem>
                  </SelectContent>
                </Select>
                <p class="mt-1 text-xs text-muted-foreground">{{ t('siteConnections.form.autoSyncPriceHint') }}</p>
              </div>
            </div>
          </div>

          <div class="flex flex-col-reverse gap-3 border-t border-border pt-6 sm:flex-row sm:justify-end">
            <Button type="button" variant="outline" class="w-full sm:w-auto" @click="closeModal">{{ t('admin.common.cancel') }}</Button>
            <Button type="submit" class="w-full sm:w-auto" :disabled="submitting || keyFileBusy">{{ isEditing ? t('admin.common.save') : t('admin.common.create') }}</Button>
          </div>
        </form>
      </DialogScrollContent>
    </Dialog>
  </div>
</template>
