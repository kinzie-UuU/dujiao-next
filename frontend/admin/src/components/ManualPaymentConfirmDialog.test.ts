import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, reactive, type App } from 'vue'
import ManualPaymentConfirmDialog from './ManualPaymentConfirmDialog.vue'
import type { AdminPayment } from '@/api/types'
const api = vi.hoisted(() => ({ confirmManualPayment: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: api }))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/ui/dialog', () => ({
  Dialog: { props: ['open'], template: '<div v-if="open"><slot /></div>' },
  DialogDescription: { template: '<div><slot /></div>' }, DialogFooter: { template: '<div><slot /></div>' },
  DialogHeader: { template: '<div><slot /></div>' }, DialogTitle: { template: '<div><slot /></div>' },
}))
vi.mock('reka-ui', () => ({ DialogPortal: { template: '<div><slot /></div>' }, DialogOverlay: { template: '<div />' }, DialogContent: { template: '<div><slot /></div>' }, Primitive: { template: '<button><slot /></button>' } }))
let app: App | undefined
const settle = async () => { for (let i = 0; i < 5; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { app?.unmount(); document.body.innerHTML = ''; vi.clearAllMocks() })
async function mount() {
  const state = reactive<{ payment: AdminPayment | null }>({ payment: null })
  const confirmed = vi.fn(); const close = vi.fn()
  const root = document.createElement('div'); document.body.append(root)
  app = createApp({ components: { ManualPaymentConfirmDialog }, setup: () => ({ state, confirmed, close }), template: '<ManualPaymentConfirmDialog :payment="state.payment" @confirmed="confirmed" @close="close" />' })
  app.mount(root)
  state.payment = { id: 17, channel_type: 'wechat', currency: 'CNY', amount: 10, order_no: 'ORDER-17' } as AdminPayment
  await settle()
  return { root, confirmed, close }
}
async function fill(root: HTMLElement, amount = '10.00') {
  const input = root.querySelector<HTMLInputElement>('#manual-received-amount')!; input.value = amount; input.dispatchEvent(new Event('input', { bubbles: true }))
  const checkbox = root.querySelector<HTMLInputElement>('input[type=checkbox]')!; checkbox.checked = true; checkbox.dispatchEvent(new Event('change', { bubbles: true })); await settle()
}
it('requires verification, submits no transaction ID, and prevents duplicate requests', async () => {
  let finish!: () => void
  api.confirmManualPayment.mockImplementation(() => new Promise<void>(resolve => { finish = resolve }))
  const { root, confirmed } = await mount()
  const form = root.querySelector('form')!
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  expect(api.confirmManualPayment).not.toHaveBeenCalled()
  expect(root.querySelector('[role=alert]')).not.toBeNull()
  await fill(root)
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  expect(api.confirmManualPayment).toHaveBeenCalledTimes(1)
  expect(api.confirmManualPayment).toHaveBeenCalledWith(17, { amount: '10.00', currency: 'CNY', payer_verified: true })
  finish(); await settle(); expect(confirmed).toHaveBeenCalledWith(17)
})
it('retains values and API errors, allowing retry', async () => {
  api.confirmManualPayment.mockRejectedValueOnce(new Error('Amount mismatch')).mockResolvedValueOnce({})
  const { root, confirmed } = await mount(); await fill(root)
  const form = root.querySelector('form')!
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  expect(root.textContent).toContain('Amount mismatch')
  expect(root.querySelector<HTMLInputElement>('#manual-received-amount')?.value).toBe('10.00')
  expect(confirmed).not.toHaveBeenCalled()
  form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
  expect(confirmed).toHaveBeenCalledWith(17)
})
it('cancel does not submit', async () => {
  const { root, close } = await mount()
  root.querySelector<HTMLButtonElement>('button[type=button]')!.click(); await settle()
  expect(close).toHaveBeenCalled(); expect(api.confirmManualPayment).not.toHaveBeenCalled()
})
