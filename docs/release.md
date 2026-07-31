# 发布流程

## 发布前

确认工作树内容、版本号和变更记录已经准备完成，然后分别执行测试和 FPK 构建：

```sh
VERSION="$(cat VERSION)"
make verify
make fpk \
  COMMIT="$(git rev-parse HEAD)" \
  BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
```

发布文件只有：

```text
dist/fpk/dockfn-<version>-x86_64.fpk
dist/fpk/dockfn-<version>-arm64.fpk
dist/SHA256SUMS
```

FPK 不携带会触发应用中心协议页的 `LICENSE` 文件；安装向导使用 `wizard/install` 提供安装说明并主动要求填写入口壳存储分区编号。生命周期在安装回调中保存该值，并固定用于后续登记壳。修改安装向导内容时必须递增 [VERSION](../VERSION)，让 fnOS 应用中心按新版本重新读取向导，避免继续使用已缓存的旧包内容。

Windows 可双击根目录的 `build-fpk.cmd` 完成同等的前端构建、交叉编译、FPK 打包和产物校验；该入口仍需要 Git Bash 或 WSL 提供 POSIX `bash`。若标准 `dist/fpk` 不可写，会自动使用 `dist/fpk-<version>`；如果整个 `dist` 目录不可写，需要先处理 WSL 锁定或目录权限。

## GitHub Release

推送 `v*` 标签后，GitHub Actions 会在通过 CI 的同一提交上重新构建产物并创建 Release：

```sh
git tag "v$VERSION"
git push origin "v$VERSION"
```

不发布 Docker 镜像、Docker FPK 或 Compose bundle。CI 会检查两个 FPK 的 ELF 架构、manifest、fnOS UI 配置、生命周期、权限、卸载默认值和校验和。
