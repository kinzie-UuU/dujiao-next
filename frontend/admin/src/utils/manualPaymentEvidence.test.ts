import { expect, it } from 'vitest'
import type { AdminPayment } from '@/api/types'
import { manualPaymentSettlement, validManualPaymentEvidence } from './manualPaymentEvidence'
it.each(['NaN', 'Infinity', '-1', '0', '1.001', '1e3'])('rejects invalid CNY amount %s', amount => {
  expect(validManualPaymentEvidence(amount, 'CNY', 'wechat', true)).toBe(false)
})
it('requires verification and permits six decimal USDT receipts', () => {
  expect(validManualPaymentEvidence('10', 'CNY', 'wechat', false)).toBe(false)
  expect(validManualPaymentEvidence('10.123456', 'USDT', 'binance_usdt', true)).toBe(true)
  expect(validManualPaymentEvidence('10.1234567', 'USDT', 'binance_usdt', true)).toBe(false)
})
it.each([['twqr', 'TWD', '450'], ['binance_usdt', 'USDT', '14.29']])('uses locked settlement quote for %s', (channel_type, currency, amount) => {
  const payment = { channel_type, currency: 'CNY', amount: 100, settlement_amount: amount, settlement_currency: currency } as AdminPayment
  expect(manualPaymentSettlement(payment)).toEqual({ amount, currency })
  delete payment.settlement_amount
  expect(manualPaymentSettlement(payment)).toEqual({ amount: '', currency })
})
