# Vendored DMTF Redfish schemas

These files are copies of individual schema documents from a DMTF DSP8010
bundle (see `REDFISH_SCHEMA_BUNDLE` in the top-level `Makefile`), restricted
to the ones reachable from `hack/redfish/spec/openapi.yaml`'s implemented
operations (`../implemented-operations.yaml`).

They exist so `hack/redfish/generate.sh` never has to fetch
`http://redfish.dmtf.org/schemas/v1/...` live: every `$ref` the implemented
operations reach has already been rewritten to point here instead, and every
`$ref` here that pointed to another external schema has been vendored too
and rewritten the same way, recursively.

Two changes were made to each file relative to DMTF's original:

1. External `$ref`s were rewritten to local, sibling-relative paths
   (`./Resource.yaml#/...` instead of `http://redfish.dmtf.org/...`).
2. Properties that are both `$ref`+sibling-keyed (typically
   `readOnly: true`) *and* listed in their schema's `required:` -- DMTF's
   own pattern for `Id`/`Name` and similar server-assigned fields -- were
   wrapped in `allOf` so the sibling key survives OpenAPI 3.0.x's
   $ref-sibling-stripping rule. Without this, openapi-generator can't tell
   those fields are read-only and wrongly requires them on request bodies
   too (see `hack/redfish/vendor-redfish-schemas`'s package doc for the
   full mechanism).

Do not hand-edit these files. To add one that's missing (a new
implemented-operations.yaml entry reaches a schema not vendored yet) or to
refresh the whole set against a newer DMTF bundle, run:

    make vendor-redfish-schema

and commit whatever changes.
