import { afterEach, expect, it, vi } from 'vitest'
import { collectManualPaymentEvidence } from './manualPaymentEvidence'
const t = (key: string) => key
afterEach(() => vi.restoreAllMocks())
it('requires actual receipt values and explicit payer verification', () => {
  vi.spyOn(window, 'prompt').mockReturnValueOnce('WX202610180001').mockReturnValueOnce('10.00').mockReturnValueOnce('cny')
  vi.spyOn(window, 'confirm').mockReturnValue(true)
  expect(collectManualPaymentEvidence(t)).toEqual({transaction_id:'WX202610180001',amount:'10.00',currency:'CNY',payer_verified:true})
})
it('does not confirm when operator cancels or refuses payer verification', () => {
  const prompt = vi.spyOn(window, 'prompt').mockReturnValue(null)
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false)
  expect(collectManualPaymentEvidence(t)).toBeNull()
  expect(confirm).not.toHaveBeenCalled()
  prompt.mockReturnValueOnce('WX202610180001').mockReturnValueOnce('10').mockReturnValueOnce('CNY')
  expect(collectManualPaymentEvidence(t)).toBeNull()
})
it.each(['NaN','Infinity','-1','1.001'])('rejects invalid received amount %s', (amount) => {
  vi.spyOn(window,'prompt').mockReturnValueOnce('WX202610180001').mockReturnValueOnce(amount).mockReturnValueOnce('CNY')
  const confirm=vi.spyOn(window,'confirm'); vi.spyOn(window,'alert').mockImplementation(()=>{})
  expect(collectManualPaymentEvidence(t)).toBeNull(); expect(confirm).not.toHaveBeenCalled()
})

it('accepts six-decimal actual USDT receipts only for USDT', () => {
 vi.spyOn(window,'prompt').mockReturnValueOnce('0x123456789abcdef').mockReturnValueOnce('10.123456').mockReturnValueOnce('USDT')
 vi.spyOn(window,'confirm').mockReturnValue(true)
 expect(collectManualPaymentEvidence(t,'binance_usdt')?.amount).toBe('10.123456')
})
