# 架构与模块

DockFN 使用一个非 root Go 业务进程，Vue 产物通过 `embed.FS` 内嵌。独立的 root 权限助手负责 fnOS 要求的 `fnpack` 和应用中心调用。

```text
fnOS 网关
   │ 管理员身份 + Unix socket
   ▼
internal/http ──► internal/app
                     ├── internal/config   apps.json 原子写
                     ├── internal/package 最小登记壳
                     └── internal/fnos    install/update/remove/discover
                                              │
                                              ▼
                                       root 权限助手
                              fnpack + appcenter-cli + 只读扫描
```

## 核心术语

- **AppSpec**：一个已成功登记入口的当前配置；可包含创建时选中的来源快照，仅用于展示和搜索。
- **登记壳**：DockFN 生成并安装的最小 FPK，只包含入口、图标、生命周期脚本和所有权信息。
- **目标服务**：已经通过固定宿主机端口提供 HTTP/HTTPS 的外部服务。
- **发现候选**：管理员手动扫描得到的临时结果，经核对和确认后可创建为入口。
- **所有权收据**：DockFN 完成安装后写入的本地证据，用于限制后续更新和删除范围。

## 模块职责

`internal/app` 封装 create、update、check、repair、rollback、remove 和 discover 的同步流程，包括稳定标识、端口探测、候选去重、包轮换、失败不落库和所有权检查。

`internal/fnos` 隔离 fnOS 高权限能力。生产环境通过 Unix socket 调用权限助手，测试使用内存实现。`internal/config` 负责原子配置写入，`internal/package` 负责生成并校验登记壳，`internal/http` 负责管理员接口。

## 持久化

```text
data/
├── apps.json
├── icons/<digest>/
│   ├── ICON.PNG
│   └── ICON_256.PNG
├── ownership/<appname>.json
├── packages/
│   ├── current/<id>.fpk
│   └── previous/<id>.{fpk,json}
├── staging/
```

`apps.json` 的唯一业务实体是 `AppSpec`。`lastErrors` 是运维提示映射；ownership 与 package 文件都是实现收据，不形成第二套业务模型。

## 同步流程

- 发现：管理员进入新增流程 → helper 只读列出 TCP 监听端口、Docker 已发布 IPv4 端口、容器网络/PID、WatchCow 标签和已安装 fnOS 入口 → 将原生 IPv4 及双栈通配监听统一为可探测的 IPv4 地址，排除具体 IPv6 地址 → 用 cgroup/PID namespace 补全 host 网络容器归属 → 通过 IPv4 完成 HTTP/HTTPS 可访问性分类 → 从已获取根页记录同源 `<link rel="icon">` 提示但不请求图标 → 按协议、端口、路径匹配已安装入口 → 以内存候选返回并写入可诊断快照；不创建 FPK、不自动登记。IPv6-only 监听和发布地址不进入候选；WatchCow `service_port` 只决定首选候选，不污染同容器的其他端口。
- 创建：验证 → 端口探测 → 分配稳定内部 ID 与 Launch ID → staging 生成（产品说明为空时只在 manifest 中用应用名称补齐 `desc`）→ helper 安装 → **分别验证 `/var/apps/{AppName}/manifest` 与 `target/ui/config`、`DesktopEntryName(AppName)` 的所选 `url/iframe` 打开方式、字符串端口、`url`、权限和两个 PNG 图标** → 保存当前 FPK → 原子创建 AppSpec。通过发现候选创建时，AppSpec 同时保存来源类型、容器或进程名、镜像说明、网络/PID 和 WatchCow 标识的只读快照；这些字段只服务于列表标签和搜索，不参与控制、重绑定或对账。AppName 和入口 ID 同为 `<LaunchID>.dkfn`；Launch ID 只允许 1–27 位小写字母、数字和内部连字符，必须以字母开头，留空时使用 `d` 加内部 ID；打开方式默认 `url`。
- 更新/修复：AppName 不变时，revision +1 → 探测 → helper 仅对 ownership 确认的壳卸载旧 FPK 并安装新 FPK → 校验成功后 current 轮换为 previous → 原子更新 AppSpec。由于 fnOS 的 `install-fpk` 不覆盖已安装手工 FPK，替换失败或桌面入口校验失败会从 `packages/current` 恢复上一成功壳。
- Launch ID 迁移：检查新 appname 无冲突 → 安装并验证新壳 → 移除旧壳 → 轮换包并原子更新 AppSpec。中途失败会移除替代壳并恢复旧壳；内部 12 位 ID 与包历史键不变。
- 回退：读取上一份 AppSpec 内容 → 使用新 revision 重新生成并安装 → 当前/上一次互换。
- 移除：校验配置与所有权标记 → helper 卸载 → 删除 AppSpec 和本项目包文件。

安装或更新失败时保留当前 AppSpec。所有操作同步完成并受超时控制。

## HTTP 接口

共 14 个：

```text
GET    /api/apps
POST   /api/apps
GET    /api/apps/{id}
PUT    /api/apps/{id}
DELETE /api/apps/{id}
POST   /api/apps/{id}/check
POST   /api/apps/{id}/repair
POST   /api/apps/{id}/rollback
GET    /api/system/status
POST   /api/discovery/scan
POST   /api/icons/discover
POST   /api/icons/preview
GET    /api/system/diagnostics
DELETE /api/system/diagnostics
```

前端主页面显示已登记应用，并以标签保留创建时的 Docker、宿主机、网络、进程、镜像和 WatchCow 来源信息。新增流程为“发现服务 → 核对信息 → 创建应用”：候选按容器或进程分组并显示来源标签，不加载业务图标；选择后预填入口信息，并在核对页依次验证 WatchCow/页面图标提示和少量常见 favicon 路径。用户修改访问路径后，在未手动填写、上传或清除图标的前提下，前端防抖请求该本地子页面，后端解析同源 `<link rel="icon">` 和 `<link rel="shortcut icon">`，并依次回退访问路径目录和服务根目录下的有限图标候选（包含 `/public/favicon.png`）；编辑已登记应用时同样执行。管理员可显式重试，识别失败会保留现有或默认图标并显示状态，不执行页面脚本或处理反爬机制。未配置自定义图标时，登记壳使用核对页预览的 DockFN 默认图标并叠加 DockFN 角标。确认创建后立即进入第 3 步显示安装和校验进度；成功状态提供关闭和继续创建，继续创建会重新扫描，失败则返回核对页并保留表单。手动填写从空表单开始。DockFN 提交 `<LaunchID>.dkfn` 入口，入口展示、FN Connect 域名和启动由 fnOS 负责。诊断接口返回脱敏日志尾部及最近一次扫描和安装失败快照；清理接口经 root helper 精确清理这些固定文件，不接受路径参数。
