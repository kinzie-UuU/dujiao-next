import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { usePaymentStepUpStore } from './paymentStepUp'
const api = vi.hoisted(() => ({ post: vi.fn(), put: vi.fn(), delete: vi.fn() }))
vi.mock('@/api/client', () => ({ api }))
import { adminAPI } from '@/api/admin'

beforeEach(() => { setActivePinia(createPinia()); vi.clearAllMocks() })
describe('payment write verification', () => {
  it('cancellation clears the code and resolves without authorization', async () => {
    const store = usePaymentStepUpStore()
    const result = store.ask()
    store.code = '123456'
    store.cancel()
    expect(await result).toBeNull()
    expect(store.code).toBe('')
    expect(store.open).toBe(false)
    expect(api.post).not.toHaveBeenCalled()
  })
  it('accepts only six digits and clears them before another write', async () => {
    const store = usePaymentStepUpStore()
    const result = store.ask()
    store.code = 'recovery'
    store.confirm()
    expect(store.open).toBe(true)
    store.code = '123456'
    store.confirm()
    expect(await result).toBe('123456')
    expect(store.code).toBe('')
    const next = store.ask()
    expect(store.code).toBe('')
    store.cancel()
    expect(await next).toBeNull()
  })
  it('replaces an old pending prompt without approving it', async () => {
    const store = usePaymentStepUpStore()
    const first = store.ask()
    const second = store.ask()
    expect(await first).toBeNull()
    store.cancel()
    expect(await second).toBeNull()
  })
  it('sends the proof header on create, update and delete', async () => {
    await adminAPI.createPaymentChannel({ name: 'test' }, '123456')
    await adminAPI.updatePaymentChannel(7, { is_active: false }, '234567')
    await adminAPI.deletePaymentChannel(7, '345678')
    expect(api.post).toHaveBeenCalledWith('/admin/payment-channels', { name: 'test' }, { headers: { 'X-Admin-TOTP': '123456' } })
    expect(api.put).toHaveBeenCalledWith('/admin/payment-channels/7', { is_active: false }, { headers: { 'X-Admin-TOTP': '234567' } })
    expect(api.delete).toHaveBeenCalledWith('/admin/payment-channels/7', { headers: { 'X-Admin-TOTP': '345678' } })
  })
})
