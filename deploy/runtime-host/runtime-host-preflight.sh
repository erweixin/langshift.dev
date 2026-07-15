#!/usr/bin/env bash
set -euo pipefail

fail() { printf 'runtime-host preflight: %s\n' "$1" >&2; exit 1; }

[[ "$(id -u)" == "0" ]] || fail "must run as root"
[[ -c /dev/kvm && -r /dev/kvm && -w /dev/kvm ]] || fail "/dev/kvm is unavailable"
[[ -r /proc/sys/kernel/random/boot_id ]] || fail "Linux boot_id is unavailable"
[[ "$(stat -fc %T /sys/fs/cgroup)" == "cgroup2fs" ]] || fail "cgroup v2 is required"
[[ -x "${JAILER_PATH:?}" && -x "${FIRECRACKER_PATH:?}" ]] || fail "pinned Firecracker binaries are unavailable"
[[ "${JAILER_PATH}" != "${FIRECRACKER_PATH}" ]] || fail "jailer and Firecracker paths must differ"
for binary in "${JAILER_PATH}" "${FIRECRACKER_PATH}"; do
  [[ -f "${binary}" && ! -L "${binary}" && "$(stat -c %u "${binary}")" == "0" ]] || fail "binary ${binary} is not a root-owned regular file"
  mode="$(stat -c %a "${binary}")"
  (( (8#${mode} & 8#022) == 0 )) || fail "binary ${binary} is group/world writable"
done
check_binary() {
  local path="$1" expected="$2" actual
  actual="sha256:$(sha256sum "${path}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || fail "binary digest mismatch for ${path}"
}
check_binary "${JAILER_PATH}" "${JAILER_DIGEST:?}"
check_binary "${FIRECRACKER_PATH}" "${FIRECRACKER_BINARY_DIGEST:?}"
[[ -d "${FIRECRACKER_CHROOT_BASE_DIR:?}" ]] || fail "chroot base directory is unavailable"
[[ "$(stat -c %u "${FIRECRACKER_CHROOT_BASE_DIR}")" == "0" ]] || fail "chroot base must be root-owned"
mode="$(stat -c %a "${FIRECRACKER_CHROOT_BASE_DIR}")"
(( (8#${mode} & 8#022) == 0 )) || fail "chroot base is group/world writable"

check_asset() {
  local path="$1" expected="$2" actual
  [[ -f "${path}" && ! -L "${path}" ]] || fail "asset ${path} is not a regular file"
  [[ "$(stat -c %u "${path}")" == "0" ]] || fail "asset ${path} is not root-owned"
  mode="$(stat -c %a "${path}")"
  (( (8#${mode} & 8#022) == 0 )) || fail "asset ${path} is group/world writable"
  actual="sha256:$(sha256sum "${path}" | awk '{print $1}')"
  [[ "${actual}" == "${expected}" ]] || fail "asset digest mismatch for ${path}"
}

check_asset "${FIRECRACKER_KERNEL_PATH:?}" "${FIRECRACKER_KERNEL_DIGEST:?}"
check_asset "${FIRECRACKER_ROOTFS_PATH:?}" "${FIRECRACKER_ROOTFS_DIGEST:?}"
check_asset "${FIRECRACKER_SCRATCH_PATH:?}" "${RUNTIME_SCRATCH_DIGEST:?}"
[[ -e "${FIRECRACKER_NETWORK_NAMESPACE:?}" && ! -L "${FIRECRACKER_NETWORK_NAMESPACE}" ]] || fail "pinned network namespace is unavailable"
[[ "$(stat -c %u "${FIRECRACKER_NETWORK_NAMESPACE}")" == "0" ]] || fail "network namespace is not root-owned"

for secret in DATABASE_URL_FILE RUNTIME_ID_KEY_FILE RUNTIME_TOKEN_PEPPER_FILE RUNTIME_HOST_CONTROL_TOKEN_FILE RUNTIME_OWNERSHIP_KEY_FILE SERVER_TLS_KEY_FILE; do
  path="${!secret:?}"
  [[ -f "${path}" && ! -L "${path}" && "$(stat -c %u "${path}")" == "0" ]] || fail "${secret} is not a root-owned regular file"
  mode="$(stat -c %a "${path}")"
  (( (8#${mode} & 8#077) == 0 )) || fail "${secret} is accessible outside root"
done

for optional_secret in STORE_EPOCH_TOKEN_FILE STORE_EPOCH_CLIENT_KEY_FILE VAULT_TOKEN_FILE VAULT_CLIENT_KEY_FILE OTLP_BEARER_TOKEN_FILE OTLP_CLIENT_KEY_FILE; do
  path="${!optional_secret:-}"
  [[ -z "${path}" ]] && continue
  [[ -f "${path}" && ! -L "${path}" && "$(stat -c %u "${path}")" == "0" ]] || fail "${optional_secret} is not a root-owned regular file"
  mode="$(stat -c %a "${path}")"
  (( (8#${mode} & 8#077) == 0 )) || fail "${optional_secret} is accessible outside root"
done

[[ -f "${RUNTIME_CAPABILITY_KEYRING_FILE:?}" && ! -L "${RUNTIME_CAPABILITY_KEYRING_FILE}" ]] || fail "capability keyring is unavailable"
[[ "$(stat -c %u "${RUNTIME_CAPABILITY_KEYRING_FILE}")" == "0" ]] || fail "capability keyring is not root-owned"

printf 'runtime-host preflight: passed\n'
