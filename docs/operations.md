# 运维

## 安装

在对应架构的 fnOS 应用中心安装：

- `dockfn-<version>-x86_64.fpk`
- `dockfn-<version>-arm64.fpk`

安装向导会主动配置后续入口壳的存储分区。填写 fnOS 存储空间的数字编号即可，例如 `1` 表示 `/vol1`；如果填写的分区不存在，安装回调会回退到 `/vol1`。安装完成后该位置固定，不会自动迁移，也不会改变目标服务、容器、卷或业务数据的存储位置。安装向导先展示说明列表，再显示分区配置，不显示协议确认页。

首次打开必须使用 fnOS 管理员账户。目标服务需要先在宿主机固定端口监听。

打开“新增应用”后会自动扫描本机 Web 服务，也可重新扫描或直接手动填写。扫描结果按 Docker、Docker Host、宿主机分组，分组可折叠；WatchCow `service_port` 标为首选入口。系统服务或无需入口的端口可点击“忽略”，移入默认收起的“已忽略”组，之后可恢复。与 DockFN 或 fnOS 已有入口匹配的候选不可重复创建。选择候选后仍需管理员核对并确认。

核对页统一管理入口信息、打开方式和图标，默认使用 URL；留空的说明和入口 ID 会自动补齐。自定义入口 ID 必须以小写字母开头，只能包含 1–27 位小写字母、数字和内部连字符。appname 和桌面入口统一为 `<ID>.dkfn`，FN Connect 域名、DNS 和证书由 fnOS 生成和管理。

访问路径变化后，DockFN 会重新读取该页面的同源 `<link rel="icon">` 或 `<link rel="shortcut icon">`，并依次尝试访问路径目录及服务根目录下的常见图标文件，包括 `/favicon.*`、`/public/favicon.*` 和 `/logo.*`（`png`、`ico`、`jpg`、`jpeg`、`svg`），也可点击“从路径识别”立即重试。PNG/JPEG/ICO 图标最大读取 2 MiB、最大 4096×4096，并会自动缩放为桌面所需尺寸；仅找到 SVG 时会提示转换后手动上传。识别失败会保留现有图标或默认图标并显示明确提示，不会请求脚本包、执行页面脚本、遍历静态资源或绕过目标服务访问控制。

修改访问路径后，DockFN 会在 400 毫秒输入防抖后读取当前本地服务子页面，优先使用页面声明的同源图标，再检查少量常见图标路径。用户一旦手动填写图标 URI、上传图片或清除图标，本次表单不再自动替换图标。

未配置自定义图标时，核对页预览和最终 fnOS 登记壳都使用 DockFN 默认图标并叠加 DockFN 角标；选择、识别或填写图标后，两处改为同一自定义图标。

点击“确认创建 DockFN 应用”后立即进入第 3 步，安装和入口校验在该页面显示动态进度。成功后可关闭窗口或继续创建；继续创建会清空表单并重新扫描服务。失败时返回核对页，保留原表单和诊断提示供修正后重试。

编辑入口 ID 时，DockFN 会先安装并验证新身份，再移除旧入口；失败时保留或恢复原入口。

## 日志与诊断

FPK 数据目录下：

```text
logs/server.log
logs/helper.log
logs/lifecycle.log
diagnostics/last-discovery.json
diagnostics/last-install-failure.json
```

诊断命令：

```sh
dockfn doctor
dockfn version
```

`doctor` 只报告路径、二进制、socket 和架构状态，不输出认证头或凭据。

管理页面的“诊断”会读取三份日志各最近 32 KiB，并读取最近一次扫描和安装失败报告；展示前会对 `password`、`token`、`authorization`、`cookie` 字段脱敏。诊断列表默认以紧凑行展示文件名和说明，不展开日志正文；点击“查看”后在独立查看器中阅读或复制全文，避免长日志影响页面性能。若创建失败或创建后只在应用中心看到登记壳而桌面没有入口，先查看 `last-install-failure.json` 和 `helper.log`：应用中心安装阶段失败会立即用当前时间、阶段、AppSpec、源 manifest/UI 配置覆盖旧报告；安装完成后的校验会分别读取 `/var/apps/{AppName}/manifest` 与 target 下的 `ui/config`/图标，并把 `DesktopEntryName(AppName)` 的 URL/iframe、字符串端口、`url`、权限或桌面图标不一致列为创建失败。

若扫描结果只有 Docker 服务而没有宿主机服务，查看 `last-discovery.json` 的 `warnings`。DockFN 会依次尝试 fnOS/Debian 常见的四个固定 `ss` 路径；全部不可用或执行失败时仍保留 Docker 候选，并在该字段和 helper 日志中记录宿主机监听扫描失败原因。

诊断窗口的“清空记录”需要二次确认。它只会截断 `server.log`、`helper.log`、`lifecycle.log` 并移除 `last-discovery.json`、`last-install-failure.json`；不会触碰 fnOS、应用中心、Docker、目标服务或其他文件。日志文件保持原路径，权限助手随后写入带 UTC 时间的“已清空”记录，便于区分清理前后的问题。

## 数据备份

停止 DockFN 后复制整个 `data/` 目录即可。`apps.json` 是当前应用配置，`discovery.json` 保存已忽略的发现候选，`icons/` 和 `packages/` 是其文件依赖。恢复时保持文件权限并恢复到同一 FPK 数据目录。

不要单独恢复 ownership 文件来认领外部应用；它必须与原始 `apps.json` 和 fnOS 登记一起恢复。

## 卸载

卸载向导默认“保留应用入口”。显式选择移除时，DockFN 串行卸载配置与 ownership 收据共同确认的登记壳；首个失败会停止卸载准备。

无论选择哪项，都不会停止或删除目标服务、Docker 容器、存储卷和业务数据。

## 失败恢复

- 创建失败：修正端口或应用中心问题后重新提交，配置不会出现半成品。
- 桌面入口校验失败：检查 `diagnostics/last-install-failure.json` 中的 `registryPath`、`installPath`、manifest 和 UI 配置，以及 `helper.log` 中的 `verify fnOS desktop entry`；DockFN 会卸载本次刚创建的不完整登记壳，不会触及目标服务、容器、卷或上游数据。
- 更新失败：当前 AppSpec 保持不变；若旧壳已被卸载，helper 会按内部应用 ID 从 `packages/current` 自动恢复上一成功 FPK（不依赖 Launch ID），可直接重试“修复”。Launch ID 迁移失败还会清理新身份并恢复旧身份；若补偿也失败，诊断会同时列出原始错误与补偿错误。
- 登记壳缺失：使用“修复”重新生成并安装。
- 错误更新成功后：使用“回退”恢复上一次成功内容；只保留一份历史。
