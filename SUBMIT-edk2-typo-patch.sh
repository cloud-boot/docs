#!/usr/bin/env bash
# Send the BaseRiscVMmuLib.c assert-typo patch to devel@edk2.groups.io.
#
# Prereqs (do once on this host):
#   - git-send-email installed and on PATH (macOS: `brew install git-send-email`)
#   - ~/.gitconfig sendemail.* fields populated for your SMTP host, or
#     a per-repo .git/config equivalent. Example for a typical TLS host:
#
#       git config --global sendemail.smtpserver        smtp.example.org
#       git config --global sendemail.smtpserverport    587
#       git config --global sendemail.smtpencryption    tls
#       git config --global sendemail.smtpuser          you@example.org
#       git config --global sendemail.suppresscc        self
#       git config --global sendemail.confirm           always
#
# Procedure:
#   1. Clone edk2 master into a scratch dir (the patch was generated against
#      edk2-stable202408 but applies cleanly on master; if it ever stops
#      applying, regenerate against master before sending).
#   2. Apply the staged patch on top of master.
#   3. Run git send-email with the right To: and CC: list.
#
# DO NOT run this without reviewing the patch first.

set -euo pipefail

DOCS_DIR="$(cd "$(dirname "$0")" && pwd)"
PATCH="${DOCS_DIR}/edk2-riscv64-protection-fix.patch"

if [[ ! -f "${PATCH}" ]]; then
  echo "patch not found at ${PATCH}" >&2
  exit 1
fi

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

echo "Cloning tianocore/edk2 (master, shallow) into ${WORK}/edk2 ..."
git -C "${WORK}" clone --depth=64 https://github.com/tianocore/edk2.git edk2 >/dev/null
cd "${WORK}/edk2"

echo "Applying patch ..."
git am "${PATCH}"

echo "Sending via git send-email ..."
git send-email \
  --to='devel@edk2.groups.io' \
  --cc='Ray Ni <ray.ni@intel.com>' \
  --cc='Rahul Kumar <rahul1.kumar@intel.com>' \
  --cc='Sunil V L <sunilvl@ventanamicro.com>' \
  --cc='Andrei Warkentin <andrei.warkentin@intel.com>' \
  --suppress-cc=self \
  --confirm=always \
  HEAD~1..HEAD
