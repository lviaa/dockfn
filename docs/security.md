# 安全模型

## 身份边界

正式 FPK 只监听 fnOS 应用网关 Unix socket。只有网关 adapter 可以把 `X-Trim-*` 头转换为进程内 Actor；HTTP 鉴权模块从不直接读取这些头。所有管理 API 都要求 fnOS 管理员。

开发 TCP 模式只允许回环地址，并要求显式 `DOCKFN_DEV_ADMIN=1`。它不属于正式部署。

## 权限助手

真机证据显示普通包用户不能访问应用中心 RPC。root helper 因此只公开以下五个 Unix-socket 动作：

- `install`
- `update`
- `remove`
- `discovery`（只读）
- `diagnostics`（只清空固定的 DockFN 诊断白名单）

请求只接受固定 JSON 字段；不接受命令、环境变量或绝对路径。install/update 的源目录必须是 staging 的后代，且通过登记壳所有权、manifest、AppSpec、普通文件和无符号链接校验。remove/update 必须已有 DockFN ownership 收据；install 只接受由完整模板生成的安全小写 fnOS ID，并拒绝与应用中心已有 appname 冲突。

`discovery` 固定只允许 `appcenter-cli list --json`、`docker ps --quiet`、`docker inspect <container-id>` 和 `ss -H -ltnp`。`ss` 只会从 `/usr/bin`、`/usr/sbin`、`/bin`、`/sbin` 四个固定系统路径中选择，不接受外部命令路径。监听结果仅保留原生 IPv4 和可通过 IPv4 探测成功的双栈通配端口；具体 IPv6 地址及 IPv6-only 监听不进入候选。Docker 绑定同样只接受 IPv4 发布地址。它还只读应用中心返回的受约束安装目录下 `ui/config`、监听 PID 的 `/proc/<pid>/cgroup` 和 PID namespace 链接，用于端口重复匹配及 host 网络容器归属；解析前会校验安装根目录。它不接受 Docker 命令、镜像名、容器名或任意路径输入，也绝不执行 Docker 生命周期操作。helper 子命令使用固定 PATH/LANG、固定 fnpack/appcenter-cli 参数、45 秒默认超时和 64 KiB 脱敏输出上限。

`diagnostics` 不接收请求体或路径参数，只能截断 `logs/` 下三份固定普通文件并移除 `data/diagnostics/` 下两份固定报告。目录访问使用受根目录约束的文件 API，拒绝把日志名解析为非普通文件；清理流程没有 fnOS、应用中心、Docker 或目标服务调用。

安装向导的入口壳分区字段只接受 1–999 的正整数；安装回调会检查对应的 `/vol{n}` 目录，不存在时回退到 `/vol1`，然后写入 DockFN 包配置。运行时仅读取该固定配置，不接受管理页面覆盖。

## 输入与文件

- ID 固定为 12 位小写十六进制。
- 应用 ID 只允许 1–27 位 ASCII 小写字母、数字和内部连字符并以字母开头；完整 fnOS ID 模板必须包含一个 `{id}` 和至少一个固定标识，渲染结果必须以字母开头、使用安全的点分小写标签且不超过 63 位。应用名称转拼音只生成建议值，服务端仍对建议、手工值和最终完整 ID 分别校验。应用 ID 变更必须走安装新壳、验证、移除旧壳和失败补偿流程。
- 协议只允许 HTTP/HTTPS，端口为 1–65535。
- path 必须以 `/` 开头，拒绝反斜线、查询串、片段、空段和编码后的 `.`/`..`。
- 图标只允许 PNG/JPEG/ICO，编码前最大 2 MiB，解码尺寸最大 4096×4096；接收到的大图会在保存前缩放为 64×64 和 256×256。ICO 只接受 PNG 封装或常见未压缩 24/32 位 DIB；SVG 只作为发现路径提示，不在服务端执行脚本或渲染，需转换后手动上传。浏览器没有提供 MIME 类型时可使用通用二进制 data URL，但后端始终以实际图片内容解码结果为准，不信任扩展名或 MIME 声明。
- 图标 URI 允许相对于当前目标服务的路径、显式 `host:port/path`，以及绝对 `file:`、`http:` 和 `https:`。相对路径固定解析为当前协议下的 `127.0.0.1:<目标端口>`；禁止 URI 内嵌账号凭据和 `//host` 形式，远程读取没有认证头，8 秒超时、最多 3 次同协议 HTTP(S) 重定向，并受同一大小/图片校验限制。该行为只可由 fnOS 管理员触发。
- 服务扫描只从已读取根页记录同源 `link` 图标提示，不发起图标请求。管理员选择候选后，核对页才通过受认证的预览接口依次验证该提示和固定常见路径；每次请求有短超时，不访问脚本、manifest、子页面或凭据，并拒绝返回 SPA HTML 的伪 favicon。
- 核对页修改访问路径后，图标发现接口只读取 `127.0.0.1:<目标端口><访问路径>`，不接受主机参数、认证信息或跨源重定向；页面最多读取 64 KiB，仅采用同协议、同主机的 `link` 图标，再按既有图片下载和内容校验规则生成预览。若页面提示不可用，有限回退路径覆盖 `/favicon.{ico,png,jpg,jpeg,svg}`、`/public/favicon.{ico,png,jpg,jpeg,svg}` 和 `/logo.{ico,png,jpg,jpeg,svg}`。手动图标状态始终优先，不会被异步发现结果覆盖。
- AppSpec 写入采用同目录临时文件、fsync 和原子 rename。
- 全局配置通过管理员接口整份替换并原子写入 `settings.json`；创建模块直接读取该配置，应用创建请求不能携带或覆盖全局模板。
- 产品说明允许为空，但生成 fnOS manifest 时仅以已验证的显示名称补齐 `desc`；两者都经过控制字符和长度校验，不接受原始 manifest 文本。
- 日志不输出 Cookie、Token、Authorization、密码或 URL 凭据。

应用中心报告安装成功并不构成 DockFN 创建成功：helper 还会检查实际安装目录 manifest、`ui/config` 中 `DesktopEntryName(AppName)` 对应的 URL/iframe 类型、字符串端口、`url`、协议、用户权限，以及可解码的 `ui/images/icon_64.png`/`icon_256.png`。检查失败时不会写入 DockFN ownership 收据或 AppSpec，并会在诊断报告和 helper 日志中保留脱敏失败原因。

## 删除保证

删除前同时要求：

1. AppSpec 存在；
2. appname 属于 DockFN 安全格式；
3. ownership 收据属于 DockFN；
4. 应用中心确认卸载结果。

删除路径没有 Docker、卷、目标服务或业务数据调用。DockFN 包卸载默认保留生成的应用入口。
