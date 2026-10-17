const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { JSDOM } = require('../../frontend/admin/node_modules/jsdom');
const script = fs.readFileSync(__dirname + '/uuuaigc-affiliate-wallet.js', 'utf8');
const pause = () => new Promise(resolve => setTimeout(resolve, 180));
async function fixture(opened = true, options = {}) {
  const dom = new JSDOM('<div id="app"><main><div class="space-y-6"><div><h2>推广返利</h2></div><form>申请提现</form></div></main></div>', { url: 'https://store.example/me/affiliate', runScripts: 'outside-only' });
  const { window } = dom; window.localStorage.setItem('user_token', 'fixture-token');
  const calls = []; let dashboardCalls = 0; let available = options.available ?? '3.00', credited = '0.00';
  window.fetch = async (url, opts) => {
    calls.push({url, opts});
    if (url.endsWith('/affiliate/dashboard') && ++dashboardCalls === 2 && options.failRefreshOnce) throw new Error('余额刷新失败');
    let data = {};
    if (url.endsWith('/public/config')) data = { affiliate: { enabled: true, reward_mode: 'gpt_random_wallet', confirm_days: options.confirmDays ?? 7, all_products: options.allProducts ?? false } };
    else if (url.endsWith('/affiliate/dashboard')) data = { opened, affiliate_code: 'ABCD1234', available_commission: available, wallet_credited_commission: credited };
    else if (url.includes('/affiliate/commissions')) data = options.records ?? [{ commission_amount: '3', status: 'wallet_credited', created_at: '2026-10-17' }];
    else if (url.endsWith('/affiliate/open')) { opened = true; }
    else if (url.endsWith('/affiliate/wallet-credit')) { available = '0.00'; credited = '3.00'; data = { credited_amount: '3.00' }; }
    return { ok: true, json: async () => ({ status_code: options.failCredit && url.endsWith('/wallet-credit') ? 401 : 200, message: '拒绝操作', data }) };
  };
  window.eval(script); await pause();
  return {dom, window, calls};
}
test('wallet mode hides cash form, reads confirmation days and credits once', async () => {
  const {dom, window, calls} = await fixture();
  try {
    const panel = window.document.querySelector('.uaw-panel'); assert.ok(panel);
    assert.match(panel.textContent, /付款满 7 天/); assert.match(panel.textContent, /不支持提现/);
    assert.ok(window.document.querySelector('[data-uaw-hidden]'));
    const b = [...panel.querySelectorAll('button')].find(n => n.textContent === '转入消费余额'); b.click(); b.click(); await pause();
    assert.equal(calls.filter(x => x.url.endsWith('wallet-credit')).length, 1);
    assert.match(panel.textContent, /已转入 ¥3.00/); assert.equal(calls.some(x => x.url.includes('withdraw')), false);
    assert.equal(panel.innerHTML.includes('fixture-token'), false);
    window.history.pushState({}, '', '/me/wallet'); window.dispatchEvent(new window.PopStateEvent('popstate')); await pause();
    assert.equal(window.document.querySelector('.uaw-panel'), null); assert.equal(window.document.querySelector('[data-uaw-hidden]'), null);
  } finally { dom.window.close(); }
});
test('unopened customers can activate and see their own referral link', async () => {
  const {dom, window} = await fixture(false);
  try { [...window.document.querySelectorAll('.uaw-panel button')].find(b => b.textContent === '开通推广').click(); await pause(); assert.match(window.document.querySelector('.uaw-link input').value, /aff=ABCD1234/); }
  finally { dom.window.close(); }
});
test('API envelope error is shown without false success', async () => {
  const {dom, window} = await fixture(true, {failCredit: true});
  try { [...window.document.querySelectorAll('.uaw-panel button')].find(b => b.textContent === '转入消费余额').click(); await pause(); assert.match(window.document.querySelector('[role=status]').textContent, /拒绝操作/); }
  finally { dom.window.close(); }
});

test('zero-day policy explains manual payment confirmation and preserves refund rules', async () => {
  const {dom, window} = await fixture(true, {confirmDays: 0});
  try {
    const rules = window.document.querySelector('.uaw-rules').textContent;
    assert.match(rules, /人工确认收款后可转入本站消费余额/);
    assert.match(rules, /退款将扣回相应奖励/);
    assert.match(rules, /不支持提现/);
    assert.doesNotMatch(rules, /付款满|0 天|7 天/);
  } finally { dom.window.close(); }
});


