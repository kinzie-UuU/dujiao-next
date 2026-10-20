import { afterEach, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import Payments from './Payments.vue'

const api = vi.hoisted(() => ({ getPayments: vi.fn(), getPayment: vi.fn(), confirmManualPayment: vi.fn() }))
vi.mock('@/api/admin', () => ({ adminAPI: api }))
vi.mock('vue-router', () => ({ useRoute: () => ({ query: { payment_id: '17' } }) }))
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/components/ComplianceGuardWrapper.vue', () => ({ default: { template: '<div><slot /></div>' } }))
vi.mock('@/components/ui/dialog', () => {
 const component = { template: '<div><slot /></div>' }
 return { Dialog: { props: ['open'], template: '<div v-if="open"><slot /></div>' }, DialogScrollContent: component, DialogHeader: component, DialogTitle: component, DialogFooter: component, DialogDescription: component }
})
vi.mock('reka-ui', async (original) => ({ ...await original<typeof import('reka-ui')>(), DialogPortal: { template: '<div><slot /></div>' }, DialogOverlay: { template: '<div />' }, DialogContent: { template: '<div><slot /></div>' }, Primitive: { template: '<button><slot /></button>' } }))
let app: App | undefined
const settle = async () => { for (let i = 0; i < 5; i++) { await Promise.resolve(); await nextTick() } }
afterEach(() => { app?.unmount(); document.body.innerHTML = ''; vi.restoreAllMocks(); vi.clearAllMocks() })

it.each([
 { provider: 'manual_qr', status: 'expired', confirm: true },
 { provider: 'manual_qr', status: 'pending', confirm: true },
 { provider: 'manual_qr', status: 'success', confirm: false },
 { provider: 'stripe', status: 'expired', confirm: false },
])('exact payment confirmation for $provider/$status', async ({ provider, status, confirm }) => {
 const response = (data: unknown) => Promise.resolve({ data: { data } })
 api.getPayments.mockImplementation(() => response([]))
 api.getPayment.mockImplementation(() => response({ id: 17, provider_type: provider, status, channel_type: 'wechat', exception_code: 'closed_order_payment_succeeded', currency: 'CNY', amount: 10 }))
 api.confirmManualPayment.mockImplementation(() => response({}))

 const root = document.createElement('div'); document.body.append(root)
 app = createApp(Payments); app.mount(root); await settle()
 expect(root.textContent).toContain('admin.payments.exceptionHint')
 const button = [...root.querySelectorAll('button')].find(el => el.textContent?.trim() === 'admin.payments.manualConfirm')
 expect(Boolean(button)).toBe(confirm)
 if (button) {
   button.click(); await settle()
   expect(api.confirmManualPayment).not.toHaveBeenCalled()
   const input = root.querySelector<HTMLInputElement>('#manual-received-amount')!; input.value = '10.00'; input.dispatchEvent(new Event('input', { bubbles: true }))
   const checkbox = root.querySelector<HTMLInputElement>('input[type=checkbox]')!; checkbox.checked = true; checkbox.dispatchEvent(new Event('change', { bubbles: true })); await settle()
   root.querySelector('form')!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); await settle()
   expect(api.confirmManualPayment).toHaveBeenCalledWith(17, { amount: '10.00', currency: 'CNY', payer_verified: true })
 }
 else { expect(api.confirmManualPayment).not.toHaveBeenCalled() }
})
