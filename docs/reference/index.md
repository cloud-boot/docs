---
title: Reference
---

# Reference

The four canonical configuration surfaces that cloud-boot exposes.

<div class="grid cards" markdown>

-   :material-console-line:{ .lg .middle } [__Kernel cmdline__](cmdline.md)

    ---

    Every `cloudboot.*` knob: where it lives, what it accepts,
    and which path it affects. Cross-referenced with the plan
    HCL and metadata-URL JSON.

-   :material-file-code:{ .lg .middle } [__Plan HCL schema__](plan-hcl.md)

    ---

    `target {}` blocks, `local.*` / `self.*` scoping, multi-arch
    OCI indexes, the seven attributes a target carries.

-   :material-link:{ .lg .middle } [__metadata-URL JSON__](metadata-url.md)

    ---

    The `cloudboot` key inside the per-instance metadata
    response. Overrides cmdline values without rebuilding the
    boot ISO.

-   :material-restart:{ .lg .middle } [__NVRAM reset__](nvram-reset.md)

    ---

    How to clear `Boot####` + `BootOrder` after a Path C target
    has been staged. In-band script and out-of-band libvirt
    recipe.

</div>

## Precedence

When the same value appears in multiple places, cloud-boot resolves
in this order (later wins):

1. **Kernel cmdline** — `/proc/cmdline` as parsed by the firmware.
2. **HCL plan** — values from the resolved target.
3. **metadata-URL JSON** — the `cloudboot` block in the per-instance
   metadata response.

So a metadata-URL value overrides both the cmdline and the plan,
which is what you want for per-instance secrets like LUKS
passphrases.