test('reward records expose table headers, status text and machine-readable dates', async () => {
  const {dom, window} = await fixture(true, {records: [
    {commission_amount: '2', status: 'available', created_at: '2026-10-17T02:30:00Z'},
    {commission_amount: '1', status: 'rejected', created_at: 'invalid'},
    {commission_amount: '4', status: 'future_status', created_at: '2026-10-17T01:00:00Z'},
  ]});
  try {
    const table = window.document.querySelector('table.uaw-records');
    assert.ok(table);
    assert.equal(table.querySelector('caption').textContent, '奖励记录');
    assert.deepEqual([...table.querySelectorAll('thead th')].map(th => [th.textContent, th.scope]), [['奖励金额', 'col'], ['状态', 'col'], ['创建时间', 'col']]);
    const rows = [...table.querySelectorAll('tbody tr')];
    assert.equal(rows.length, 3);
    assert.equal(rows[0].querySelector('.uaw-badge').textContent, '可转入余额');
    assert.equal(rows[0].querySelector('time').dateTime, '2026-10-17T02:30:00.000Z');
    assert.equal(rows[1].querySelector('.uaw-badge').textContent, '已扣回');
    assert.equal(rows[1].querySelector('time').textContent, '日期待更新');
    assert.equal(rows[1].querySelector('time').hasAttribute('datetime'), false);
    assert.equal(rows[2].querySelector('.uaw-badge').dataset.state, 'unknown');
    assert.equal(rows[2].querySelector('.uaw-badge').textContent, '处理中');
  } finally { dom.window.close(); }
});

test('zero balance disables transfer while empty state retains usable sharing controls', async () => {
  const {dom, window, calls} = await fixture(true, {available: '0.00', records: []});
  try {
    const panel = window.document.querySelector('.uaw-panel');
    const credit = panel.querySelector('[data-action="credit"]');
    assert.equal(credit.disabled, true);
    credit.click(); await pause();
    assert.equal(calls.some(call => call.url.endsWith('/wallet-credit')), false);
    assert.match(panel.querySelector('.uaw-empty').textContent, /还没有奖励记录/);
    assert.equal(panel.querySelector('table'), null);
    const copy = panel.querySelector('[data-action="copy"]');
    assert.equal(copy.disabled, false);
    assert.equal(copy.classList.contains('uaw-primary'), false);
    assert.equal(panel.querySelector('[data-action="refresh"]').classList.contains('uaw-primary'), false);
    copy.click(); await pause();
    const input = panel.querySelector('input');
    assert.equal(window.document.activeElement, input);
    assert.equal(input.selectionEnd - input.selectionStart, input.value.length);
    assert.match(panel.querySelector('[role="status"]').textContent, /手动复制/);
  } finally { dom.window.close(); }
});

test('refresh retains keyboard focus and does not duplicate the panel', async () => {
  const {dom, window, calls} = await fixture();
  try {
    const refresh = window.document.querySelector('[data-action="refresh"]');
    refresh.focus(); refresh.click(); await pause();
    assert.equal(window.document.activeElement.dataset.action, 'refresh');
    assert.equal(window.document.querySelectorAll('.uaw-panel').length, 1);
    assert.equal(calls.filter(call => call.url.endsWith('/affiliate/dashboard')).length, 2);
    assert.ok([...window.document.querySelectorAll('.uaw-panel svg')].every(svg => svg.getAttribute('aria-hidden') === 'true' && svg.getAttribute('focusable') === 'false'));
  } finally { dom.window.close(); }
});


test('successful credit with failed readback locks stale transfer until retry succeeds', async () => {
  const {dom, window, calls} = await fixture(true, {failRefreshOnce: true});
  try {
    const panel = window.document.querySelector('.uaw-panel');
    panel.querySelector('[data-action="credit"]').click(); await pause();
    assert.match(panel.querySelector('[role="status"]').textContent, /操作已完成，但余额信息未刷新/);
    assert.equal(panel.querySelector('[data-action="credit"]').disabled, true);
    assert.equal(panel.querySelector('[data-action="refresh"]').disabled, false);
    panel.querySelector('[data-action="credit"]').click();
    assert.equal(calls.filter(call => call.url.endsWith('/wallet-credit')).length, 1);
    panel.querySelector('[data-action="refresh"]').click(); await pause();
    assert.equal(panel.querySelector('[data-kind="available"] dd').textContent, '¥0.00');
    assert.equal(panel.querySelector('[data-kind="credited"] dd').textContent, '¥3.00');
    assert.equal(panel.querySelector('[data-action="credit"]').disabled, true);
    assert.equal(panel.querySelector('[role="status"]').textContent, '');
  } finally { dom.window.close(); }
});

 test('all-products policy is explicit and legacy scope stays accurate', async () => {
  for (const allProducts of [true, false]) {
    const {dom, window} = await fixture(true, {allProducts, confirmDays: 0});
    try {
      const text = window.document.querySelector('.uaw-panel').textContent;
      assert.match(text, allProducts ? /本站任意商品/ : /指定 GPT 商品/);
      if (allProducts) assert.doesNotMatch(text, /GPT/);
      assert.match(text, /人民币主订单/);
      assert.match(text, /不足 1 元不奖励/);
    } finally { dom.window.close(); }
  }
});
