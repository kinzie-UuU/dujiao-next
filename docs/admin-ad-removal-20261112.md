# 后台第三方广告移除（未部署）

原始开发基线：`786c06e6015662ef010255c095fb3add219f2fc2`；原始去广告提交：`132eaa4df5e89125f88eb101cb5683b7494b6523`。工作分支：`codex/remove-admin-ads-20261112`。

移除后台首页三个第三方广告位、广告组件、API 客户端方法、类型和无用翻译；保留原有十二张业务统计卡片、订单/支付告警、库存提示及刷新功能。后端删除广告代理模块、容器装配、两个广告路由和对应内建角色权限，从源码中移除广告网关与曝光请求。旧版后台即使仍请求广告接口，也不再有后端广告代理可以执行外连。

采购、支付、通知、Telegram、下游回调、库存同步与定时任务的业务代码未修改。未操作数据库、生产配置、服务或部署，也没有创建订单、付款或发送通知。

## 原始开发基线的本地验证

- `go test ./...`：195 个有测试的包通过。
- `go vet ./...`：通过。
- `go test -tags release ./internal/architecture ./internal/app/httpserver ./internal/authz`：通过。
- `go test -race ./internal/upstream ./internal/modules/procurement/integrationtest ./internal/modules/payment/application ./internal/modules/notification/application`：通过；使用隔离测试替身，无真实采购或支付。
- `go build -tags release -o /tmp/dujiao-noads-api ./cmd/server`：通过；检查产物不含原广告网关域名及曝光上报地址。
- 后台前端：12 个测试文件、63 项测试通过；`vue-tsc` 和 Vite 构建通过。
- 构建后的 105 个后台 JS 文件中未发现原广告网关、广告接口或曝光方法。
- Playwright 使用实际 Dashboard 组件、本地主容器与侧栏占位、拦截后的测试数据，在 1440/390 宽度的浅色和深色四组合检查：12 张业务卡片保留，桌面四列三行、手机单列，无广告空容器或横向溢出。四类业务请求均由本地替身响应，未请求生产 API 或广告服务。这不是线上完整后台验收。
- 页面截图位于商城工作区 `output/playwright/admin-ad-removal-20261112/`；临时页面和预览服务已清理。
- 新回归保护检查后台加载与刷新时完整请求集合，仅包含四类业务数据请求，同时核对金额、库存及失败支付提示。后端保护广告网关、路由和权限不被重新引入；保留原有架构文件数约束。

以上为本地结果。尚未部署，线上广告行为不会因此自动改变。后续上线需要同时采用本次 API 与后台前端产物，不能仅隐藏广告组件就宣称后端停止上报。


## 后端 main 合并前复验

远端 `main` 基线：`906f46e85724522d2a6dbb37efb4b66e42f3e3cf`。在干净副本中只移植去广告提交；原始代码补丁与移植后的稳定 patch-id 同为 `a4a81b00cfd0fc917b6acf53cc15e69a1b190774`。随后仅补充本节验收记录。未引入原开发分支的 Muse 接入、支付告警或其他历史提交；采购、支付、通知及定时任务目录相对远端 main 无差异。

- `go test ./...`：190 个有测试的包通过；`go vet ./...` 通过。
- 上述四个业务包的 `-race` 检查、release 标签下架构/路由/权限测试及 release 构建均通过。
- 按 CI 版本 Node 24.11.1、pnpm 10.34.3，以冻结锁文件安装两端依赖；锁文件无变化。
- SVG 策略 10 项测试及 507 个前端文件扫描通过。
- Admin 10 个测试文件、57 项测试通过；User 61 项测试通过。测试集合与原始开发基线不同，不能沿用原来的计数。
- Admin `build:fullstack`、User `build`（均包含类型检查）通过；嵌入两端资产后 `go build -tags release,fullstack` 通过。
- Admin 产物没有广告接口、曝光标记或广告位标识；保留 `__DJ_ADMIN_BASE__`；后端产物没有原广告网关或曝光地址。
- GoReleaser v2.20.1 `check` 通过，仅解析发布配置，没有执行 release 或镜像发布。
- `git diff --check` 通过。原始浏览器截图仍是前节注明的本地页面验收，不冒充本次线上验证。

以上复验均为本地执行；GitHub 托管检查状态另见 PR。合并不代表部署，不创建发布标签、不运行手动发布工作流。
