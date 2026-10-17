/* Scoped compatibility panel for the deployed storefront; does not replace its app or payment code. */
(() => {
  'use strict';
  if (window.__uuuaigcAffiliateWallet) return;
  window.__uuuaigcAffiliateWallet = true;
  const route = () => location.pathname.replace(/\/+$/, '') === '/me/affiliate';
  let host, original, loading = false, config, generation = 0, needsRefresh = false;
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
    if (!host || loading) return false;
    loading = true;
    const current = host, revision = generation;
    try {
      const [d, records] = await Promise.all([api('/affiliate/dashboard'), api('/affiliate/commissions?page=1&page_size=20')]);
      if (!route() || current !== host || revision !== generation) return false;
      render(d.data || {}, records.data || [], records.pagination || {});
      needsRefresh = false;
      return true;
    } catch (error) { if (current === host) message(error.message, true); return false; }
    finally { loading = false; }
  }
  async function mutate(path, success) {
    const current = host;
    const buttons = [...current.querySelectorAll('button')].map(button => ({button, disabled: button.disabled})); buttons.forEach(({button}) => button.disabled = true);
    try {
      const r = await api(path, 'POST'); needsRefresh = true;
      const refreshed = await refresh();
      if (current === host) message(refreshed ? success(r.data || {}) : '操作已完成，但余额信息未刷新，请刷新核对。', !refreshed);
    }
    catch (error) { if (current === host) message(error.message, true); }
    finally { if (current === host) buttons.forEach(({button, disabled}) => { if (button.isConnected) button.disabled = disabled || (needsRefresh && button.dataset.action === 'credit'); }); }
  }
  // Outline icons follow the account area’s existing visual language.
  function icon(name) {
    const paths = { gift: ['M12 7v14', 'M3 7h18v4H3z', 'M5 11v10h14V11', 'M12 7H7.5A2.5 2.5 0 1 1 10 4.5L12 7Z', 'M12 7h4.5A2.5 2.5 0 1 0 14 4.5L12 7Z'], copy: ['M9 9h12v12H9z', 'M5 15H3V3h12v2'], arrow: ['M7 17 17 7', 'M7 7h10v10'], refresh: ['M20 7v5h-5', 'M4 17v-5h5', 'M6.1 7a7 7 0 0 1 11.6-2L20 8', 'M4 16l2.3 3A7 7 0 0 0 17.9 17'], receipt: ['M4 3v18l4-2 4 2 4-2 4 2V3l-4 2-4-2-4 2-4-2Z', 'M8 9h8', 'M8 13h6'], chevron: ['m9 5 7 7-7 7'] };
    const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
    for (const [key, value] of Object.entries({ viewBox: '0 0 24 24', fill: 'none', stroke: 'currentColor', 'stroke-width': '1.6', 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true', focusable: 'false' })) svg.setAttribute(key, value);
    for (const d of paths[name] || []) { const path = document.createElementNS(svg.namespaceURI, 'path'); path.setAttribute('d', d); svg.append(path); }
    return svg;
  }
  function render(d, records, pagination) {
    const focusedAction = host.contains(document.activeElement) ? document.activeElement.dataset.action : null;
    host.replaceChildren();
    const heading = el('div', null, 'uaw-heading');
    const symbol = el('span', null, 'uaw-symbol'); symbol.append(icon('gift'));
    const title = el('div'), subtitle = el('p', '邀请好友购买指定 GPT 商品，每单随机奖励 ', 'uaw-muted'); subtitle.append(el('span', '1–5 元。', 'uaw-nowrap')); title.append(el('h2', '推广奖励'), subtitle);
    heading.append(symbol, title); host.append(heading);
    const status = el('p', '', 'uaw-status'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite'); host.append(status);
    const days = Number.isInteger(config.confirm_days) ? config.confirm_days : 7;
    const availability = days <= 0 ? '人工确认收款后可转入本站消费余额' : `付款满 ${days} 天后可转入消费余额`;
    if (d.opened) {
      const stats = el('dl', null, 'uaw-stats');
      [['可转入余额', d.available_commission, days <= 0 ? '人工确认收款后可用' : `付款满 ${days} 天后可用`, 'available'], ['待确认奖励', d.pending_commission, days <= 0 ? '订单确认后更新' : `付款满 ${days} 天后可用`, 'pending'], ['已转入消费余额', d.wallet_credited_commission, '已转入钱包', 'credited']].forEach(([label, amount, hint, kind]) => {
        const item = el('div', null, 'uaw-stat'); item.dataset.kind = kind;
        const value = el('dd'); value.append(el('span', '¥', 'uaw-currency'), document.createTextNode(money(amount)));
        item.append(el('dt', label), value, el('p', hint, 'uaw-muted')); stats.append(item);
      });
      host.append(stats);
      const transfer = el('div', null, 'uaw-transfer');
      const note = el('p', Number(d.available_commission) > 0 ? '转入钱包后，结算时即可使用。' : '有可用奖励后，即可转入消费余额。', 'uaw-muted');
      const actions = el('div', null, 'uaw-actions');
      const wallet = el('a', '查看钱包', 'uaw-wallet'); wallet.href = '/me/wallet'; wallet.append(icon('chevron'));
      const credit = button('转入消费余额', () => mutate('/affiliate/wallet-credit', r => `已转入 ¥${money(r.credited_amount)}，可在本站购买商品时使用。`)); credit.classList.add('uaw-primary'); credit.dataset.action = 'credit'; credit.disabled = !(Number(d.available_commission) > 0); credit.append(icon('arrow'));
      actions.append(wallet, credit); transfer.append(note, actions); host.append(transfer);
      const share = el('div', null, 'uaw-card uaw-share');
      share.append(el('h3', '我的推广链接'), el('p', '好友通过此链接下单，符合条件的订单会计入你的奖励。', 'uaw-muted'));
      const url = new URL(d.promotion_path || `/?aff=${encodeURIComponent(d.affiliate_code || '')}`, location.origin);
      if (url.origin !== location.origin) throw new Error('推广链接异常，请联系客服。');
      const row = el('div', null, 'uaw-link-row');
      const label = el('label', null, 'uaw-link'); label.append(el('span', '我的推广链接', 'uaw-sr-only'));
      const input = el('input'); input.readOnly = true; input.value = url.href; input.setAttribute('aria-label', '我的推广链接'); label.append(input);
      const copy = button('复制推广链接', async () => { try { await navigator.clipboard.writeText(url.href); message('推广链接已复制。'); } catch { input.focus(); input.select(); message('请手动复制已选中的推广链接。'); } }); copy.dataset.action = 'copy'; copy.prepend(icon('copy'));
      row.append(label, copy); share.append(row);
      const activity = el('div', null, 'uaw-activity');
      for (const [label, value] of [['访问次数', d.click_count], ['有效订单', d.valid_order_count]]) { const item = el('p'); item.append(el('span', label), el('strong', String(Number(value) || 0))); activity.append(item); }
      share.append(activity); host.append(share);
      const history = el('div', null, 'uaw-card uaw-history');
      const recordHeading = el('div', null, 'uaw-record-heading'); const recordTitle = el('div'); recordTitle.append(el('h3', '奖励记录'), el('p', '查看奖励状态与转入情况', 'uaw-muted'));
      const reload = button('刷新', refresh); reload.classList.add('uaw-quiet'); reload.dataset.action = 'refresh'; reload.prepend(icon('refresh')); recordHeading.append(recordTitle, reload); history.append(recordHeading);
      if (!records.length) { const empty = el('div', null, 'uaw-empty'); empty.append(icon('receipt'), el('p', '还没有奖励记录'), el('span', '分享上方链接，好友下单并确认收款后即可获得奖励。', 'uaw-muted')); history.append(empty); }
      else {
        const names = { pending_confirm: '待确认', available: '可转入余额', wallet_credited: '已转入余额', rejected: '已扣回', withdrawn: '历史已结算' };
        const table = el('table', null, 'uaw-records'); table.append(el('caption', '奖励记录', 'uaw-sr-only'));
        const thead = el('thead'), tr = el('tr'); for (const label of ['奖励金额', '状态', '创建时间']) { const th = el('th', label); th.scope = 'col'; tr.append(th); } thead.append(tr); table.append(thead);
        const tbody = el('tbody');
        for (const r of records) { const row = el('tr'), amount = el('td', `¥${money(r.commission_amount)}`, 'uaw-record-amount'), state = el('td'), dateCell = el('td', null, 'uaw-record-date');
          const badge = el('span', names[r.status] || '处理中', 'uaw-badge'); badge.dataset.state = Object.hasOwn(names, r.status) ? r.status : 'unknown'; state.append(badge);
          const date = new Date(r.created_at); const time = el('time', Number.isNaN(date.getTime()) ? '日期待更新' : date.toLocaleString('zh-CN', {year:'numeric',month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false})); if (!Number.isNaN(date.getTime())) time.dateTime = date.toISOString(); dateCell.append(time);
          row.append(amount, state, dateCell); tbody.append(row);
        }
        table.append(tbody); history.append(table);
      }
      if (Number(pagination.total) > records.length) history.append(el('p', '当前显示最近 20 笔奖励。', 'uaw-record-note uaw-muted')); host.append(history);
    } else {
      const welcome = el('div', null, 'uaw-card uaw-welcome');
      welcome.append(el('p', '每笔符合条件的订单', 'uaw-muted'), el('p', '¥1–5', 'uaw-welcome-value'), el('h3', '分享给好友，获得消费奖励'), el('p', '开通后获得专属推广链接，奖励可用于购买本站商品。', 'uaw-muted'));
      const open = button('开通推广', () => mutate('/affiliate/open', () => '推广已开通，可以复制链接分享。')); open.classList.add('uaw-primary'); open.dataset.action = 'open'; open.append(icon('arrow')); welcome.append(open); host.append(welcome);
    }
    const rules = el('details', null, 'uaw-rules'); rules.open = !d.opened;
    const summary = el('summary'); summary.append(el('span', '奖励规则'), icon('chevron')); rules.append(summary);
    const rulesList = el('ul');
    for (const text of ['好友通过推广链接购买指定 GPT 商品，每笔符合条件的人民币主订单随机奖励整数 1～5 元。', `${availability}，仅限本站消费，不支持提现。`, '退款将扣回相应奖励；奖励不超过参与商品实付整数金额，不足 1 元不奖励。']) rulesList.append(el('li', text));
    rules.append(rulesList); host.append(rules);
    if (focusedAction) host.querySelector(`[data-action="${focusedAction}"]`)?.focus();
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
    const loadingCard = el('div', null, 'uaw-card');
    const status = el('p', '正在加载推广奖励…', 'uaw-status'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
    loadingCard.append(el('h2', '推广奖励'), status, button('重试', refresh)); host.append(loadingCard);
    panel.after(host); await refresh();
  }
  const style = el('style'); style.textContent = `[data-uaw-hidden]{display:none!important}
.uaw-panel{color:var(--u-ink,var(--ui-text-primary,#171815));font:inherit;min-width:0;--uaw-line:var(--ui-border,var(--u-line,#e7e7e1));--uaw-muted:var(--ui-text-muted,var(--u-ink-500,#6a6c65));--uaw-card:var(--ui-bg-elevated,var(--u-white,#fff));--uaw-soft:var(--ui-bg-soft,var(--u-paper,#fafaf7))}
.uaw-panel *{box-sizing:border-box}.uaw-panel h2,.uaw-panel h3,.uaw-panel p,.uaw-panel dl,.uaw-panel dd{margin:0}.uaw-panel svg{width:18px;height:18px;flex-shrink:0}.uaw-panel h2{font-size:21.6px;font-weight:500;line-height:1.4;letter-spacing:-.3px}.uaw-panel h3{font-size:17px;font-weight:500;line-height:1.5}.uaw-panel .uaw-muted{font-size:13px;line-height:1.7;color:var(--uaw-muted)}
.uaw-heading{display:flex;align-items:center;gap:14px;margin-bottom:24px}.uaw-heading p{margin-top:4px}.uaw-symbol{display:flex;align-items:center;justify-content:center;width:44px;height:44px;flex-shrink:0;background:color-mix(in srgb,var(--u-yellow-500,#e6ff57) 10%,var(--ui-bg-elevated,#fff));color:var(--u-ink,var(--ui-text-primary,#171815));border-radius:12px}.uaw-symbol svg{width:22px;height:22px}
.uaw-status:empty{display:none}.uaw-status:not(:empty){padding:12px 16px;margin-bottom:18px;font-size:14px;line-height:1.6;border:1px solid var(--uaw-line);border-radius:10px;background:var(--uaw-soft)}.uaw-status[data-error=true]{color:var(--ui-danger,#b42318);border-color:currentColor}
.uaw-stats{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:16px}.uaw-stat{padding:22px 24px;border:1px solid var(--uaw-line);border-radius:16px;background:var(--uaw-card);min-width:0}.uaw-stat dt{font-size:13px;line-height:1.5;color:var(--uaw-muted)}.uaw-stat dd{font-size:28px;line-height:1.25;font-weight:500;font-variant-numeric:tabular-nums;letter-spacing:-.7px;margin:12px 0 8px;overflow-wrap:anywhere}.uaw-currency{font-size:18px;margin-right:5px;font-weight:400;letter-spacing:0}.uaw-stat[data-kind=available]{border-color:var(--u-line-strong,var(--ui-border-strong,#cfcfc7));background:color-mix(in srgb,var(--u-yellow-500,#e6ff57) 10%,var(--ui-bg-elevated,#fff))}
.uaw-transfer{display:flex;justify-content:space-between;align-items:center;gap:12px;padding:18px 0 24px}.uaw-actions{display:flex;align-items:center;gap:16px;flex-shrink:0}.uaw-button{display:inline-flex;justify-content:center;align-items:center;gap:8px;min-height:44px;padding:10px 16px;border:1px solid var(--uaw-line);border-radius:10px;font:inherit;font-size:14px;font-weight:400;line-height:1.4;background:var(--uaw-card);color:inherit;cursor:pointer;white-space:nowrap;transition:background-color .15s,border-color .15s}.uaw-button:hover{background:var(--uaw-soft)}.uaw-button.uaw-primary{background:#e6ff57;border-color:#e6ff57;color:#17200a}.uaw-button.uaw-primary:hover{background:#d4ee43;border-color:#d4ee43}.uaw-button:disabled{cursor:not-allowed;background:var(--uaw-soft);border-color:var(--uaw-line);color:var(--uaw-muted)}.uaw-button:focus-visible,.uaw-link input:focus-visible,.uaw-wallet:focus-visible,.uaw-rules summary:focus-visible{outline:2px solid #829922;outline-offset:3px}.uaw-wallet{display:inline-flex;align-items:center;justify-content:center;gap:5px;min-height:44px;font-size:13px;text-decoration:none;color:var(--uaw-muted)}.uaw-wallet:hover{color:inherit;text-decoration:underline}.uaw-wallet svg{width:14px;height:14px}
.uaw-card{border:1px solid var(--uaw-line);border-radius:16px;background:var(--uaw-card);padding:24px;margin-bottom:24px}.uaw-share>p{margin-top:6px}.uaw-link-row{display:flex;gap:12px;margin-top:18px}.uaw-link{display:block;flex:1;min-width:0}.uaw-link input{display:block;width:100%;min-height:46px;font:inherit;font-size:16px;color:var(--u-ink-700,var(--ui-text-secondary,#454741));border:1px solid var(--uaw-line);border-radius:10px;background:var(--uaw-soft);padding:12px 14px;text-overflow:ellipsis}.uaw-activity{display:flex;gap:28px;margin-top:20px;padding-top:16px;border-top:1px solid var(--uaw-line);font-size:13px;color:var(--uaw-muted)}.uaw-activity p{display:flex;align-items:baseline;gap:12px}.uaw-activity strong{font-size:16px;font-weight:500;font-variant-numeric:tabular-nums;color:var(--u-ink,var(--ui-text-primary,#171815))}
.uaw-record-heading{display:flex;justify-content:space-between;align-items:center;gap:16px;margin-bottom:20px}.uaw-record-heading p{margin-top:4px}.uaw-quiet{font-size:13px}.uaw-records{width:100%;border-collapse:collapse;text-align:left;font-size:13px;line-height:1.6}.uaw-records th{padding:12px 16px;font-size:12px;font-weight:400;color:var(--uaw-muted);background:var(--uaw-soft);border-block:1px solid var(--uaw-line)}.uaw-records td{padding:17px 16px;border-bottom:1px solid var(--uaw-line)}.uaw-records tbody tr:last-child td{border-bottom:0}.uaw-records th:last-child,.uaw-records td:last-child{text-align:right}.uaw-record-amount{font-weight:500;font-size:15px;font-variant-numeric:tabular-nums}.uaw-record-date{font-size:12px;color:var(--uaw-muted);font-variant-numeric:tabular-nums}.uaw-badge{display:inline-flex;padding:3px 9px;border:1px solid var(--uaw-line);border-radius:6px;font-size:12px;line-height:1.5;white-space:nowrap;background:var(--uaw-soft);color:var(--u-ink-700,var(--ui-text-secondary,#454741))}.uaw-badge[data-state=available]{background:color-mix(in srgb,var(--u-yellow-500,#e6ff57) 10%,var(--ui-bg-elevated,#fff));color:var(--u-ink,var(--ui-text-primary,#171815))}.uaw-badge[data-state=rejected]{color:var(--ui-text-muted,var(--u-ink-500,#6a6c65))}.uaw-empty{display:flex;flex-direction:column;align-items:center;text-align:center;padding:30px 16px 24px;border-top:1px solid var(--uaw-line)}.uaw-empty>svg{width:28px;height:28px;color:var(--uaw-muted);margin-bottom:12px}.uaw-empty>p{font-size:14px;margin-bottom:6px}.uaw-empty>span{max-width:340px}.uaw-record-note{padding-top:14px;border-top:1px solid var(--uaw-line)}
.uaw-rules{padding:0 4px}.uaw-rules summary{display:flex;align-items:center;justify-content:space-between;min-height:44px;cursor:pointer;font-size:14px;list-style:none}.uaw-rules summary::-webkit-details-marker{display:none}.uaw-rules summary svg{width:16px;height:16px;transition:transform .15s}.uaw-rules[open] summary svg{transform:rotate(90deg)}.uaw-rules ul{margin:8px 0 0;padding-left:18px;color:var(--uaw-muted);font-size:13px;line-height:1.9}.uaw-rules li+li{margin-top:5px}.uaw-welcome{padding:32px}.uaw-welcome .uaw-welcome-value{font-size:44px;line-height:1.2;font-weight:500;letter-spacing:-1px;margin:12px 0 20px}.uaw-welcome h3+p{margin-top:6px}.uaw-welcome button{margin-top:22px}.uaw-nowrap{white-space:nowrap}.uaw-sr-only{position:absolute;width:1px;height:1px;padding:0;margin:-1px;overflow:hidden;clip:rect(0,0,0,0);white-space:nowrap;border:0}
@media(max-width:900px){.uaw-stat{padding:18px}.uaw-stat dd{font-size:26px}.uaw-transfer{align-items:flex-start;flex-direction:column}.uaw-actions{align-self:flex-end}}
@media(max-width:600px){.uaw-heading{margin-bottom:20px;gap:12px}.uaw-panel h2{font-size:20px}.uaw-stats{grid-template-columns:repeat(2,minmax(0,1fr));gap:12px}.uaw-stat[data-kind=available]{grid-column:1/-1}.uaw-stat[data-kind=available] dd{font-size:34px}.uaw-stat dd{font-size:25px;margin:10px 0 6px}.uaw-stat{padding:18px 16px}.uaw-stat .uaw-muted{font-size:12px}.uaw-transfer{padding:16px 0 20px;gap:10px}.uaw-actions{width:100%;justify-content:space-between}.uaw-card{padding:20px;margin-bottom:20px}.uaw-link-row{flex-direction:column;gap:10px}.uaw-link-row>.uaw-button{align-self:stretch}.uaw-record-heading{margin-bottom:16px}.uaw-record-heading p{font-size:12px}.uaw-records thead{position:absolute;width:1px;height:1px;overflow:hidden;clip:rect(0,0,0,0)}.uaw-records tr{display:grid;grid-template-columns:1fr auto;padding:16px 0;border-bottom:1px solid var(--uaw-line);gap:6px}.uaw-records tbody tr:last-child{border-bottom:0}.uaw-records td{padding:0;border:0}.uaw-records td:nth-child(2){text-align:right}.uaw-records td:last-child{grid-column:1/-1;text-align:left}.uaw-empty{padding:24px 0 8px}.uaw-welcome{padding:24px}.uaw-rules{padding:0}.uaw-activity{gap:20px}.uaw-activity p{gap:8px}}
@media(prefers-reduced-motion:reduce){.uaw-panel *{transition:none}}
`; document.head.append(style);
  let queued = false;
  const schedule = () => { if (queued) return; queued = true; setTimeout(() => { queued = false; sync(); }, 80); };
  new MutationObserver(schedule).observe(document.documentElement, { childList: true, subtree: true });
  window.addEventListener('popstate', schedule); window.addEventListener('storage', schedule);
  setInterval(schedule, 1500); schedule();
})();
