import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import Dashboard from './Dashboard.vue'

const api = vi.hoisted(() => ({
  getDashboardOverview: vi.fn(),
  getDashboardTrends: vi.fn(),
  getDashboardRankings: vi.fn(),
  getDashboardInventoryAlerts: vi.fn(),
}))
vi.mock('@/api/admin', () => ({ adminAPI: api }))
vi.mock('vue-i18n', async (importOriginal) => ({ ...await importOriginal<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key, locale: { value: 'zh-CN' } }) }))
vi.mock('@/components/admin/DashboardAd.vue', () => ({ default: { template: '<div />' } }))

let app: App | undefined
afterEach(() => { app?.unmount(); document.body.innerHTML = ''; vi.clearAllMocks() })

describe('dashboard cost coverage', () => {
  it.each([
    { name: 'zero costs', missing: 2, profit: null, pending: true },
    { name: 'mixed costs', missing: 1, profit: null, pending: true },
    { name: 'complete costs', missing: 0, profit: '130.00', pending: false },
    { name: 'legacy API without cost coverage', missing: undefined, profit: '200.00', pending: true },
  ])('renders $name without turning revenue into profit', async ({ missing, profit, pending }) => {
    const response = (data: unknown) => Promise.resolve({ data: { data } })
    api.getDashboardOverview.mockImplementation(() => response({ currency: 'CNY', kpi: { paid_orders: 2, gmv_paid: '200.00', total_cost: '70.00', total_profit: profit, profit_margin: profit === null ? null : '65.00', missing_cost_items: missing }, funnel: {}, alerts: [] }))
    api.getDashboardTrends.mockImplementation(() => response({ points: [{ date: '2026-09-07', orders_total: 2, orders_paid: 2, payments_success: 2, payments_failed: 0, gmv_paid: '200.00', profit, missing_cost_items: missing }] }))
    api.getDashboardRankings.mockImplementation(() => response({ top_products: [{ product_id: 1, title: 'Test product', paid_orders: 2, quantity: 2, paid_amount: '200.00', profit, missing_cost_items: missing }], top_channels: [] }))
    api.getDashboardInventoryAlerts.mockImplementation(() => response([]))
    const root = document.createElement('div')
    document.body.append(root)
    app = createApp(Dashboard)
    app.component('RouterLink', { template: '<a><slot /></a>' })
    app.mount(root)
    await Promise.resolve()
    await Promise.resolve()
    await nextTick()
    const text = root.textContent || ''
    expect(text).toContain('200.00')
    expect(text).toContain('Test product')
    if (pending) {
      expect(text.match(/admin\.dashboard\.profitPending(?!Reason)/g)).toHaveLength(4)
      expect(text).toContain('admin.dashboard.profitPendingReason')
    } else {
      expect(text).not.toContain('admin.dashboard.profitPending')
      expect(text.match(/130\.00/g)).toHaveLength(3)
      expect(text).toContain('65.00%')
    }
  })
})
