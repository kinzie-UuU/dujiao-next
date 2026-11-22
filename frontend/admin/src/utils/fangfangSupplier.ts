import type { AdminSiteConnection } from '@/api/types'

export const fangfangProtocol = 'fangfang-readonly'
export const fangfangOrigin = 'https://canboso.com'
export const fangfangKeyMarker = 'fangfang-global'
export const fangfangTargets = [
  { local_product_id: 37, local_sku_id: 70, upstream_product_id: 1, label: 'Muse 31B' },
  { local_product_id: 41, local_sku_id: 74, upstream_product_id: 2, label: 'Plus' },
] as const

export function protectFangfangPayload(payload: Partial<AdminSiteConnection>): Partial<AdminSiteConnection> {
  if (payload.protocol !== fangfangProtocol) return payload
  return { ...payload, base_url: fangfangOrigin, api_key: fangfangKeyMarker, callback_url: '', auto_sync_price: false, exchange_rate: 1, price_markup_percent: 0, price_rounding_mode: 'none' }
}

export function parseFangfangKey(text: string): string {
  const key = text.trim()
  if (!key || key.length > 256 || /\s/.test(key)) throw new Error('Invalid key file')
  return key
}
