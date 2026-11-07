import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

function harness(delay = 13000) {
  let now = 0, serial = 0
  const timers = new Map()
  const clock = (fn, delay) => { const id = ++serial; timers.set(id, { at: now + delay, fn }); return id }
  const context = vm.createContext({
    exports: {}, URLSearchParams, AbortController, FormData, Promise,
    localStorage: { getItem: () => 'synthetic-token', removeItem() {} },
    setTimeout: clock, clearTimeout: id => timers.delete(id),
    fetch: (url, opts) => new Promise((resolve, reject) => {
      opts.signal.addEventListener('abort', () => reject(new Error('aborted')))
      clock(() => resolve({ ok: true, json: async () => ({ status_code: 0, data: [{ id: 34 }] }) }), delay)
    }),
    require: name => name === '@/i18n' ? { default: { global: { t: k => k, locale: 'zh-CN' } } }
      : name === '@/utils/notify' ? { notifyError() {} } : { adminUrl: p => p },
  })
  const client = fs.readFileSync(new URL('../src/api/client.ts', import.meta.url), 'utf8').replace('import.meta.env.VITE_API_BASE_URL', '""')
  vm.runInContext(ts.transpileModule(client, { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText, context)
  const api = context.exports.api
  context.exports = {}; context.require = () => ({ api })
  vm.runInContext(ts.transpileModule(fs.readFileSync(new URL('../src/api/admin.ts', import.meta.url), 'utf8'), { compilerOptions: { module: ts.ModuleKind.CommonJS } }).outputText, context)
  return { api, admin: context.exports.adminAPI, advance(ms) { now += ms; for (const [id, timer] of [...timers]) if (timer.at <= now) { timers.delete(id); timer.fn() } } }
}

test('supplier catalogue survives 10 seconds and returns at 13 seconds', async () => {
  const h = harness(); const pending = h.admin.getUpstreamProducts({ connection_id: 1 })
  h.advance(10001); h.advance(2999)
  assert.equal((await pending).data.data[0].id, 34)
})
test('other reads retain default 10 second timeout', async () => {
  const h = harness(); const pending = h.api.get('/admin/products'); const rejected = assert.rejects(pending, /networkError/)
  h.advance(10000); await rejected
})

test('supplier catalogue still aborts at its bounded 20 second timeout', async () => {
  const h = harness(25000); const pending = h.admin.getUpstreamProducts(); const rejected = assert.rejects(pending, /networkError/)
  h.advance(20000); await rejected
})
