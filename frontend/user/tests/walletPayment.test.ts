import test from 'node:test'
import assert from 'node:assert/strict'
import { walletPaymentCents, walletPurchaseBlock } from '../src/utils/money.ts'

test('reward reversal debt cannot increase checkout or online payment', () => {
  const payable = 9900
  const credit = walletPaymentCents('-5.00', '99.00')
  assert.equal(credit, 0)
  assert.equal(payable - credit, payable)
})

test('wallet payment caps valid balances at the order total and rejects invalid amounts', () => {
  assert.equal(walletPaymentCents('5.00', '99.00'), 500)
  assert.equal(walletPaymentCents('100.00', '99.00'), 9900)
  assert.equal(walletPaymentCents('0', '99.00'), 0)
  assert.equal(walletPaymentCents('invalid', '99.00'), 0)
  assert.equal(walletPaymentCents('5.00', '-1.00'), 0)
})

test('checkout allocation after repaying reward debt matches usable balance', () => {
  for (const [balance, total, wallet, online] of [
    ['-3.00', '99.00', 0, 9900],
    ['0.00', '99.00', 0, 9900],
    ['3.00', '99.00', 300, 9600],
    ['3.00', '2.00', 200, 0],
  ] as const) {
    const deducted = walletPaymentCents(balance, total)
    assert.equal(deducted, wallet)
    assert.equal(Math.round(Number(total) * 100) - deducted, online)
  }
})

test('wallet debt blocks purchases independently of selected payment method; repaid accounts resume', () => {
  for (const balance of ['-5.00', '-3.00', '-0.01']) {
    assert.equal(walletPurchaseBlock(balance, true, false), 'debt')
  }
  for (const balance of ['0.00', '3.00']) {
    assert.equal(walletPurchaseBlock(balance, true, false), null)
  }
})

test('unverified wallet balance fails closed for purchases', () => {
  assert.equal(walletPurchaseBlock('0', false, false), 'unavailable')
  assert.equal(walletPurchaseBlock('0', true, true), 'unavailable')
  assert.equal(walletPurchaseBlock('invalid', true, false), 'unavailable')
})
