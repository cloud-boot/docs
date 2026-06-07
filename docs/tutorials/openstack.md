---
title: Quickstart — OpenStack
---

# Quickstart — OpenStack with Keystone application credentials

Push `boot.iso` to Glance once, then drive each instance with a
**per-instance metadata-URL** so the plan can change without
rebuilding the image. Authentication via **Keystone application
credentials** — long-lived, project-scoped, rotatable, never the
operator's password.

## 1. Upload the boot image (once)

```bash
cloud-boot build --arch aarch64 --iso boot.iso \
  --cmdline "cloudboot.exit=reboot"

openstack image create cloud-boot \
    --file boot.iso \
    --disk-format raw \
    --container-format bare \
    --property hw_firmware_type=uefi \
    --property hw_architecture=aarch64
```

The image carries only the bootstrap kernel + `cloud-boot-init`. No
per-tenant rebuild ever needs to happen.

## 2. Create an application credential

```bash
openstack application credential create cloud-boot-runner \
    --role member \
    --description "cloud-boot per-instance auth"
```

The output gives you `id`, `secret`. Keep these as project-scoped
secrets — both are needed by cloud-boot at runtime.

## 3. Stand up an instance with cloud-boot

```bash
openstack server create \
    --image cloud-boot \
    --flavor m1.small \
    --network production \
    --user-data user-data.yaml \
    cloudboot-test-01
```

Where `user-data.yaml` carries the cloud-boot configuration in the
per-instance metadata service. `cloud-boot-init` reads that JSON
on every boot (and re-reads it on every Path C reboot), so you can
change the plan without restarting the instance from scratch.

## 4. The metadata-URL JSON

```json
{
  "cloudboot": {
    "plan":   "harbor.example.com/boot/prod:latest",
    "target": "primary",
    "exit":   "reboot",
    "keymap": "fr-mac",

    "openstack": {
      "auth_url":          "https://keystone.example.com:5000/v3",
      "app_cred_id":       "13af9ad7e9...",
      "app_cred_secret":   "S3cr3t!..."
    }
  }
}
```

cloud-boot-init reads this via `cloudboot.metadata.url=` (default
`http://169.254.169.254/openstack/latest/meta_data.json`). The
top-level `cloudboot` key is the only one it cares about; anything
else in the document is preserved for cloud-init or your own
agents.

See [Reference / metadata-URL JSON](../reference/metadata-url.md) for
the full key list, including LUKS passphrases and ZFS dataset
overrides.

## 5. Push the plan to Harbor

```bash
# Harbor with the keystone-auth plugin accepts the application
# credential as a bearer token. cloud-boot reuses the same token
# for metadata-URL GETs and OCI pulls.

cloud-boot push plan harbor.example.com/boot/prod:latest -f prod.hcl
cloud-boot push artifact harbor.example.com/boot/linux:6.6 \
    --kernel ./vmlinuz-6.6 --initrd ./initrd-6.6
cloud-boot push index harbor.example.com/boot/linux:6.6 \
    --arm64 harbor.example.com/boot/linux:6.6-arm64 \
    --amd64 harbor.example.com/boot/linux:6.6-amd64
```

## 6. Rotating credentials

```bash
# Mint a new AC
openstack application credential create cloud-boot-runner-v2 \
    --role member \
    --description "rolling rotation 2026Q3"

# Update the metadata-URL JSON for affected instances
openstack server set --user-data new-user-data.yaml cloudboot-test-01

# Old AC keeps working until you explicitly delete it
openstack application credential delete cloud-boot-runner-v1
```

Because the metadata-URL is consulted on every boot, the next
reboot picks up the new credentials. No `boot.iso` rebuild, no
re-signing, no Glance churn.

## 7. NVRAM reset on the compute host

Each Path C instance has its own NVRAM (`*.nvram` next to the
libvirt domain XML). To wipe `BootOrder` / `Boot0001` so the
instance re-runs the menu on the next boot:

=== "In-band (operator can SSH into the guest)"

    ```bash
    ssh guest sudo /sbin/reset-cloud-boot.sh
    ```

=== "Out-of-band (operator has compute-node access)"

    ```bash
    virsh undefine cloudboot-test-01 --keep-nvram=no
    virsh define /etc/libvirt/qemu/cloudboot-test-01.xml
    virsh start cloudboot-test-01
    ```

The script and the libvirt incantation are also explained in
[Reference / NVRAM reset](../reference/nvram-reset.md).

## Wiring with Magnum, Heat, or Cloud-Init

Magnum / Heat templates accept user-data; just put the JSON shown
above inside a `# cloud-config`-style header or a raw user-data
file. Cloud-init's `meta_data.json` is exactly what
`cloud-boot-init` reads, so existing tooling that targets
`169.254.169.254` works unchanged.

## Path A vs Path C on OpenStack

Both work. Pick:

- **Path A** (`cloudboot.exit=kexec`) if you want one-shot boots —
  no NVRAM state survives, every reboot re-runs the plan.
- **Path C** (`cloudboot.exit=reboot`) if you want the firmware's
  `BootOrder` to honour a staged target across reboots — handy
  with PXE-less infrastructure or instances that should survive a
  metadata-service outage.

For shared infrastructure where instances might be migrated between
hosts, Path A is usually simpler: it leaves no NVRAM state behind.
For long-running production instances where the boot decision is
stable, Path C is faster on subsequent boots (no plan pull, no FS
walk — firmware loads the staged target directly).

## Next

- [Reference / cmdline](../reference/cmdline.md) — every
  `cloudboot.*` knob.
- [Reference / plan HCL](../reference/plan-hcl.md) — full HCL
  schema with `local.*` / `self.*` semantics.
- [Filesystem drivers](../filesystems/index.md) — what cloud-boot
  reads on the chained distro side.
