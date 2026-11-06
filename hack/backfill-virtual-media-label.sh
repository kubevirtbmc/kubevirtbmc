#!/usr/bin/env bash
# Label virtual media DataVolumes created by a kubevirtbmc release from before
# bmc.kubevirt.io/virtual-media existed, so the new release can eject and
# replace them.
#
# For every VirtualMachineBMC it looks at the DataVolume named after its VM and
# labels it bmc.kubevirt.io/virtual-media=<vm> only if it looks like media
# kubevirtbmc made (HTTP source, cdi.kubevirt.io/storage.bind.immediate.requested)
# and the VM uses it as nothing but its CD-ROM (not a dataVolumeTemplate, not
# owned by the VM, not another disk). Anything else is reported and skipped.
#
# Dry run by default; pass --apply to label. Needs kubectl and jq.
#   make virtualmedialabel                # show what would be labeled
#   make virtualmedialabel APPLY=1        # label
#   make virtualmedialabel NAMESPACE=foo  # one namespace only
# or run hack/backfill-virtual-media-label.sh [--apply] directly.
set -euo pipefail

KUBECTL="${KUBECTL:-kubectl}"

LABEL=bmc.kubevirt.io/virtual-media
APPLY=0
[ "${1:-}" = "--apply" ] && APPLY=1
for t in "$KUBECTL" jq; do command -v "$t" >/dev/null || { echo "$t not found" >&2; exit 1; }; done

if [ -n "${NAMESPACE:-}" ]; then scope=(-n "$NAMESPACE"); else scope=(-A); fi

labeled=0 skipped=0
while read -r ns vm; do
  [ -n "$vm" ] || continue
  if ! dv="$("$KUBECTL" -n "$ns" get datavolume "$vm" -o json 2>/dev/null)"; then
    continue # no DataVolume named after the VM: nothing to backfill
  fi
  if [ "$(jq -r --arg l "$LABEL" '.metadata.labels[$l] // ""' <<<"$dv")" = "$vm" ]; then
    continue # already labeled
  fi
  vmjson="$("$KUBECTL" -n "$ns" get vm "$vm" -o json 2>/dev/null || echo '{}')"

  reason="$(jq -r --argjson vm "$vmjson" --arg n "$vm" '
    def cdvols: [($vm.spec.template.spec.domain.devices.disks // [])[] | select(.cdrom != null) | .name];
    def refs:   [($vm.spec.template.spec.volumes // [])[]
                 | select((.dataVolume.name // "") == $n
                          or (.persistentVolumeClaim.claimName // "") == $n
                          or (.ephemeral.persistentVolumeClaim.claimName // "") == $n)
                 | .name];
    if (.spec.source.http.url // "") == "" then "no HTTP source"
    elif (.metadata.annotations // {} | has("cdi.kubevirt.io/storage.bind.immediate.requested")) | not
      then "no bind-immediate annotation"
    elif [($vm.spec.dataVolumeTemplates // [])[] | .metadata.name] | index($n) != null
      then "a dataVolumeTemplate of the VM"
    elif [(.metadata.ownerReferences // [])[] | .uid] | index($vm.metadata.uid // "-") != null
      then "owned by the VM"
    elif (refs - cdvols) | length > 0 then "used by the VM as a disk other than the CD-ROM"
    else "" end' <<<"$dv")"

  if [ -n "$reason" ]; then
    echo "skip   $ns/$vm: $reason"
    skipped=$((skipped + 1))
    continue
  fi
  if [ "$APPLY" = 1 ]; then
    "$KUBECTL" -n "$ns" label datavolume "$vm" "$LABEL=$vm" >/dev/null
    echo "label  $ns/$vm"
  else
    echo "would label $ns/$vm"
  fi
  labeled=$((labeled + 1))
done < <("$KUBECTL" get virtualmachinebmcs.bmc.kubevirt.io "${scope[@]}" \
           -o jsonpath='{range .items[*]}{.metadata.namespace}{" "}{.spec.virtualMachineRef.name}{"\n"}{end}')

echo
if [ "$APPLY" = 1 ]; then echo "labeled $labeled, skipped $skipped"
else echo "$labeled to label, $skipped skipped (dry run; --apply to label)"; fi
