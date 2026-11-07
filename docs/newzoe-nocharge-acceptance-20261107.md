# NewZoe 不扣款流程验证 — 2026-11-07

用户限定只做不扣款测试。本轮使用隔离内存 SQLite、模拟支付网关/队列，以及拦截所有请求的 NewZoe HTTP transport；没有真实支付、供应商采购、扣余额或向生产注入回调。生产仅执行只读配置与映射检查。

## 补齐的覆盖

- NewZoe 采购生命周期扩展至 3 / 21 / 22 / 24 / 34，包含 Codex、Muse 和 Figma。每款分别验证当前提交版本和旧版本已有回执的轮询兼容。
- Figma 使用 synthetic-password 与 synthetic-mail-token，验证凭据载荷完整交付。
- 成功采购后再次提交被拒绝；重复完成轮询只有一条交付记录；订单变为 delivered；超龄失败订单不再次采购。
- 新支付衔接测试覆盖创建支付时不采购；金额/币种不符保持 pending_payment；成功确认及三次重复回调只派发一次采购；上游商品不误走本地卡密队列。

## 实际执行

1. upstream、procurement integration、fulfillment integration、order integration/application、payment integration：8 个测试包、144 项测试/子测试通过。
2. upstream、procurement/...、fulfillment/...、order integration/application、payment/application 的 race 检查：6 个有测试的包、282 项测试/子测试通过，无 race 报告。
3. 新增支付衔接测试单独 race：4 项测试/子测试通过。
4. order/application race：62 项测试/子测试通过。
5. 上述核心模块 go vet 通过；git diff --check 通过。

计数包含父测试及子测试，不可相加当作独立场景数。现有回归覆盖支付签名/金额/币种拒绝、并发支付确认、采购并发唯一提交、响应丢失及回执落库失败、退款/取消后迟到交付不泄露凭据、凭据加密、队列失败后对原采购单重试。

日志保存于 storefront 的 output/newzoe-nocharge-20261107/，原执行日志 /tmp/newzoe-nocharge-20261107/。

## 线上只读检查

API 与 Redis 容器 healthy；redis.enabled=true，queue.enabled=true，queue.concurrency=10。支付服务向采购服务的接线已核对源码。

连接 1 为 newzoe，自动跟价开启，markup=100%，ceil_int。映射 3/21/22/34 均启用、商品为 upstream；观测售价分别 10/10/15/81，库存分别 45/41/7/2（浮动快照，不是锁定报价）。

## 结论与边界

本轮未发现已测正常配置下的运行逻辑故障，补齐了 Figma/当前版本及支付到采购派发的测试缺口；仅修改测试，无需部署生产代码。

这些是分层集成测试，不是浏览器到真实支付/供应商的完整实单。未验证真实支付渠道到账、供应商实际交付速度、账号可登录或实际订阅权益。采购队列失败有失败状态及后台重试；队列配置关闭时不会派发采购，不能宣称自动恢复。当前线上队列已启用。
