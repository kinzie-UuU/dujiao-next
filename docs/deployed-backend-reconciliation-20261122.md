# 已上线后端源码归并（2026-11-22）

本次仅整理 Git 代码，不重新部署生产，不执行采购、支付、业务数据迁移或备份任务。

## 来源与边界

- GitHub main 基线：`7456134`。
- 已部署构建源：`fb46c0fdad7f1a38b56c472fc59db3b343c1bcb6`。
- 实际运行 API 二进制 SHA256：`c0edafa1685c39ca869fe8396f6dcb7ef1a09aaf084ccbf2b1e7c5e69bd29ccd`。
- 当前镜像：`sha256:8aae9ac064a7d4d76a0e3a79be52e81a56ddb6740f1f2f3896cb6a5785b30215`。镜像旧 labels 不代表实际运行二进制来源。
- 部署闭包依据为商城工作区 `operations/gmail-delivery-labels-20261121/BACKEND-VERIFICATION.md` 和 `operations/plus-auto-delivery-20261120/` 所记录的源码、构建与实机摘要；私有构建/生产输出不纳入仓库。

只纳入该源的 Go 后端及配套测试、技术说明，包括安全审计与通知、采购一次性提交及队列恢复、Fang Fang 备援采购、Gmail/Plus 支付派发与交付隐私。未核实独立上线来源的 admin 前端、Qwanhai 草稿和本地其他功能不纳入。

以三方整合保留 main 的广告代理删除及 SMTP 品牌修复；归并后的 `internal/` 相对已部署源仅保留 main 的广告移除和对应测试。因此整合版本不声称与生产二进制完全相同，也不据此自动重新发布。

## 本地验证

新增/变更 Go 文件 gofmt 和 git diff --check 通过。对本次路径扫描常见凭据字面量无命中，未纳入生产数据库、密钥、构建产物或私有输出。

Go 1.26.10 默认工具链的漏洞扫描报告标准库 GO-2026-6859；Go 1.26.11（现行生产发布工具链）源码扫描为 0 项漏洞。未为绕过检查修改工作流或依赖。

- Go 1.26.10：`go test ./...` 全库通过（204 个有测试的包，含架构约束）；`go vet ./...` 通过；所有跟踪 Go 文件 gofmt 通过。
- Go 1.26.11：`go test -race ./internal/modules/procurement/... ./internal/bootstrap/adminauth ./internal/modules/order/...` 通过（15 个有测试的包，含备援采购、退款围栏及通知），无 data race。
- Go 1.26.11：`go build ./cmd/server` 通过；该实际生成二进制的 `govulncheck -mode=binary` 无漏洞。

一次初始 race 命令误列不存在的独立 backupprocurement 路径，返回 setup failed；备援模块实际位于 procurement 内，修正路径后的上述完整命令成功。未修改应用或测试以隐藏失败。

## 独立核实的 Fang Fang 管理端

后端提交后另行摘取只读连接及报价管理 UI 的 5 文件提交 `eba208d69712fa9e99b98b30c1ee013b827cd498`（本分支为 `bc2637f`），不混入其他 admin 前端改动。独立核验的 patch-id 与线上源 `f804e1683e4cf1721741091095f11f462ce93d6d` 相符，2038 个线上静态文件摘要核对相符；该独立候选 Vitest 4 项、vue-tsc、Vite build 均通过。摘取无冲突，不修改 Go 代码。本记录区分该独立 UI 验证与上述后端验证；没有重新部署整合结果。
