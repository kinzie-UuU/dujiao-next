/* Scoped compatibility panel for the deployed storefront; does not replace its app or payment code. */
(() => {
  'use strict';
  if (window.__uuuaigcAffiliateWallet) return;
  window.__uuuaigcAffiliateWallet = true;
  const route = () => location.pathname.replace(/\/+$/, '') === '/me/affiliate';
  let host, original, loading = false, config, generation = 0;
  const money = value => Number.isFinite(Number(value)) ? Number(value).toFixed(2) : '0.00';
  const el = (tag, text, cls) => { const n = document.createElement(tag); if (text != null) n.textContent = text; if (cls) n.className = cls; return n; };
  async function api(path, method = 'GET', auth = true) {
    const headers = { 'Content-Type': 'application/json' };
    if (auth) {
      const token = localStorage.getItem('user_token');
      if (!token) throw new Error('请先登录后查看推广奖励。');
      headers.Authorization = `Bearer ${token}`;
    }
    const response = await fetch(`/api/v1${path}`, { method, headers, credentials: 'same-origin', cache: 'no-store' });
    const body = await response.json();
    if (!response.ok || (body.status_code && body.status_code !== 200)) throw new Error(body.message || '请求失败，请稍后重试。');
    return body;
  }
  function button(text, action) { const b = el('button', text, 'uaw-button'); b.type = 'button'; b.onclick = action; return b; }
  function message(text, error = false) { const n = host?.querySelector('[role=status]'); if (n) { n.textContent = text; n.setAttribute('data-error', String(error)); } }
  async function refresh() {
    if (!host || loading) return;
    loading = true;
    const current = host, revision = generation;
    try {
      const [d, records] = await Promise.all([api('/affiliate/dashboard'), api('/affiliate/commissions?page=1&page_size=20')]);
      if (!route() || current !== host || revision !== generation) return;
      render(d.data || {}, records.data || [], records.pagination || {});
    } catch (error) { if (current === host) message(error.message, true); }
    finally { loading = false; }
  }
  async function mutate(path, success) {
    const current = host;
    const buttons = [...current.querySelectorAll('button')]; buttons.forEach(b => b.disabled = true);
    try { const r = await api(path, 'POST'); await refresh(); if (current === host) message(success(r.data || {})); }
    catch (error) { if (current === host) message(error.message, true); }
    finally { if (current === host) buttons.forEach(b => b.disabled = false); }
  }
  function render(d, records, pagination) {
    host.replaceChildren();
    host.append(el('h2', '推广消费奖励'));
    const days = Number.isInteger(config.confirm_days) ? config.confirm_days : 7;
    host.append(el('p', `好友通过推广链接购买指定 GPT 商品，每笔符合条件的人民币主订单随机奖励整数 1～5 元。付款满 ${days} 天后可转入消费余额，抵扣本站商品，不支持提现。退款将扣回相应奖励；奖励不超过参与商品实付整数金额，不足 1 元不奖励。`, 'uaw-rules'));
    const status = el('p', '', 'uaw-status'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite'); host.append(status);
    if (!d.opened) { host.append(button('开通推广', () => mutate('/affiliate/open', () => '推广已开通，可以复制链接分享。'))); return; }
    const url = new URL(d.promotion_path || `/?aff=${encodeURIComponent(d.affiliate_code || '')}`, location.origin);
    if (url.origin !== location.origin) throw new Error('推广链接异常，请联系客服。');
    const label = el('label', '我的推广链接', 'uaw-link'); const input = el('input'); input.readOnly = true; input.value = url.href; input.setAttribute('aria-label', '我的推广链接'); label.append(input); host.append(label);
    host.append(button('复制推广链接', async () => { try { await navigator.clipboard.writeText(url.href); message('推广链接已复制。'); } catch { input.focus(); input.select(); message('请手动复制已选中的推广链接。'); } }));
    const stats = el('dl', null, 'uaw-stats');
    [['待确认奖励', d.pending_commission], ['可转入余额', d.available_commission], ['已转入消费余额', d.wallet_credited_commission]].forEach(([label, amount]) => { const item = el('div'); item.append(el('dt', label), el('dd', `¥${money(amount)}`)); stats.append(item); });
    host.append(stats, el('p', `访问 ${Number(d.click_count) || 0} 次 · 有效订单 ${Number(d.valid_order_count) || 0} 笔`));
    const credit = button('转入消费余额', () => mutate('/affiliate/wallet-credit', r => `已转入 ¥${money(r.credited_amount)}，可在本站购买商品时使用。`)); credit.disabled = !(Number(d.available_commission) > 0); host.append(credit);
    const wallet = el('a', '查看消费余额', 'uaw-wallet'); wallet.href = '/me/wallet'; host.append(wallet);
    host.append(el('h3', '奖励记录'), button('刷新', refresh));
    if (!records.length) host.append(el('p', '暂无奖励记录。'));
    const names = { pending_confirm: '待确认', available: '可转入余额', wallet_credited: '已转入余额', rejected: '已扣回', withdrawn: '历史已结算' };
    const list = el('ul', null, 'uaw-records');
    records.forEach(r => { const date = new Date(r.created_at); list.append(el('li', `¥${money(r.commission_amount)} · ${names[r.status] || '处理中'} · ${Number.isNaN(date.getTime()) ? '日期待更新' : date.toLocaleDateString('zh-CN')}`)); });
    host.append(list);
    if (Number(pagination.total) > records.length) host.append(el('p', '当前显示最近 20 笔奖励。'));
  }
  function cleanup() { generation++; host?.remove(); host = null; if (original) original.removeAttribute('data-uaw-hidden'); original = null; }
  async function sync() {
    if (!route()) { if (host || original) cleanup(); return; }
    if (!localStorage.getItem('user_token')) { if (host || original) cleanup(); return; }
    if (host?.isConnected) return;
    const candidates = [...document.querySelectorAll('#app main .space-y-6')];
    const panel = candidates.reverse().find(n => [...n.querySelectorAll('h2,h3')].some(h => /^(推广返利|推廣返利|Affiliate Program|Affiliate Center|推广中心|推廣中心|Affiliate)$/.test(h.textContent.trim())));
    if (!panel) return;
    if (!config) { if (loading) return; loading = true; try { config = (await api('/public/config', 'GET', false)).data?.affiliate; } catch { return; } finally { loading = false; } }
    if (!config?.enabled || config.reward_mode !== 'gpt_random_wallet' || !route() || !panel.isConnected) return;
    original = panel; original.setAttribute('data-uaw-hidden', '');
    host = el('section', null, 'uaw-panel'); host.setAttribute('aria-label', '推广消费奖励');
    const status = el('p', '正在加载推广奖励…'); status.setAttribute('role', 'status'); host.append(status, button('重试', refresh));
    panel.after(host); await refresh();
  }
  const style = el('style'); style.textContent = '[data-uaw-hidden]{display:none!important}.uaw-panel{border:1px solid var(--border,#d9ddd0);border-radius:16px;padding:28px;color:inherit;background:var(--card,transparent)}.uaw-panel h2{font-size:24px;font-weight:500}.uaw-panel h3{font-size:18px;font-weight:500;margin-top:28px}.uaw-rules{line-height:1.8;margin:12px 0}.uaw-status{min-height:24px;margin:12px 0}.uaw-status[data-error=true]{color:#b91c1c}.dark .uaw-status[data-error=true]{color:#fca5a5}.uaw-button{font:inherit;min-height:44px;border:1px solid #85923d;border-radius:8px;padding:8px 16px;background:#e6ff57;color:#17200a;margin:8px 8px 8px 0;cursor:pointer}.uaw-button:disabled{opacity:.5;cursor:default}.uaw-button:focus-visible,.uaw-link input:focus-visible,.uaw-wallet:focus-visible{outline:3px solid #829922;outline-offset:3px}.uaw-link{display:block;margin-top:16px}.uaw-link input{display:block;width:100%;min-height:44px;box-sizing:border-box;margin-top:8px;padding:10px;border:1px solid #89917a;border-radius:8px;background:transparent;color:inherit}.uaw-stats{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px;margin:24px 0}.uaw-stats dt{font-size:14px}.uaw-stats dd{font-size:24px;margin:6px 0}.uaw-wallet{color:inherit;display:inline-block;padding:12px;min-height:44px;box-sizing:border-box;text-decoration:underline}.uaw-records{padding-left:20px;line-height:2}@media(max-width:600px){.uaw-panel{padding:20px}.uaw-stats{grid-template-columns:1fr;gap:12px}}'; document.head.append(style);
  let queued = false;
  const schedule = () => { if (queued) return; queued = true; setTimeout(() => { queued = false; sync(); }, 80); };
  new MutationObserver(schedule).observe(document.documentElement, { childList: true, subtree: true });
  window.addEventListener('popstate', schedule); window.addEventListener('storage', schedule);
  setInterval(schedule, 1500); schedule();
})();
