#!/bin/sh
set -eu

version="${1:?version required}"
root="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
out="$root/dist/fpk"
rm -rf "$out"
mkdir -p "$out"

for arch in amd64 arm64; do
  case "$arch" in
    amd64)
      platform=x86
      fnos_arch=x86_64
      artifact_arch=x86_64
      ;;
    arm64)
      platform=arm
      fnos_arch=aarch64
      artifact_arch=arm64
      ;;
  esac

  work="$root/dist/.fpk-native-$arch"
  rm -rf "$work"
  mkdir -p "$work/app/target" "$work/app/ui/images" "$work/cmd" "$work/config" "$work/wizard"
  sed \
    -e "s/{{VERSION}}/$version/g" \
    -e "s/{{PLATFORM}}/$platform/g" \
    -e "s/{{ARCH}}/$fnos_arch/g" \
    "$root/packaging/fnos/common/manifest.template" >"$work/manifest"
  for script in main migrate install_init install_callback upgrade_init upgrade_callback uninstall_init uninstall_callback config_init config_callback preflight; do
    cp "$root/packaging/fnos/common/cmd/$script" "$work/cmd/$script"
  done
  cp "$root/packaging/fnos/common/wizard/uninstall" "$work/wizard/uninstall"
  cp "$root/packaging/fnos/common/app/ui/config" "$work/app/ui/config"
  cp "$root/assets/ICON.PNG" "$work/ICON.PNG"
  cp "$root/assets/ICON_256.PNG" "$work/ICON_256.PNG"
  cp "$root/assets/ICON.PNG" "$work/app/ui/images/icon_64.png"
  cp "$root/assets/ICON_256.PNG" "$work/app/ui/images/icon_256.png"
  cp "$root/LICENSE" "$work/LICENSE"
  cp "$root/packaging/fnos/native/resource" "$work/config/resource"
  cp "$root/packaging/fnos/native/privilege" "$work/config/privilege"
  cp "$root/bin/dockfn-linux-$arch" "$work/app/target/dockfn"
  chmod 755 "$work"/cmd/* "$work/app/target/dockfn"

  output="$out/dockfn-native-$artifact_arch.fpk"
  if command -v fnpack >/dev/null 2>&1; then
    fnpack_out="$root/dist/.fnpack-native-$arch"
    rm -rf "$fnpack_out"
    mkdir -p "$fnpack_out"
    (cd "$fnpack_out" && fnpack build -d "$work")
    built="$(find "$fnpack_out" -maxdepth 1 -type f -name '*.fpk' -print)"
    [ -n "$built" ] && [ "$(printf '%s\n' "$built" | wc -l)" -eq 1 ] || {
      echo "fnpack did not produce exactly one FPK" >&2
      exit 1
    }
    mv "$built" "$output"
    rm -rf "$fnpack_out"
  else
    package_dir="$root/dist/.package-native-$arch"
    app_dir="$root/dist/.app-native-$arch"
    rm -rf "$package_dir" "$app_dir"
    mkdir -p "$package_dir" "$app_dir"
    cp -R "$work/app/." "$app_dir/"
    cp -R "$work/config" "$app_dir/config"
    tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 -czf "$package_dir/app.tgz" -C "$app_dir" .
    cp "$work/manifest" "$work/ICON.PNG" "$work/ICON_256.PNG" "$work/LICENSE" "$package_dir/"
    cp -R "$work/cmd" "$work/config" "$work/wizard" "$package_dir/"
    tar --sort=name --mtime='UTC 1970-01-01' --owner=0 --group=0 -czf "$output" -C "$package_dir" .
    rm -rf "$package_dir" "$app_dir"
  fi
done

rm -rf \
  "$root/dist/.fpk-native-amd64" "$root/dist/.fpk-native-arm64" \
  "$root/dist/.fnpack-native-amd64" "$root/dist/.fnpack-native-arm64" \
  "$root/dist/.package-native-amd64" "$root/dist/.package-native-arm64" \
  "$root/dist/.app-native-amd64" "$root/dist/.app-native-arm64"
