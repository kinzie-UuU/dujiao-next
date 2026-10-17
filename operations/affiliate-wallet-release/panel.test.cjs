const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { JSDOM } = require('../../frontend/admin/node_modules/jsdom');
const script = fs.readFileSync(__dirname + '/uuuaigc-affiliate-wallet.js', 'utf8');
const pause = () => new Promise(resolve => setTimeout(resolve, 180));
async function fixture(opened = true, options = {}) {
  const dom = new JSDOM('<div id="app"><main><div class="space-y-6"><div><h2>推广返利</h2></div><form>申请提现</form></div></main></div>', { url: 'https://store.example/me/affiliate', runScripts: 'outside-only' });
  const { window } = dom; window.localStorage.setItem('user_token', 'fixture-token');
  const calls = []; let available = '3.00', credited = '0.00';
  window.fetch = async (url, opts) => {
    calls.push({url, opts});
    let data = {};
    if (url.endsWith('/public/config')) data = { affiliate: { enabled: true, reward_mode: 'gpt_random_wallet', confirm_days: options.confirmDays ?? 7 } };
    else if (url.endsWith('/affiliate/dashboard')) data = { opened, affiliate_code: 'ABCD1234', available_commission: available, wallet_credited_commission: credited };
    else if (url.includes('/affiliate/commissions')) data = [{ commission_amount: '3', status: 'wallet_credited', created_at: '2026-10-17' }];
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
