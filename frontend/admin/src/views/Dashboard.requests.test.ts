import { afterEach, describe, expect, it, vi } from 'vitest'
import { createApp, nextTick, type App } from 'vue'
import i18n from '@/i18n'
import Dashboard from './Dashboard.vue'

let app: App | undefined

afterEach(() => {
  app?.unmount()
  document.body.innerHTML = ''
  vi.unstubAllGlobals()
})

describe('dashboard requests', () => {
  it('loads and refreshes statistics and alerts without advertising requests', async () => {
    const responses: Record<string, unknown> = {
      '/api/v1/admin/dashboard/overview': {
        currency: 'CNY',
        kpi: { paid_orders: 2, gmv_paid: '200.00', total_profit: '130.00', missing_cost_items: 0 },
        funnel: {},
        alerts: [{ type: 'payments_failed', level: 'warning', value: 3 }],
      },
      '/api/v1/admin/dashboard/trends': { points: [] },
      '/api/v1/admin/dashboard/rankings': { top_products: [], top_channels: [] },
      '/api/v1/admin/dashboard/inventory-alerts': [{
        product_id: 1, product_title: { 'zh-CN': '库存回归样品' },
        fulfillment_type: 'manual', alert_type: 'low_stock_products', available_stock: 2,
      }],
    }
    // Exercise the real API client: silently caught ad failures must still be
    // visible in this complete request log, rather than hidden by a component mock.
    const fetchMock = vi.fn(async (input: string | URL | Request, _options?: RequestInit) => {
      const url = new URL(String(input), 'http://localhost')
      return new Response(JSON.stringify({ status_code: 0, data: responses[url.pathname] ?? {} }), {
        status: 200, headers: { 'Content-Type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetchMock)
    const root = document.createElement('div')
    document.body.append(root)
    app = createApp(Dashboard)
    app.use(i18n)
    app.component('RouterLink', { template: '<a><slot /></a>' })
    app.mount(root)
    await vi.waitFor(() => expect(root.textContent).toContain('库存回归样品'))
    await nextTick()

    const paths = () => fetchMock.mock.calls.map(([input]) => new URL(String(input), 'http://localhost').pathname).sort()
    const expected = Object.keys(responses).sort()
    expect(paths()).toEqual(expected)
    expect(root.textContent).toContain('200.00')
    expect(root.textContent).toContain(i18n.global.t('admin.dashboard.alertTypes.payments_failed'))
    const refresh = Array.from(root.querySelectorAll('button')).find(
      button => button.textContent?.trim() === i18n.global.t('admin.dashboard.actions.refreshNow'),
    )
    expect(refresh).toBeDefined()
    refresh!.click()
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(8))
    await nextTick()
    expect(paths()).toEqual([...expected, ...expected].sort())
    for (const [input] of fetchMock.mock.calls) {
      expect(new URL(String(input), 'http://localhost').origin).toBe('http://localhost')
    }
    for (const [input, options] of fetchMock.mock.calls.slice(4)) {
      const url = new URL(String(input), 'http://localhost')
      if (!url.pathname.endsWith('/inventory-alerts')) expect(url.searchParams.get('force_refresh')).toBe('true')
      expect((options as RequestInit | undefined)?.method).toBe('GET')
    }
  })
})
