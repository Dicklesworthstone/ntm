Canary fixture anchor for scripts/guards/binary_bloat_gate.sh.
Synthetic ELF, Mach-O, PE, and >2 MiB bloat payloads are materialized in a
temporary directory during each self-test run so zero binary bytes are tracked
in Git to test the binary bloat guard itself.
