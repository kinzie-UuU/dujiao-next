import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import Orders from './Orders.vue'
const api = vi.hoisted(() => ({ getOrders: vi.fn(), getOrder: vi.fn(), getPayment: vi.fn(), getSettings: vi.fn(), updateOrderStatus: vi.fn(), confirmManualPayment: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: api }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: {} }) }))
vi.mock('vue-i18n', async original => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }) }))
vi.mock('./components/OrderDetailDialog.vue', () => ({ default: { template: '<div />' } }))
vi.mock('./components/OrderFulfillmentModal.vue', () => ({ default: { template: '<div />' } }))
vi.mock('@/components/ManualPaymentConfirmDialog.vue', () => ({ default: { props: ['payment'], emits: ['close', 'confirmed'], template: '<div v-if="payment" data-confirm :data-id="payment.id" :data-currency="payment.settlement_currency"><button data-cancel @click="$emit(\'close\')">cancel</button><button data-success @click="$emit(\'confirmed\', payment.id)">success</button></div>' } }))
vi.mock('@/components/ui/select', () => {
  const slot = { template: '<slot />' }
  return { Select: { props: ['modelValue'], emits: ['update:modelValue'], template: '<select :value="modelValue" @change="$emit(\'update:modelValue\', $event.target.value)"><slot /></select>' }, SelectContent: slot, SelectTrigger: { template: '<span />' }, SelectValue: slot, SelectItem: { props: ['value'], template: '<option :value="value"><slot /></option>' } }
})
let app: App | undefined
const settle = async () => { for (let i = 0; i < 6; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { app?.unmount(); document.body.innerHTML = ''; vi.clearAllMocks() })
it('routes paid transitions through the shared dialog with full payment details; cancel restores status', async () => {
  const order = { id: 1, order_no: 'ORDER-1', status: 'pending_payment', total_amount: 100, currency: 'CNY', items: [] }
  const response = (data: unknown) => Promise.resolve({ data: { data } })
  api.getOrders.mockImplementation(() => response([order])); api.getSettings.mockImplementation(() => response({}))
  api.getOrder.mockImplementation(() => response({ ...order, payments: [{ id: 17, provider_type: 'manual_qr', status: 'pending', channel_type: 'twqr' }] }))
  api.getPayment.mockImplementation(() => response({ id: 17, channel_type: 'twqr', settlement_amount: '450', settlement_currency: 'TWD' }))
  const root = document.createElement('div'); document.body.append(root); app = createApp(Orders); app.mount(root); await settle()
  const select = [...root.querySelectorAll('select')].find(el => el.value === 'pending_payment')!
  select.value = 'paid'; select.dispatchEvent(new Event('change', { bubbles: true })); await settle()
  const update = [...root.querySelectorAll('button')].find(el => el.textContent?.trim() === 'admin.orders.update')!
  update.click(); await settle()
  expect(api.getPayment).toHaveBeenCalledWith(17)
  expect(root.querySelector('[data-confirm]')?.getAttribute('data-currency')).toBe('TWD')
  expect(api.updateOrderStatus).not.toHaveBeenCalled(); expect(api.confirmManualPayment).not.toHaveBeenCalled()
  root.querySelector<HTMLButtonElement>('[data-cancel]')!.click(); await settle()
  expect(select.value).toBe('pending_payment'); expect(root.querySelector('[data-confirm]')).toBeNull()
  select.value = 'paid'; select.dispatchEvent(new Event('change', { bubbles: true })); await settle(); update.click(); await settle()
  root.querySelector<HTMLButtonElement>('[data-success]')!.click(); await settle()
  expect(api.getOrders).toHaveBeenCalledTimes(2)
})
