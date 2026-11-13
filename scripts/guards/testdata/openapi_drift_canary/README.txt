Canary fixture directory for scripts/guards/openapi_drift_gate.sh (bd-rc-track-e-uplift-xkt68.1).
Verifies that server URL drift, parity_matrix <-> openapi path mismatch, untyped /api/v1/* schema
regression (<98%), and missing paths in web/src/lib/api/schema.d.ts all fire deterministically.
