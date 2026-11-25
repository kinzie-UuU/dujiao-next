# 验证码品牌邮件上线（2026-11-25）

用户明确授权上线后，生产于北京时间约21:48完成受控切换与回读。范围仅注册、重置密码、Telegram绑定、新旧邮箱更换验证码的主站邮件卡片；其他品牌、订单邮件、验证码生成/校验及有效期保持。

## 发布身份

- 应用源码：`800dfb7231361c424450e72e1dbbd2365999ae49`，分支 `codex/verification-email-production-20261125`，已提交并推送。
- 从实际生产 `fb46c0fdad7f1a38b56c472fc59db3b343c1bcb6` 独立分支，仅6文件；未带入设计基线的广告/后台改动。
- 镜像：`sha256:37f472f35bf1148fbe97c987a20fc140da4a92939cb8cd98e054df2f70f009ae`。
- 运行二进制 SHA256：`85e5d37a1aeb9d44b7fb54444368cab77c6945637a50a0bc994f512a3b50d5b3`。
- generation：`c3889e47784d47218f1ae65088961501`。
- lifecycle transaction：`279fd2f3eb56401d95072c73036f5e92`，`verified` / `committed`。
- API container：`b7e9e9b7f8006b4dfe1106e1d585949c662441b1870e4321389f085886b12464`。

## 验证

Go 1.26.11 完整测试206包、vet、SMTP/mailbrand/userauth integration race全部通过；额外验证码系列race通过。源码govulncheck无发现。前后端安装/测试/构建、内嵌资源检查通过；346个内嵌前端资源与生产旧版逐文件相同。跨版本兼容与五阶段Linux smoke通过（baseline→candidate→baseline→candidate_return→candidate_empty，含空库、合成登录和只读订单）。邮件15种语言/用途组合、150组视口/主题预览及200%放大验收见设计记录。

线上镜像、/proc实际程序SHA、Compose候选引用和品牌URL匹配；健康接口200、重启0；公开浏览器首页和配置接口200；fence released、generation gate已清除，worker心跳1、critical/default队列均未暂停。启动后41条日志采样无error级事件。非浏览器urllib曾被403拒绝，实际浏览器访问成功。

未额外发送真实邮件、验证码或操作用户账号；QQ/Gmail/Outlook真实客户端收信及强制反色仍未试验。上线与健康回读不等于真实收信验收。

## 备份与登记修正

完整加密备份：`/var/lib/uuuaigc-storefront-backups/storefront-20261125T131456Z.enc`，恢复检查7930个文件，独立密文SHA校验通过。商城自动备份仍为manual，未启用timer。

为满足原9GiB备份安全余量，一份已在Mac验证的11/19 20:40旧密文和meta复制至 `~/Library/Application Support/UuUAiGC/storefront-backup/historical-20261125-email-release`，逐文件hash/fsync后双锁CAS迁除服务器重复副本，保留其他两包。释放909101237字节，无代码或业务数据清理；Mac原packages副本亦保留。

首次lifecycle请求在只读admit阶段被startup摘要漂移拒绝，原API未切换且健康。独立重构仅两项既有授权差异：11/22禁用商城备份timer的wants链接，11/24异地监控timer晚间窗口。两项逆变换精确恢复登记旧摘要。可恢复六文件登记链修正经过13项中断/漂移测试和独立审查，只更新startup→audit→approval及同generation after哈希，保留历史；未改timer、API、网络或业务数据。重新通过原admission后发起上述成功事务。旧程序保留为受控baseline回退，禁止恢复数据库。

证据位于商城工作区 `output/verification-email-release-20261125/`，包含源码归档、构建manifest、完整测试、安全扫描、备份校验、初次拒绝、精确startup重构、登记修正和最终切换回执；私有登记材料不进入Git。
