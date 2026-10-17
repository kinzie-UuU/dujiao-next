# GPT 推广奖励发布

用户已确认：当前在售 GPT 商品每笔符合条件的人民币主订单随机奖励整数 1–5 元，管理员核实到账并确认收款后即可转入本站消费余额（confirm_days=0），无需等待 7 天，不可提现；退款自动扣回。参与商品 ID 1/2/3/19/22/23 已于 2026-10-17 重新核对。低额订单奖励不超过符合条件净支付整数金额；不足 1 元不计奖。自推不计奖，同一主订单不重复抽取。

此发布在 ce9c85e9 安全基线上合并 02294963 钱包奖励分支，保留付款金额与币种校验、支付渠道 TOTP 和重试保护。修正两分支合并时父子退款重复累计，并补 Capture 欠额提示。

API/admin 控制沿用 `../security-backports-release/README.md` 中 SHA 固定的两个控制器，生产基线更新为 ce9c85e9 / cc81d6857b10。依次 inventory、plan、deploy；校验精确库存摘要与计划摘要，备份 SQLite，保留 live overlays，失败只回滚代码。

API 上线且前台入口验证后，运行 `enable.py --binary-sha256 <新版本实际SHA>` 作只读检查，再加 `--production-write` 写入唯一 `affiliate_config`。脚本持有生产锁、校验二进制/迁移列/商品名单、仅允许精确的原 7 天配置升级为 0 天，拒绝其他配置漂移；升级前核验全部启用收款渠道仍为 manual_qr 且没有待确认历史奖励、先备份数据库；不修改价格库存、支付配置或用户账目。当前运行 API 已支持 0 天，无需替换支付二进制；如出现历史待确认奖励则停止发布并单独核对，不能遗漏。配置读取直达数据库，无需清空 Redis。不要启用旧现金返利商品开关。

`approved-config.json` 是用户批准的目标配置，不代表已应用生产。旧 `docs/gpt-wallet-rewards.disabled.json` 保留为原分支历史草案。发布实测结果另记录，单元测试不可宣称真实支付验收。

定时刷新持久化：`persist.py` 以相同脚本摘要更新 `/opt/uuuaigc-prerender/template.html`、刷新资产列表和持久 JS，使用全局生产锁及刷新任务 mkdir 锁、三个文件的精确哈希计划、备份和失败恢复。发布窗口应记录并暂时停止当前 active 的 prerender/reset/price 定时器，等待正在运行的刷新完成，重新盘点用户资源后执行 user_release 和 persist，再恢复原有定时器；禁止忽略状态漂移继续部署。强制一次跳过索引提交的 prerender 刷新并回读脚本，验证不会被下周期覆盖。
