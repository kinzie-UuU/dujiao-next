import { describe, expect, it } from 'vitest'
import { fangfangOrigin, fangfangProtocol, fangfangKeyMarker, fangfangTargets, parseFangfangKey, protectFangfangPayload } from './fangfangSupplier'

describe('Fang Fang read-only connection', () => {
  it('overrides forged purchase-adjacent form settings without exposing the secret as api_key', () => {
    const payload = protectFangfangPayload({ protocol: fangfangProtocol, base_url: 'https://other.example', api_key: 'private-key', api_secret: 'private-key', auto_sync_price: true, exchange_rate: 26000, price_markup_percent: 100, callback_url: 'https://other.example/callback' })
    expect(payload).toMatchObject({ base_url: fangfangOrigin, api_key: fangfangKeyMarker, api_secret: 'private-key', auto_sync_price: false, exchange_rate: 1, price_markup_percent: 0, callback_url: '' })
  })
  it('does not change existing supplier payloads', () => {
    const existing = { protocol: 'newzoe', auto_sync_price: true, exchange_rate: 1, price_markup_percent: 100 }
    expect(protectFangfangPayload(existing)).toBe(existing)
  })
  it('accepts a single key with a trailing newline and rejects malformed content', () => {
    expect(parseFangfangKey(' test-key-value\n')).toBe('test-key-value')
    for (const text of ['', 'one\ntwo', 'one two', 'x'.repeat(257)]) expect(() => parseFangfangKey(text)).toThrow()
  })
  it('links the existing account SKUs, not the invitation CDK', () => {
    expect(fangfangTargets.map(({local_product_id,local_sku_id,upstream_product_id}) => [local_product_id,local_sku_id,upstream_product_id])).toEqual([[37,70,1],[41,74,2]])
  })
})
