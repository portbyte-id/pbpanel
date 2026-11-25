# open-appsec step 4 — host findings (2026-11-25)

Measured on `root@10.10.11.106` (Ubuntu 26.04, agent `1.1.37-open-source`, local management),
during step 4's rendering work. **No product code was involved; the host policy was restored to the
stock file afterwards** (`restored: [('Any', 'Learn')]`).

## A. `specificRules` needs a complete practice, and the failure is silent

The step-0 note (plan §0.5) gave the mode names and said confidence lives on a practice. It did not
say the practice is **structurally required**. From the v1beta2 schema
(`config/linux/v1beta2/schema/schema_v1beta2.yaml`, `threatPreventionPractices.items.required`):

```
required: [webAttacks, intrusionPrevention, fileSecurity, snortSignatures]
```

A practice with only `webAttacks` is accepted by `--apply-policy` and **silently discarded**:

```
New policy applied.                                # --apply-policy exit 1, which is its success path
/var/log/nano_agent/cp-nano-orchestration.dbg:
  Failed to retrieve AppSec local policy with version: v1beta2, Trying version: v1beta1
  Policy in path: ... was not loaded. Error: rapidjson internal assertion failure: IsObject()
  Could not generate appsec local policy
/etc/cp/conf/policy.json                            # the PREVIOUS policy, untouched
```

With all four blocks present the same rule binds:

```
policy.json → waap.WAAP.WebApplicationSecurity:
  {assetName: opa.test, webAttackMitigationMode: Prevent, webAttackMitigationSeverity: Medium}
  {assetName: Any,      webAttackMitigationMode: Inactive}
rules.rulebase.rulesConfig:
  context: Any(All(Any(EqualHost(opa.test)),EqualListeningPort(80)),
               All(Any(EqualHost(opa.test)),EqualListeningPort(443)))
```

**Consequence for the panel: a policy the agent cannot compile is not an error the panel can see.**
`--apply-policy` exits **1 on success and on failure alike** (the script's success path ends
`echo "New policy applied."; exit 1`), prints "New policy applied" for a policy it then discards, and
`--status` reports `Policy load status: Success` because the *previous* policy is still loaded. The
only observable signal is the absence of `Could not generate appsec local policy` in
`/var/log/nano_agent/cp-nano-orchestration.dbg` after the apply.

## B. The renderer's shape is proven

This is the policy step 4's `RenderPolicy` emits (default `inactive`, one `specificRules` per site,
a practice per confidence, the stock log trigger with local delivery). Applied to the host:

```
rendered-shape: generation_errors=0 rows=3 Any:Inactive|a.test:Learn|b.test:Prevent
```

`detect` → `Learn`, `block` → `Prevent`, and the default stays `Inactive`, as D4 requires.

## C. Exception keys — `urlPath` does not even compile; a path exception is not renderable

Probed with a method that cannot be fooled by the log's asynchrony: one policy per probe, a
distinct host per probe, 30 s settle, and the verdict read from `policy.json` — a policy the agent
cannot compile leaves the **previous** rows in place, so the probe host is absent.

```
baseline (no exceptions key) compiled=YES rows=[('p1.test', 'Prevent'), ('Any', 'Inactive')]
exception paramName          compiled=NO  rows=[('Any', 'Learn')]
exception urlPath            compiled=NO  rows=[('Any', 'Learn')]
exception hostName           compiled=YES rows=[('p4.test', 'Prevent'), ('Any', 'Inactive')]
```

All three probes used `value: /api/`, so the two failures may be a value-shape problem rather than a
key problem — this probe does not separate them. What it does establish is what step 4 needed:

- **A `urlPath` exception does not compile**, on top of step 0's finding (§6.1) that it compiles to an
  empty match when it is accepted at all. Either way it is not a control, so the panel must not
  render one. **Step 4 renders no exceptions at all**, and the policy's header comment says why.
- **`hostName` is viable** if a later step wants a host-wide exception.
- **A rejected policy is invisible to `--apply-policy`**: it exits 1 on success too (the script's
  success path is `echo "New policy applied."; exit 1`), prints "New policy applied", and
  `--status` still says `Policy load status: Success` because the previous policy is loaded. The
  only signal is `policy.json` keeping the old rows. **This is why the apply order must treat the
  policy apply as unverifiable and lean on confedit's rollback for the nginx side.**

## D. The empty-line trap: one probe was a false negative

An earlier probe reported "generation_errors=1" for a policy that in fact compiles. Cause: that
policy's `specificRules` named a practice (`pb-tp-high`) that the document did not define. The
baseline probe here defines and references `default-threat-prevention-practice` and compiles cleanly.
**Every practice a `specificRules` entry names must exist in the same document.**
