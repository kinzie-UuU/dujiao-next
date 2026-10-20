import type { AdminPayment } from '@/api/types'

export interface ManualPaymentEvidence {
  payer_verified: boolean
  amount: string
  currency: string
}

export function manualPaymentSettlement(payment: AdminPayment) {
  const channel = payment.channel_type
  const currency = channel === 'twqr' ? 'TWD' : channel === 'binance_usdt' ? 'USDT' : payment.currency
  const converted = channel === 'twqr' || channel === 'binance_usdt'
  const amount = converted
    ? (payment.settlement_currency === currency ? payment.settlement_amount : undefined)
    : payment.amount
  return { currency, amount: amount != null && Number(amount) > 0 && Number.isFinite(Number(amount)) ? String(amount) : '' }
}

export function validManualPaymentEvidence(amount: string, currency: string, channel: string, verified: boolean) {
  return verified && (channel === 'binance_usdt' ? /^\d+(?:\.\d{1,6})?$/ : /^\d+(?:\.\d{1,2})?$/).test(amount)
    && Number.isFinite(Number(amount)) && Number(amount) > 0 && /^[A-Z]{3,8}$/.test(currency)
}
