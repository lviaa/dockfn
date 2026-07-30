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
dist/fpk/dockfn-native-x86_64.fpk
dist/fpk/dockfn-native-arm64.fpk
dist/SHA256SUMS
```

## GitHub Release

推送 `v*` 标签后，GitHub Actions 会在通过 CI 的同一提交上重新构建产物并创建 Release：

```sh
git tag "v$VERSION"
git push origin "v$VERSION"
```

不发布 Docker 镜像、Docker FPK 或 Compose bundle。CI 会检查两个 FPK 的 ELF 架构、manifest、fnOS UI 配置、生命周期、权限、卸载默认值和校验和。
