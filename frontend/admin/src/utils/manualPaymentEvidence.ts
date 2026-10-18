export interface ManualPaymentEvidence {
  transaction_id: string
  payer_verified: boolean
  amount: string
  currency: string
}

// Collect the platform ledger values, never derive proof from a customer screenshot.
export function collectManualPaymentEvidence(t: (key: string) => string, channel = ''): ManualPaymentEvidence | null {
  const transaction_id = window.prompt(t('admin.payments.receiptTransactionPrompt'))?.trim()
  if (!transaction_id) return null
  const amount = window.prompt(t('admin.payments.receiptAmountPrompt'))?.trim()
  if (!amount) return null
  const currency = window.prompt(t('admin.payments.receiptCurrencyPrompt'))?.trim().toUpperCase()
  if (!currency) return null
  if (!/^[A-Za-z0-9._:-]{10,128}$/.test(transaction_id) || !(channel === 'binance_usdt' ? /^\d+(?:\.\d{1,6})?$/ : /^\d+(?:\.\d{1,2})?$/).test(amount) || Number(amount) <= 0 || !/^[A-Z]{3,8}$/.test(currency)) {
    window.alert(t('admin.payments.receiptInvalid'))
    return null
  }
  if (!window.confirm(t('admin.payments.receiptPayerPrompt'))) return null
  return { transaction_id, amount, currency, payer_verified: true }
}
