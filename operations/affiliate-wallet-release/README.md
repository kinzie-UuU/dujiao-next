# 全站推广奖励发布

用户已确认：全站商品每笔符合条件的人民币主订单随机奖励整数 1–5 元，管理员核实到账并确认收款后即可转入本站消费余额（confirm_days=0），无需等待 7 天，不可提现；退款自动扣回。使用 `all_products: true` 和空 `eligible_product_ids` 明确开启全部商品，未来新增商品无需枚举 ID；未开启该标记的历史配置仍按其指定名单处理。低额订单奖励不超过符合条件净支付整数金额；不足 1 元不计奖。自推不计奖，同一主订单不重复抽取。内部 `gpt_random_wallet` 模式名为兼容历史账目保留，不代表 GPT 商品限制。

历史首发在 ce9c85e9 安全基线上合并 02294963 钱包奖励分支，保留付款金额与币种校验、支付渠道 TOTP 和重试保护，修正父子退款重复累计。此次 API 候选必须从最新合并源构建，包含 179775134a41 库存提醒功能和已上线支付安全改动；禁止用旧二进制替换。

API/admin 控制沿用 `../security-backports-release/README.md` 中 SHA 固定的两个控制器。`release.py` 锁定已审查生产基线 `dujiao-next:payment-security-179775134a41-27bc44a2ade5`，二进制 SHA256 为 `27bc44a2ade595c76b90fcb3298ef4f689e21974062bda6890f5581fb0b1989f`。发布时重新 inventory，若实际基线漂移必须审查新变更，不能放宽守卫。依次 inventory、plan、deploy；校验精确库存摘要与计划摘要，备份 SQLite，保留 live admin overlays，失败只回滚代码。使用合并后完整 revision、干净独立克隆构建的 Linux amd64 fullstack 二进制及对应 admin dist，并校验 `go version -m` 的 revision / modified=false。

必须先上线支持 `all_products` 的新 API 并验证，再运行 `enable.py --binary-sha256 <新版本实际SHA>` 作只读检查，最后加 `--production-write` 写入唯一 `affiliate_config`。脚本持有生产锁、校验二进制及迁移列，仅允许从精确的六商品/0 天原配置升级，兼容原配置中显式的 `all_products:false`；7 天配置或任何其他漂移均拒绝。新建或升级前核验全部启用收款渠道仍为 manual_qr 且没有待确认历史奖励，先备份数据库，并在事务内重新核对配置、渠道和待确认奖励。不再以六个 GPT 商品的在售状态限制全站配置，不修改商品、价格库存、支付配置或用户账目，也不补发历史订单奖励。出现历史待确认奖励应停止发布并单独核对。公开配置缓存需等待自然过期并回读；无需清空 Redis。不要启用旧现金返利商品开关。

`approved-config.json` 是用户批准的目标配置，不代表已应用生产。旧 `docs/gpt-wallet-rewards.disabled.json` 保留为原分支历史草案。发布后分别记录二进制 SHA、实际数据库与公开 `all_products:true` 配置、全站 UI 文案、商品库存不变和本地非 GPT / 退款回归证据。单元测试不可宣称真实支付验收。

定时刷新持久化：`persist.py` 以相同脚本摘要更新 `/opt/uuuaigc-prerender/template.html`、刷新资产列表和持久 JS，使用全局生产锁及刷新任务 mkdir 锁、三个文件的精确哈希计划、备份和失败恢复。发布窗口应记录并暂时停止当前 active 的 prerender/reset/price 定时器，等待正在运行的刷新完成，重新盘点用户资源后执行 user_release 和 persist，再恢复原有定时器；禁止忽略状态漂移继续部署。强制一次跳过索引提交的 prerender 刷新并回读脚本，验证不会被下周期覆盖。
